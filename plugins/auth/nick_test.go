// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// A public nickname must never reveal an account name. Each case below is a way
// to write a login that a plain string comparison would miss.
func TestNickCollidesWithLogins(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin() // login "admin"
	admin.createUser("alice", "author")
	admin.createUser("bob", "author")
	ctx := context.Background()

	taken := []string{
		"admin", "ADMIN", "Admin", // case
		"a.d.m.i.n", "a_d-m i n", "a d m i n", // separators
		"adm1n", "admln", "4dmin", // digits and letters that look alike
		"adrnin",                // r+n looks like m
		"Ivan admin", "admin K", // the login as a word
		"xx-admin-yy", "i.admin", // ... with separators
		"superadmin", "Adminova", // a long login inside a longer nick (known false positive)
		"alice", "A1ice", "Alice B", // an ordinary author login
		"bob", "b0b", "Bob K.", "b o b",
	}
	free := []string{
		"Ivan", "Marina K.", "writer-7", "Joanne",
		"adm", "dmin", // pieces are not logins
		"Bobby", "Bobcat", // a short login (3) is matched as a whole or as a word only
		"alic", "mal",
	}
	for _, n := range taken {
		if got, err := e.p.NameTaken(ctx, n); err != nil || !got {
			t.Errorf("NameTaken(%q) = %v, %v; want true", n, got, err)
		}
	}
	for _, n := range free {
		if got, err := e.p.NameTaken(ctx, n); err != nil || got {
			t.Errorf("NameTaken(%q) = %v, %v; want false", n, got, err)
		}
	}
}

// A user made through the API is visible at once; one made by another process
// (the host helper writes through a one-shot container) within the cache lifetime.
func TestNickCheckSeesNewAndDeletedUsers(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	ctx := context.Background()

	if taken, _ := e.p.NameTaken(ctx, "carol"); taken {
		t.Fatal("carol is free before she exists")
	}
	id := admin.createUser("carol", "author")
	if taken, _ := e.p.NameTaken(ctx, "carol"); !taken {
		t.Error("a user created through the API must be seen immediately")
	}
	want(t, admin.do("DELETE", "/users/"+strconv.FormatInt(id, 10), nil), 204)
	if taken, _ := e.p.NameTaken(ctx, "carol"); taken {
		t.Error("a deleted login is free again immediately")
	}

	// Written behind the plugin's back.
	if _, err := e.p.db.Exec(`INSERT INTO auth_users(username, password_hash, role, created_at) VALUES ('dave','x','author',1)`); err != nil {
		t.Fatal(err)
	}
	if taken, _ := e.p.NameTaken(ctx, "dave"); taken {
		t.Log("seen before the cache expired (fine)")
	}
	e.now = e.now.Add(accountsTTL + time.Second)
	if taken, _ := e.p.NameTaken(ctx, "dave"); !taken {
		t.Error("a user written by another process must be seen after the cache lifetime")
	}
}

func TestSkeleton(t *testing.T) {
	same := [][]string{
		{"admin", "ADMIN", "a.d.m.i.n", "adm1n", "adrnin", "a d-m_i n"},
		{"vvilliam", "william"},
	}
	for _, group := range same {
		for _, s := range group[1:] {
			if skeleton(group[0]) != skeleton(s) {
				t.Errorf("skeleton(%q) = %q, skeleton(%q) = %q; want equal", group[0], skeleton(group[0]), s, skeleton(s))
			}
		}
	}
	if skeleton("admin") == skeleton("admi") {
		t.Error("different names must stay different")
	}
}
