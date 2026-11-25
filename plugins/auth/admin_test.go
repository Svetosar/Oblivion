// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"oblivion/internal/db"
	"oblivion/internal/plugins"
)

// Account management outside the HTTP server: the "user" subcommands of the
// binary, run by the operator (or by the host helper in a one-shot container).

func newAdmin(t *testing.T) *Admin {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := plugins.MigratePlugin(context.Background(), d, New()); err != nil {
		t.Fatal(err)
	}
	return NewAdmin(d)
}

func (a *Admin) mustCreate(t *testing.T, login, pw, role string) {
	t.Helper()
	if err := a.Create(context.Background(), login, pw, role); err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
}

func (a *Admin) verifies(t *testing.T, login, pw string) bool {
	t.Helper()
	var hash string
	if err := a.db.QueryRow(`SELECT password_hash FROM auth_users WHERE username = ?`, login).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	ok, err := verifyPassword(context.Background(), pw, hash)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// On a fresh volume nothing has created the schema yet: that is exactly when the
// first administrator is made.
func TestSchemaMigrationIsRepeatable(t *testing.T) {
	a := newAdmin(t)
	a.mustCreate(t, "root", "correct horse battery", "admin")
	if err := plugins.MigratePlugin(context.Background(), a.db, New()); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	users, err := a.List(context.Background())
	if err != nil || len(users) != 1 || users[0].Username != "root" || users[0].Role != "admin" || users[0].Disabled {
		t.Fatalf("users = %+v, err %v", users, err)
	}
}

func TestAdminCreateValidates(t *testing.T) {
	a := newAdmin(t)
	ctx := context.Background()
	a.mustCreate(t, "alice", "correct horse battery", "author")
	for name, c := range map[string]struct{ login, pw, role string }{
		"short password":     {"bob", "short", "author"},
		"Cyrillic password":  {"bob", "пароль-на-русском-языке", "author"},
		"Cyrillic login":     {"боб", "correct horse battery", "author"},
		"short login":        {"ab", "correct horse battery", "author"},
		"unknown role":       {"bob", "correct horse battery", "owner"},
		"empty role":         {"bob", "correct horse battery", ""},
		"login with a space": {"bob smith", "correct horse battery", "author"},
	} {
		if err := a.Create(ctx, c.login, c.pw, c.role); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := a.Create(ctx, "ALICE", "correct horse battery", "author"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("duplicate (other case): %v, want ErrUsernameTaken", err)
	}
	if users, _ := a.List(ctx); len(users) != 1 {
		t.Errorf("%d users after refused creations, want 1", len(users))
	}
}

func TestAdminResetPassword(t *testing.T) {
	a := newAdmin(t)
	ctx := context.Background()
	a.mustCreate(t, "root", "correct horse battery", "admin")
	a.mustCreate(t, "alice", "another long password", "author")
	var uid int64
	if err := a.db.QueryRow(`SELECT id FROM auth_users WHERE username='alice'`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := a.db.Exec(`INSERT INTO auth_sessions(token_hash, user_id, csrf, created_at, last_seen, expires_at) VALUES (x'01', ?, 'c', ?, ?, ?)`, uid, now, now, now+3600); err != nil {
		t.Fatal(err)
	}

	if err := a.ResetPassword(ctx, "ALICE", "weak"); err == nil {
		t.Error("a weak password was accepted")
	}
	if !a.verifies(t, "alice", "another long password") {
		t.Error("a refused reset changed the password")
	}
	if err := a.ResetPassword(ctx, "ALICE", "brand new password!"); err != nil { // the login is case-insensitive
		t.Fatal(err)
	}
	if a.verifies(t, "alice", "another long password") || !a.verifies(t, "alice", "brand new password!") {
		t.Error("the password did not change")
	}
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM auth_sessions WHERE user_id = ?`, uid).Scan(&n)
	if n != 0 {
		t.Errorf("%d sessions survived a password reset", n)
	}
	if !a.verifies(t, "root", "correct horse battery") {
		t.Error("another account was touched")
	}
	if err := a.ResetPassword(ctx, "nobody", "brand new password!"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("unknown user: %v, want ErrNoSuchUser", err)
	}
}

func TestAdminDisableEnableKeepsOneAdministrator(t *testing.T) {
	a := newAdmin(t)
	ctx := context.Background()
	a.mustCreate(t, "root", "correct horse battery", "admin")
	if err := a.SetDisabled(ctx, "root", true); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disabling the last administrator: %v, want ErrLastAdmin", err)
	}
	a.mustCreate(t, "second", "another long password", "admin")
	if err := a.SetDisabled(ctx, "root", true); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDisabled(ctx, "second", true); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("now the second one is the last: %v", err)
	}
	if err := a.SetDisabled(ctx, "root", false); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDisabled(ctx, "ghost", true); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("unknown user: %v", err)
	}
	users, _ := a.List(ctx)
	for _, u := range users {
		if u.Disabled {
			t.Errorf("%s is still disabled", u.Username)
		}
	}
}

// What the operator does must be what the running server sees.
func TestAdminChangesAreVisibleToTheRunningServer(t *testing.T) {
	e := newEnv(t)
	adm := &Admin{db: e.p.db}
	ctx := context.Background()
	if err := adm.Create(ctx, "carol", "correct horse battery", "editor"); err != nil {
		t.Fatal(err)
	}
	c := e.client("192.0.2.7")
	w := c.do("POST", "/login", map[string]string{"username": "carol", "password": "correct horse battery"})
	want(t, w, 200)
	c.csrf = decodeBody(t, w)["csrf"].(string)
	want(t, c.do("GET", "/me", nil), 200)

	if err := adm.ResetPassword(ctx, "carol", "a different long password"); err != nil {
		t.Fatal(err)
	}
	want(t, c.do("GET", "/me", nil), 401) // the old session is gone
	want(t, e.client("192.0.2.7").do("POST", "/login", map[string]string{"username": "carol", "password": "correct horse battery"}), 401)
	want(t, e.client("192.0.2.7").do("POST", "/login", map[string]string{"username": "carol", "password": "a different long password"}), 200)

	if err := adm.SetDisabled(ctx, "carol", true); err != nil {
		t.Fatal(err)
	}
	want(t, e.client("192.0.2.7").do("POST", "/login", map[string]string{"username": "carol", "password": "a different long password"}), 401)
}
