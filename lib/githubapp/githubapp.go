// Package githubapp is the operator-level GitHub App seam used to pull charts
// from private Git repositories. The operator registers one GitHub App and
// supplies its App ID + RSA private key (see lib/config); each organization
// installs that App on its repositories, and the backend mints a short-lived
// installation access token per rollout attempt to authenticate the clone — no
// git secret is stored per organization.
//
// The Authenticator holds the parsed private key and talks to the GitHub REST
// API: it signs an App JWT (RS256, app-wide), exchanges it for an installation
// token, and looks an installation up to confirm it exists and read the account
// it is installed on. It also signs/verifies the short-lived state token that
// binds the connect → install → callback flow to the initiating organization
// (see SignState).
package githubapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// defaultBaseURL is the GitHub REST API root. Overridable on the Authenticator
// so tests can point it at an httptest server.
const defaultBaseURL = "https://api.github.com"

// Authenticator mints GitHub App credentials from the operator's App ID and RSA
// private key. The zero value is unusable; construct it with New.
type Authenticator struct {
	appID   int64
	key     *rsa.PrivateKey
	baseURL string
	http    *http.Client
}

// New parses the PEM private key and returns an Authenticator for the given
// numeric App ID. It errors if the key can't be parsed, so a misconfigured
// GitHub App fails fast at startup rather than at first rollout.
func New(appID int64, privateKeyPEM string) (*Authenticator, error) {
	if appID == 0 {
		return nil, errors.New("githubapp: app id is required")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse private key: %w", err)
	}
	return &Authenticator{
		appID:   appID,
		key:     key,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// AppJWT signs an app-wide JWT (RS256) used to authenticate as the GitHub App
// itself — for looking up installations and exchanging for installation tokens.
// GitHub rejects an `iat` in the future and an `exp` more than 10 minutes out,
// so we back-date `iat` 60s for clock skew and set a 9-minute expiry.
func (a *Authenticator) AppJWT() (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(a.appID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),
	})
	return token.SignedString(a.key)
}

// InstallationToken exchanges the App JWT for a short-lived (~1h) installation
// access token scoped to the given installation. It is called late, per rollout
// attempt, so River retries always carry a fresh token.
func (a *Authenticator) InstallationToken(ctx context.Context, installationID int64) (token string, expiresAt time.Time, err error) {
	var body struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.baseURL, installationID)
	if err := a.do(ctx, http.MethodPost, url, &body); err != nil {
		return "", time.Time{}, err
	}
	return body.Token, body.ExpiresAt, nil
}

// Installation is the subset of a GitHub App installation we record: which
// account (org or user) it is installed on.
type Installation struct {
	Login       string
	AccountType string
}

// GetInstallation looks an installation up by id, confirming it exists for this
// App and returning the account it is installed on. Used on the connect
// callback to verify before storing.
func (a *Authenticator) GetInstallation(ctx context.Context, installationID int64) (Installation, error) {
	var body struct {
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
	}
	url := fmt.Sprintf("%s/app/installations/%d", a.baseURL, installationID)
	if err := a.do(ctx, http.MethodGet, url, &body); err != nil {
		return Installation{}, err
	}
	return Installation{Login: body.Account.Login, AccountType: body.Account.Type}, nil
}

// Repository is the subset of a repository accessible to an installation that
// the repository picker needs: its full name (owner/repo), HTTPS clone URL,
// whether it is private, and its default branch.
type Repository struct {
	FullName      string
	CloneURL      string
	Private       bool
	DefaultBranch string
}

// ListRepositories returns every repository the installation can reach. Unlike
// the App-level calls above, GitHub's /installation/repositories endpoint is
// authenticated with an installation access token (not the App JWT), so this
// mints one first and pages through the results (100 per page) until a short
// page signals the end.
func (a *Authenticator) ListRepositories(ctx context.Context, installationID int64) ([]Repository, error) {
	token, _, err := a.InstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	const perPage = 100
	var repos []Repository
	for page := 1; ; page++ {
		var body struct {
			Repositories []struct {
				FullName      string `json:"full_name"`
				CloneURL      string `json:"clone_url"`
				Private       bool   `json:"private"`
				DefaultBranch string `json:"default_branch"`
			} `json:"repositories"`
		}
		url := fmt.Sprintf("%s/installation/repositories?per_page=%d&page=%d", a.baseURL, perPage, page)
		if err := a.doToken(ctx, url, token, &body); err != nil {
			return nil, err
		}
		for _, r := range body.Repositories {
			repos = append(repos, Repository{
				FullName:      r.FullName,
				CloneURL:      r.CloneURL,
				Private:       r.Private,
				DefaultBranch: r.DefaultBranch,
			})
		}
		if len(body.Repositories) < perPage {
			return repos, nil
		}
	}
}

// doToken performs a GET authenticated with an installation access token
// (Authorization: token …), as opposed to do which signs as the App. It shares
// do's Accept/version headers and non-2xx error shape.
func (a *Authenticator) doToken(ctx context.Context, url, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("githubapp: GET %s: status %d: %s", url, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// do performs a GitHub API request authenticated as the App and decodes a JSON
// response into out. A non-2xx response is returned as an error including the
// status, so callers (and ultimately the rollout) get a clear failure.
func (a *Authenticator) do(ctx context.Context, method, url string, out any) error {
	appJWT, err := a.AppJWT()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("githubapp: %s %s: status %d: %s", method, url, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- state token ---------------------------------------------------------

// stateTTL bounds how long an issued connect state is valid: long enough to
// install the App on GitHub, short enough to limit replay.
const stateTTL = 15 * time.Minute

// stateClaims is the payload bound into the connect state token: the
// organization that initiated the connect, a nonce, and an expiry.
type stateClaims struct {
	Org   uuid.UUID `json:"org"`
	Nonce string    `json:"nonce"`
	Exp   int64     `json:"exp"`
}

// SignState mints a tamper-evident state token binding a connect flow to the
// initiating organization, HMAC-SHA256-keyed by the deployment's secret key
// (the same base64 key as lib/secrets). It is round-tripped through GitHub's
// install redirect and verified on the callback: because the App JWT can read
// every installation of the App, verifying the installation alone would let a
// caller attach an installation id they don't own — the state proves the same
// org that initiated the connect is the one completing it.
//
// secretKey is the base64-encoded key; an empty key is rejected so the caller
// surfaces a clear "set SPACEFLEET_SECRET_KEY" error rather than signing with
// an empty key.
func SignState(secretKey string, org uuid.UUID) (string, error) {
	key, err := decodeKey(secretKey)
	if err != nil {
		return "", err
	}
	claims := stateClaims{Org: org, Nonce: uuid.NewString(), Exp: time.Now().Add(stateTTL).Unix()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := signMAC(key, body)
	return body + "." + mac, nil
}

// VerifyState validates a state token and returns the organization it was
// issued for. It errors when the token is malformed, the signature doesn't
// match, or it has expired. The caller must additionally check the returned org
// equals the current request's org.
func VerifyState(secretKey, state string) (uuid.UUID, error) {
	key, err := decodeKey(secretKey)
	if err != nil {
		return uuid.Nil, err
	}
	body, mac, ok := strings.Cut(state, ".")
	if !ok {
		return uuid.Nil, errors.New("githubapp: malformed state")
	}
	expected := signMAC(key, body)
	if subtle.ConstantTimeCompare([]byte(mac), []byte(expected)) != 1 {
		return uuid.Nil, errors.New("githubapp: state signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return uuid.Nil, errors.New("githubapp: malformed state payload")
	}
	var claims stateClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return uuid.Nil, errors.New("githubapp: malformed state payload")
	}
	if time.Now().Unix() > claims.Exp {
		return uuid.Nil, errors.New("githubapp: state expired")
	}
	return claims.Org, nil
}

func decodeKey(secretKey string) ([]byte, error) {
	if secretKey == "" {
		return nil, errors.New("githubapp: secret key is required to sign connect state (set SPACEFLEET_SECRET_KEY)")
	}
	key, err := base64.StdEncoding.DecodeString(secretKey)
	if err != nil {
		return nil, fmt.Errorf("githubapp: decode secret key: %w", err)
	}
	return key, nil
}

func signMAC(key []byte, body string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
