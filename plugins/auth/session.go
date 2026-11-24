// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"oblivion/sdk"
)

const (
	sessionAbsolute = 7 * 24 * time.Hour
	sessionIdle     = 12 * time.Hour
	touchEvery      = time.Minute
	csrfHeader      = "X-CSRF-Token"
)

func (p *Plugin) cookieName() string {
	if p.rt.SecureCookies {
		return "__Host-sid" // __Host- needs Secure, Path=/ and no Domain
	}
	return "sid"
}

func (p *Plugin) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	c := &http.Cookie{
		Name: p.cookieName(), Value: token, Path: "/",
		HttpOnly: true, Secure: p.rt.SecureCookies, SameSite: http.SameSiteStrictMode,
	}
	if token == "" {
		c.MaxAge = -1
	} else {
		c.Expires = expires
	}
	http.SetCookie(w, c)
}

func hashToken(tok string) []byte { h := sha256.Sum256([]byte(tok)); return h[:] }

func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newSession stores a fresh session for userID and returns (token, csrf, expiry).
func (p *Plugin) newSession(r *http.Request, userID int64) (string, string, time.Time, error) {
	tok, err := randToken(32)
	if err != nil {
		return "", "", time.Time{}, err
	}
	csrf, err := randToken(24)
	if err != nil {
		return "", "", time.Time{}, err
	}
	now := p.now()
	exp := now.Add(sessionAbsolute)
	_, err = p.db.ExecContext(r.Context(),
		`INSERT INTO auth_sessions(token_hash, user_id, csrf, created_at, last_seen, expires_at) VALUES (?,?,?,?,?,?)`,
		hashToken(tok), userID, csrf, now.Unix(), now.Unix(), exp.Unix())
	return tok, csrf, exp, err
}

type sessionInfo struct {
	principal *sdk.Principal
	csrf      string
	tokenHash []byte
}

// lookup resolves the session cookie of r. A missing, unknown, expired or idle
// session, or a disabled user, yields (nil, nil): the request is anonymous.
func (p *Plugin) lookup(r *http.Request) (*sessionInfo, error) {
	c, err := r.Cookie(p.cookieName())
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return nil, nil
	}
	th := hashToken(c.Value)
	var (
		uid               int64
		name, role, csrf  string
		disabled          int
		lastSeen, expires int64
	)
	err = p.db.QueryRowContext(r.Context(), `
		SELECT u.id, u.username, u.role, u.disabled, s.csrf, s.last_seen, s.expires_at
		FROM auth_sessions s JOIN auth_users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, th).Scan(&uid, &name, &role, &disabled, &csrf, &lastSeen, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := p.now()
	if disabled == 1 || now.Unix() >= expires || now.Sub(time.Unix(lastSeen, 0)) > sessionIdle {
		return nil, nil
	}
	if now.Sub(time.Unix(lastSeen, 0)) > touchEvery {
		// Best effort: a failed touch must not fail the request.
		_, _ = p.db.ExecContext(r.Context(), `UPDATE auth_sessions SET last_seen = ? WHERE token_hash = ?`, now.Unix(), th)
	}
	return &sessionInfo{principal: principalFor(uid, name, role), csrf: csrf, tokenHash: th}, nil
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// csrfExempt: routes that run before a session exists.
func csrfExempt(path string) bool {
	return path == "/api/v1/auth/login" || path == "/api/v1/auth/setup"
}

// Authenticate implements sdk.Identity.
func (p *Plugin) Authenticate(r *http.Request) (*sdk.Principal, error) {
	s, err := p.lookup(r)
	if err != nil || s == nil {
		return nil, err
	}
	if !isSafeMethod(r.Method) && !csrfExempt(r.URL.Path) {
		if err := checkCSRF(r, s.csrf); err != nil {
			return nil, fmt.Errorf("%w: %v", sdk.ErrForbidden, err)
		}
	}
	return s.principal, nil
}

// checkCSRF requires the per-session token header (constant-time compare) and,
// when the browser sends them, same-origin Origin / Sec-Fetch-Site values.
func checkCSRF(r *http.Request, want string) error {
	got := r.Header.Get(csrfHeader)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return errors.New("csrf token missing or invalid")
	}
	return checkOrigin(r)
}

// checkOrigin rejects cross-site requests when the browser tells us about them.
func checkOrigin(r *http.Request) error {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return errors.New("cross-site request")
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return errors.New("origin mismatch")
		}
	}
	return nil
}

// revokeSessions deletes every session of userID, except the one whose token
// hash is `except` (pass nil to delete all).
func (p *Plugin) revokeSessions(ctx context.Context, userID int64, except []byte) error {
	if except == nil {
		_, err := p.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ?`, userID)
		return err
	}
	_, err := p.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ? AND token_hash <> ?`, userID, except)
	return err
}
