package email

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a single-connection SMTP server that speaks just enough of the
// protocol for net/smtp, never advertises STARTTLS (like a local catcher or a
// capability-stripping MITM), and records every command line it receives.
type fakeSMTP struct {
	host string
	port int

	mu       sync.Mutex
	commands []string
	data     string
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	f := &fakeSMTP{host: host, port: port}

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		br := bufio.NewReader(conn)
		fmt.Fprintf(conn, "220 fake ESMTP\r\n")
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimRight(line, "\r\n")
			f.mu.Lock()
			f.commands = append(f.commands, cmd)
			f.mu.Unlock()

			switch verb := strings.ToUpper(cmd); {
			case strings.HasPrefix(verb, "EHLO"):
				fmt.Fprintf(conn, "250-fake\r\n250 AUTH PLAIN LOGIN\r\n")
			case strings.HasPrefix(verb, "HELO"):
				fmt.Fprintf(conn, "250 fake\r\n")
			case strings.HasPrefix(verb, "MAIL"), strings.HasPrefix(verb, "RCPT"):
				fmt.Fprintf(conn, "250 ok\r\n")
			case strings.HasPrefix(verb, "DATA"):
				fmt.Fprintf(conn, "354 go ahead\r\n")
				var body strings.Builder
				for {
					l, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimRight(l, "\r\n") == "." {
						break
					}
					body.WriteString(l)
				}
				f.mu.Lock()
				f.data = body.String()
				f.mu.Unlock()
				fmt.Fprintf(conn, "250 queued\r\n")
			case strings.HasPrefix(verb, "QUIT"):
				fmt.Fprintf(conn, "221 bye\r\n")
				return
			default:
				fmt.Fprintf(conn, "250 ok\r\n")
			}
		}
	}()
	return f
}

func (f *fakeSMTP) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func (f *fakeSMTP) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

// When StartTLS is configured and the server doesn't advertise it, Send must
// fail before anything sensitive (AUTH credentials, the message with its
// invite token) crosses the wire — not silently downgrade to cleartext.
func TestSendRefusesCleartextWhenStartTLSConfigured(t *testing.T) {
	srv := startFakeSMTP(t)
	sender := NewSMTP(Config{
		Host:     srv.host,
		Port:     srv.port,
		Username: "user",
		Password: "hunter2",
		From:     "no-reply@example.com",
		StartTLS: true,
	})

	err := sender.Send(context.Background(), Message{To: "a@example.com", Subject: "hi", Text: "secret-invite-token"})
	if err == nil {
		t.Fatal("Send succeeded over cleartext despite StartTLS being configured")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("error should name STARTTLS, got: %v", err)
	}
	for _, cmd := range srv.received() {
		switch verb := strings.ToUpper(cmd); {
		case strings.HasPrefix(verb, "AUTH"),
			strings.HasPrefix(verb, "MAIL"),
			strings.HasPrefix(verb, "RCPT"),
			strings.HasPrefix(verb, "DATA"):
			t.Fatalf("client sent %q in cleartext after the missing-STARTTLS check should have aborted", cmd)
		}
	}
	if srv.body() != "" {
		t.Fatalf("message body reached the server in cleartext: %q", srv.body())
	}
}

// With StartTLS explicitly disabled (a local catcher like MailHog), the
// cleartext path still delivers.
func TestSendCleartextWhenStartTLSDisabled(t *testing.T) {
	srv := startFakeSMTP(t)
	sender := NewSMTP(Config{
		Host:     srv.host,
		Port:     srv.port,
		From:     "no-reply@example.com",
		StartTLS: false,
	})

	if err := sender.Send(context.Background(), Message{To: "a@example.com", Subject: "hi", Text: "hello"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(srv.body(), "hello") {
		t.Fatalf("server never received the message body, got: %q", srv.body())
	}
}
