// Package email is Spacefleet's thin outbound-email seam. The rest of the app
// depends on the Sender interface; the concrete transport is SMTP (configured
// from the environment / Helm values). When SMTP is not configured the server
// wires in a Noop sender, so callers never have to special-case "email is off"
// — features that send mail (today: invitations) keep working and simply skip
// the send. Whether email is actually enabled is a config concern, surfaced to
// the UI separately so it can adjust wording.
package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Message is a single outbound email. Both Text and HTML are optional; a
// well-formed message supplies at least one. When both are present they are
// sent as a multipart/alternative so clients pick the richer part.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a Message. Implementations must be safe for concurrent use.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Noop is a Sender that discards messages. It's wired in when SMTP isn't
// configured so callers don't have to nil-check.
type Noop struct{}

func (Noop) Send(context.Context, Message) error { return nil }

// Config is the SMTP transport configuration.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// StartTLS upgrades the connection with STARTTLS after connecting (the usual
	// submission setup on port 587). When the server doesn't advertise STARTTLS
	// (e.g. a local catcher like MailHog) the upgrade is skipped.
	StartTLS bool
}

// SMTPSender delivers mail over SMTP.
type SMTPSender struct {
	cfg Config
}

// NewSMTP builds an SMTP-backed Sender from cfg.
func NewSMTP(cfg Config) *SMTPSender { return &SMTPSender{cfg: cfg} }

// Send connects to the SMTP server and delivers msg. It honors ctx for the
// initial connection (net/smtp itself isn't context-aware once connected, so a
// connection deadline derived from ctx bounds the rest of the exchange).
func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	if msg.To == "" {
		return errors.New("email: message has no recipient")
	}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))

	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("email: dial %s: %w", addr, err)
	}
	// Bound the remaining (non-context-aware) exchange. Use the context deadline
	// when present, otherwise a sane default.
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("email: smtp client: %w", err)
	}
	defer func() { _ = c.Close() }()

	if s.cfg.StartTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host}); err != nil {
				return fmt.Errorf("email: starttls: %w", err)
			}
		}
	}

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}

	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("email: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(msg.To); err != nil {
		return fmt.Errorf("email: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: DATA: %w", err)
	}
	if _, err := w.Write(s.build(msg)); err != nil {
		return fmt.Errorf("email: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: close body: %w", err)
	}
	return c.Quit()
}

// build renders msg into RFC 5322 bytes. With both parts it emits a
// multipart/alternative; with one, a single-part message of the right type.
func (s *SMTPSender) build(msg Message) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", s.cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", msg.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", msg.Subject)
	b.WriteString("MIME-Version: 1.0\r\n")

	switch {
	case msg.HTML != "" && msg.Text != "":
		const boundary = "spacefleet-boundary-7f3a1c"
		fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
		fmt.Fprintf(&b, "--%s\r\n", boundary)
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		writeBody(&b, msg.Text)
		fmt.Fprintf(&b, "\r\n--%s\r\n", boundary)
		b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		writeBody(&b, msg.HTML)
		fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	case msg.HTML != "":
		b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		writeBody(&b, msg.HTML)
	default:
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		writeBody(&b, msg.Text)
	}
	return b.Bytes()
}

// writeBody writes body with bare LFs normalized to CRLF, as SMTP expects.
func writeBody(b *bytes.Buffer, body string) {
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
}
