// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oblivion/internal/db"
	"oblivion/internal/plugins"
	"oblivion/sdk"
)

const (
	adminPW = "correct horse battery"
	otherPW = "another long password"
)

type env struct {
	t   *testing.T
	p   *Plugin
	h   http.Handler
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &env{t: t, p: New(), now: time.Unix(1_800_000_000, 0)}
	e.p.now = func() time.Time { return e.now }
	m := plugins.New(slog.New(slog.NewTextHandler(io.Discard, nil)), d, "auth")
	m.SetRuntime(sdk.Runtime{ClientIP: func(r *http.Request) string { return r.Header.Get("X-Test-IP") }})
	m.Register(e.p)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.h = m.IdentityMiddleware(m.APIHandler())
	return e
}

type client struct {
	e      *env
	cookie *http.Cookie
	csrf   string
	ip     string
}

func (e *env) client(ip string) *client { return &client{e: e, ip: ip} }

func (c *client) do(method, path string, body any) *httptest.ResponseRecorder {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, "http://cms.test/api/v1/auth"+path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("X-Test-IP", c.ip)
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	if c.csrf != "" {
		r.Header.Set(csrfHeader, c.csrf)
	}
	w := httptest.NewRecorder()
	c.e.h.ServeHTTP(w, r)
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "sid" {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
		}
	}
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad JSON %q: %v", w.Body.String(), err)
	}
	return m
}

func want(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, code, w.Body.String())
	}
}

func (c *client) login(user, pw string) *httptest.ResponseRecorder {
	w := c.do("POST", "/login", map[string]string{"username": user, "password": pw})
	if w.Code == 200 {
		c.csrf = decodeBody(c.e.t, w)["csrf"].(string)
	}
	return w
}

// setupAdmin completes first-run setup and returns a logged-in admin client.
func (e *env) setupAdmin() *client {
	e.t.Helper()
	c := e.client("192.0.2.1")
	w := c.do("POST", "/setup", map[string]string{"token": e.p.setupToken, "username": "admin", "password": adminPW})
	want(e.t, w, 201)
	c.csrf = decodeBody(e.t, w)["csrf"].(string)
	return c
}

func (c *client) createUser(name, role string) int64 {
	c.e.t.Helper()
	w := c.do("POST", "/users", map[string]string{"username": name, "password": otherPW, "role": role})
	want(c.e.t, w, 201)
	return int64(decodeBody(c.e.t, w)["id"].(float64))
}

func TestSetupFlow(t *testing.T) {
	e := newEnv(t)
	c := e.client("192.0.2.1")
	if e.p.setupToken == "" {
		t.Fatal("no setup token issued on an empty database")
	}
	st := c.do("GET", "/setup", nil)
	want(t, st, 200)
	if decodeBody(t, st)["available"] != true {
		t.Fatal("setup should be available on an empty database")
	}
	want(t, c.do("POST", "/setup", map[string]string{"token": "wrong", "username": "admin", "password": adminPW}), 401)
	want(t, c.do("POST", "/setup", map[string]string{"token": e.p.setupToken, "username": "x", "password": adminPW}), 400)
	want(t, c.do("POST", "/setup", map[string]string{"token": e.p.setupToken, "username": "admin", "password": "short"}), 400)

	admin := e.setupAdmin()
	w := admin.do("GET", "/me", nil)
	want(t, w, 200)
	if got := decodeBody(t, w)["user"].(map[string]any)["username"]; got != "admin" {
		t.Fatalf("me.username = %v", got)
	}
	if decodeBody(t, c.do("GET", "/setup", nil))["available"] != false {
		t.Fatal("setup must report unavailable once a user exists")
	}
	// Setup is single-use.
	want(t, c.do("POST", "/setup", map[string]string{"token": "anything", "username": "evil", "password": adminPW}), 404)
}

func TestSetupTokenExpiresAndRotates(t *testing.T) {
	e := newEnv(t)
	old := e.p.setupToken
	e.now = e.now.Add(setupTokenTTL + time.Second)
	c := e.client("192.0.2.1")
	want(t, c.do("POST", "/setup", map[string]string{"token": old, "username": "admin", "password": adminPW}), 401)
	if e.p.setupToken == old || e.p.setupToken == "" {
		t.Fatal("expired token was not replaced")
	}
	want(t, c.do("POST", "/setup", map[string]string{"token": old, "username": "admin", "password": adminPW}), 401)
	want(t, c.do("POST", "/setup", map[string]string{"token": e.p.setupToken, "username": "admin", "password": adminPW}), 201)
}

func TestLoginLogout(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()

	c := e.client("192.0.2.2")
	bad := c.do("POST", "/login", map[string]string{"username": "admin", "password": "nope-nope-nope"})
	unknown := c.do("POST", "/login", map[string]string{"username": "ghost", "password": "nope-nope-nope"})
	want(t, bad, 401)
	want(t, unknown, 401)
	if bad.Body.String() != unknown.Body.String() {
		t.Fatalf("login errors differ, user enumeration possible: %q vs %q", bad.Body, unknown.Body)
	}

	want(t, c.login("ADMIN", adminPW), 200) // usernames are case-insensitive
	ck := c.cookie
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" {
		t.Fatalf("session cookie not hardened: %+v", ck)
	}
	want(t, c.do("GET", "/me", nil), 200)

	// Logout is a mutation: without the CSRF header it is refused and the session survives.
	csrf := c.csrf
	c.csrf = ""
	want(t, c.do("POST", "/logout", nil), 403)
	want(t, c.do("GET", "/me", nil), 200)
	c.csrf = csrf
}

func TestLogoutWithCSRFInvalidatesSession(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()
	c := e.client("192.0.2.2")
	want(t, c.login("admin", adminPW), 200)
	old := c.cookie
	want(t, c.do("POST", "/logout", nil), 204)
	c.cookie = old // replay the stolen cookie
	want(t, c.do("GET", "/me", nil), 401)
}

func TestLoginRejectsNonJSONAndCrossSite(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()
	r := httptest.NewRequest("POST", "http://cms.test/api/v1/auth/login", strings.NewReader("username=admin&password="+adminPW))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	want(t, w, 415)

	r = httptest.NewRequest("POST", "http://cms.test/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"`+adminPW+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	want(t, w, 403)
}

func TestCSRFRequiredOnMutations(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	csrf := admin.csrf
	admin.csrf = ""
	want(t, admin.do("POST", "/users", map[string]string{"username": "bob", "password": otherPW, "role": "editor"}), 403)
	admin.csrf = "wrong"
	want(t, admin.do("POST", "/users", map[string]string{"username": "bob", "password": otherPW, "role": "editor"}), 403)
	admin.csrf = csrf
	want(t, admin.do("POST", "/users", map[string]string{"username": "bob", "password": otherPW, "role": "editor"}), 201)
}

func TestRoleEnforcement(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	admin.createUser("alice", "author")

	anon := e.client("192.0.2.3")
	want(t, anon.do("GET", "/users", nil), 401)

	alice := e.client("192.0.2.4")
	want(t, alice.login("alice", otherPW), 200)
	want(t, alice.do("GET", "/users", nil), 403)
	want(t, alice.do("POST", "/users", map[string]string{"username": "eve", "password": otherPW, "role": "admin"}), 403)
	want(t, alice.do("PATCH", "/users/1", map[string]any{"role": "author"}), 403)
	want(t, alice.do("DELETE", "/users/1", nil), 403)
}

func TestCreateUserValidation(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	want(t, admin.do("POST", "/users", map[string]string{"username": "bad name", "password": otherPW, "role": "author"}), 400)
	want(t, admin.do("POST", "/users", map[string]string{"username": "bob", "password": otherPW, "role": "root"}), 400)
	want(t, admin.do("POST", "/users", map[string]string{"username": "bob", "password": "short", "role": "author"}), 400)
	want(t, admin.do("POST", "/users", map[string]string{"username": "bob", "password": otherPW, "role": "author"}), 201)
	want(t, admin.do("POST", "/users", map[string]string{"username": "BOB", "password": otherPW, "role": "author"}), 409)
	want(t, admin.do("POST", "/users", map[string]any{"username": "carol", "password": otherPW, "role": "author", "extra": 1}), 400)

	w := admin.do("GET", "/users", nil)
	want(t, w, 200)
	if strings.Contains(w.Body.String(), "argon2") || strings.Contains(w.Body.String(), "password") {
		t.Fatalf("user list leaks credentials: %s", w.Body)
	}
}

func TestLastAdminProtection(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	want(t, admin.do("PATCH", "/users/1", map[string]any{"role": "editor"}), 409)
	want(t, admin.do("PATCH", "/users/1", map[string]any{"disabled": true}), 409)
	want(t, admin.do("DELETE", "/users/1", nil), 409)

	// Changing only the password of the last admin is fine.
	// (It ends that user's sessions, so log in again.)
	want(t, admin.do("PATCH", "/users/1", map[string]any{"password": "brand new long password"}), 204)
	want(t, admin.login("admin", "brand new long password"), 200)

	// With a second active admin the first can step down; then the second is protected.
	id2 := admin.createUser("root2", "admin")
	want(t, admin.do("PATCH", "/users/1", map[string]any{"role": "editor"}), 204)
	admin2 := e.client("192.0.2.9")
	want(t, admin2.login("root2", otherPW), 200)
	want(t, admin2.do("DELETE", fmt.Sprintf("/users/%d", id2), nil), 409)
	want(t, admin2.do("PATCH", fmt.Sprintf("/users/%d", id2), map[string]any{"disabled": true}), 409)

	want(t, admin2.do("DELETE", "/users/999", nil), 404)
	want(t, admin2.do("PATCH", "/users/999", map[string]any{"role": "author"}), 404)
}

func TestDisabledAdminDoesNotCountAsAnotherAdmin(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	id2 := admin.createUser("root2", "admin")
	want(t, admin.do("PATCH", fmt.Sprintf("/users/%d", id2), map[string]any{"disabled": true}), 204)
	// root2 is disabled, so "admin" is effectively the last one.
	want(t, admin.do("DELETE", "/users/1", nil), 409)
}

func TestDisableRevokesSessionsAndBlocksLogin(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	id := admin.createUser("bob", "editor")
	bob := e.client("192.0.2.5")
	want(t, bob.login("bob", otherPW), 200)
	want(t, bob.do("GET", "/me", nil), 200)

	want(t, admin.do("PATCH", fmt.Sprintf("/users/%d", id), map[string]any{"disabled": true}), 204)
	want(t, bob.do("GET", "/me", nil), 401)
	want(t, e.client("192.0.2.6").login("bob", otherPW), 401)
}

func TestAdminPasswordResetRevokesSessions(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	id := admin.createUser("bob", "editor")
	bob := e.client("192.0.2.5")
	want(t, bob.login("bob", otherPW), 200)
	want(t, admin.do("PATCH", fmt.Sprintf("/users/%d", id), map[string]any{"password": "reset by the admin pw"}), 204)
	want(t, bob.do("GET", "/me", nil), 401)
	want(t, e.client("192.0.2.6").login("bob", "reset by the admin pw"), 200)
}

func TestDeleteUserRemovesSessions(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	id := admin.createUser("bob", "editor")
	bob := e.client("192.0.2.5")
	want(t, bob.login("bob", otherPW), 200)
	want(t, admin.do("DELETE", fmt.Sprintf("/users/%d", id), nil), 204)
	want(t, bob.do("GET", "/me", nil), 401)
}

func TestPasswordChange(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	admin.createUser("bob", "editor")

	laptop, phone := e.client("192.0.2.7"), e.client("192.0.2.8")
	want(t, laptop.login("bob", otherPW), 200)
	want(t, phone.login("bob", otherPW), 200)

	want(t, laptop.do("POST", "/password", map[string]string{"current": "wrong wrong wrong", "new": "a fresh long password"}), 403)
	want(t, laptop.do("POST", "/password", map[string]string{"current": otherPW, "new": "short"}), 400)
	want(t, laptop.do("POST", "/password", map[string]string{"current": otherPW, "new": "a fresh long password"}), 204)

	want(t, laptop.do("GET", "/me", nil), 200) // current session survives
	want(t, phone.do("GET", "/me", nil), 401)  // other sessions are revoked
	want(t, e.client("192.0.2.10").login("bob", otherPW), 401)
	want(t, e.client("192.0.2.10").login("bob", "a fresh long password"), 200)
}

func TestSessionExpiry(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	want(t, admin.do("GET", "/me", nil), 200)
	e.now = e.now.Add(sessionIdle + time.Minute)
	want(t, admin.do("GET", "/me", nil), 401)

	c := e.client("192.0.2.2")
	want(t, c.login("admin", adminPW), 200)
	for range 20 { // stay active, but hit the absolute lifetime
		e.now = e.now.Add(sessionIdle - time.Hour)
		if c.do("GET", "/me", nil).Code == 401 {
			return
		}
	}
	t.Fatal("session outlived its absolute lifetime")
}

func TestIPLockout(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()
	c := e.client("198.51.100.1")
	var last *httptest.ResponseRecorder
	for i := 0; i < 12; i++ { // 10 free failures, the 11th triggers the lockout, the 12th is refused
		last = c.do("POST", "/login", map[string]string{"username": fmt.Sprintf("user%d", i), "password": "wrong-wrong-wrong"})
	}
	want(t, last, 429)
	if last.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	// Even the correct password is refused while locked out, and success does not reset the IP counter.
	want(t, c.login("admin", adminPW), 429)

	// Another network is unaffected.
	want(t, e.client("198.51.100.2").login("admin", adminPW), 200)

	// Lockout ends with time.
	e.now = e.now.Add(16 * time.Minute)
	want(t, c.login("admin", adminPW), 200)
}

func TestIPv6RotationWithinSlash64IsLimited(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()
	var last *httptest.ResponseRecorder
	for i := 0; i < 12; i++ {
		c := e.client(fmt.Sprintf("2001:db8:1:2::%x", i+1)) // 12 addresses, same /64
		last = c.do("POST", "/login", map[string]string{"username": fmt.Sprintf("u%d", i), "password": "wrong-wrong-wrong"})
	}
	want(t, last, 429)
	want(t, e.client("2001:db8:1:3::1").login("admin", adminPW), 200) // different /64
}

func TestUsernameThrottleAcrossManyIPs(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()
	// 20 free failures from 20 different networks, then the account slows down for everyone.
	for i := 0; i < 20; i++ {
		c := e.client(fmt.Sprintf("203.0.113.%d", i+1))
		want(t, c.do("POST", "/login", map[string]string{"username": "admin", "password": "wrong-wrong-wrong"}), 401)
	}
	c := e.client("203.0.113.200")
	want(t, c.do("POST", "/login", map[string]string{"username": "admin", "password": "wrong-wrong-wrong"}), 401) // 21st failure sets 1s lock
	want(t, c.login("admin", adminPW), 429)
	e.now = e.now.Add(3 * time.Second)
	want(t, c.login("admin", adminPW), 200)
}

func TestLimitKey(t *testing.T) {
	for in, out := range map[string]string{
		"192.0.2.1":         "192.0.2.1",
		"::ffff:192.0.2.1":  "192.0.2.1",
		"2001:db8:1:2::1":   "2001:db8:1:2::/64",
		"2001:db8:1:2:f::9": "2001:db8:1:2::/64",
		"garbage":           "garbage",
	} {
		if got := limitKey(in); got != out {
			t.Errorf("limitKey(%q) = %q, want %q", in, got, out)
		}
	}
}

func TestLimiterMemoryIsHardBounded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := newLimiter(0, time.Minute, time.Hour, time.Hour)
	l.now = func() time.Time { return now }
	l.max = 100
	for i := 0; i < 10_000; i++ {
		now = now.Add(time.Millisecond)
		l.fail(fmt.Sprintf("k%d", i)) // every key is locked out immediately (free=0)
		if len(l.entries) > l.max {
			t.Fatalf("table grew to %d > max %d", len(l.entries), l.max)
		}
	}
	if l.check("k9999") == 0 {
		t.Fatal("newest lockout was dropped")
	}
}

func TestPasswordHashing(t *testing.T) {
	h, err := hashPassword(context.Background(), "some long password")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := verifyPassword(context.Background(), "some long password", h); !ok || err != nil {
		t.Fatalf("verify good: %v %v", ok, err)
	}
	if ok, _ := verifyPassword(context.Background(), "other", h); ok {
		t.Fatal("wrong password verified")
	}
	// A tampered hash demanding absurd memory must be refused, not executed.
	bomb := strings.Replace(h, "m=19456", "m=4000000", 1)
	if _, err := verifyPassword(context.Background(), "x", bomb); err == nil {
		t.Fatal("memory-bomb parameters accepted")
	}
}

func TestHashQueueIsBoundedAndCancellable(t *testing.T) {
	// Occupy every slot.
	var releases []func()
	for i := 0; i < hashSlots; i++ {
		r, err := acquireHash(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	// Fill the waiting room with cancellable waiters.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, maxHashWaiting)
	for i := 0; i < maxHashWaiting; i++ {
		go func() { _, err := acquireHash(ctx); done <- err }()
	}
	for hashWaiting.Load() != maxHashWaiting {
		time.Sleep(time.Millisecond)
	}
	// One more is refused at once instead of queueing.
	if _, err := acquireHash(context.Background()); !errors.Is(err, errBusy) {
		t.Fatalf("overflow waiter: got %v, want errBusy", err)
	}
	// Cancelling releases all waiters without a slot being granted.
	cancel()
	for i := 0; i < maxHashWaiting; i++ {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter returned %v", err)
		}
	}
	for _, r := range releases {
		r()
	}
	if r, err := acquireHash(context.Background()); err != nil {
		t.Fatalf("slots not released: %v", err)
	} else {
		r()
	}
}

func TestLoginAnswers503WhenHashingIsSaturated(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()
	var releases []func()
	for i := 0; i < hashSlots; i++ {
		r, _ := acquireHash(context.Background())
		releases = append(releases, r)
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()
	stop := make(chan struct{})
	for i := 0; i < maxHashWaiting; i++ {
		go func() {
			r, err := acquireHash(context.Background())
			if err == nil {
				<-stop
				r()
			}
		}()
	}
	defer close(stop)
	for hashWaiting.Load() != maxHashWaiting {
		time.Sleep(time.Millisecond)
	}
	c := e.client("192.0.2.77")
	w := c.do("POST", "/login", map[string]string{"username": "admin", "password": adminPW})
	want(t, w, 503)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("503 without Retry-After")
	}
	// An overloaded server must not count that as a failed login attempt.
	if d := e.p.ipLimit.check(ipKey("192.0.2.77")); d != 0 {
		t.Fatalf("503 counted as a failure: lockout %v", d)
	}
}

func countSessions(t *testing.T, e *env) int {
	t.Helper()
	var n int
	if err := e.p.db.QueryRow(`SELECT COUNT(*) FROM auth_sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSessionsPerUserAreCapped(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin() // session #1
	var clients []*client
	for i := 0; i < maxSessionsPerUser+4; i++ {
		c := e.client(fmt.Sprintf("198.51.100.%d", 10+i))
		want(t, c.login("admin", adminPW), 200)
		clients = append(clients, c)
		e.now = e.now.Add(time.Second) // distinct creation times
	}
	if n := countSessions(t, e); n != maxSessionsPerUser {
		t.Fatalf("sessions = %d, want %d", n, maxSessionsPerUser)
	}
	want(t, clients[0].do("GET", "/me", nil), 401) // oldest evicted
	want(t, clients[len(clients)-1].do("GET", "/me", nil), 200)
}

func TestExpiredSessionsAreCleanedUp(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()
	c := e.client("198.51.100.1")
	want(t, c.login("admin", adminPW), 200)
	if countSessions(t, e) != 2 {
		t.Fatal("setup precondition")
	}
	if n, err := e.p.cleanupSessions(context.Background()); err != nil || n != 0 {
		t.Fatalf("live sessions must survive cleanup: n=%d err=%v", n, err)
	}
	e.now = e.now.Add(sessionIdle + time.Minute)
	n, err := e.p.cleanupSessions(context.Background())
	if err != nil || n != 2 || countSessions(t, e) != 0 {
		t.Fatalf("idle sessions not removed: n=%d err=%v left=%d", n, err, countSessions(t, e))
	}
}
