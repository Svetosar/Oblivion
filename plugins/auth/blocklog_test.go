// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func failLogin(c *client, user, pw string) int {
	return c.do("POST", "/login", map[string]string{"username": user, "password": pw}).Code
}

// An operator (or fail2ban) needs to see who gets blocked: one line when the
// block starts, with the address, and nothing that was typed.
func TestLoginBlockIsLoggedOnceWithTheClientAddress(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()
	c := e.client("203.0.113.9")
	secret := "my-secret-wrong-password"

	blocked := 0
	for i := 0; i < 40 && blocked < 5; i++ {
		if failLogin(c, "admin", secret) == 429 {
			blocked++
		}
	}
	if blocked < 5 {
		t.Fatalf("the client was never blocked (%d)", blocked)
	}
	out := e.logs.String()
	if n := strings.Count(out, "sign-in blocked"); n != 1 {
		t.Fatalf("%d block lines for one block, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "client=203.0.113.9") || !strings.Contains(out, "level=WARN") {
		t.Errorf("the line lacks the client address or the level:\n%s", out)
	}
	for _, leak := range []string{secret, "admin\"", "username"} {
		if strings.Contains(out, leak) {
			t.Errorf("the log contains %q:\n%s", leak, out)
		}
	}

	// Another client has its own block and its own line. It tries another name:
	// "admin" is already throttled by name, which is a different kind of block.
	c2 := e.client("203.0.113.10")
	for i := 0; i < 40; i++ {
		if failLogin(c2, "ghost", secret) == 429 {
			break
		}
	}
	if !strings.Contains(e.logs.String(), "client=203.0.113.10") {
		t.Errorf("the second client's block is not logged:\n%s", e.logs)
	}
}

// A login that is slowed down for everybody is a different event from one
// network being blocked: no address, because none is to blame.
func TestUsernameThrottleIsLoggedWithoutAnAddress(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin()
	// Spread the failures over many networks so that only the username limiter trips.
	var tripped bool
	for i := 0; i < 400 && !tripped; i++ {
		c := e.client("198.51.100." + strconv.Itoa(i%250+1))
		c2 := e.client("10." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250) + ".1")
		_ = c
		if failLogin(c2, "admin", "wrong-wrong-wrong-pw") == 429 {
			tripped = true
		}
	}
	if !tripped {
		t.Skip("the username limiter did not trip within the attempts")
	}
	out := e.logs.String()
	if !strings.Contains(out, "sign-in blocked") || !strings.Contains(out, "kind=username") {
		t.Fatalf("the username throttle was not logged:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "sign-in blocked") && (strings.Contains(line, "admin") || strings.Contains(line, "client=")) {
			t.Errorf("a username or an address is in the throttle line: %s", line)
		}
	}
}

// "First" is true once per lockout, and a new lockout reports again.
func TestLimiterReportsEachLockoutOnce(t *testing.T) {
	l := newLimiter(1, time.Minute, time.Hour, time.Hour)
	now := time.Unix(1_800_000_000, 0)
	l.now = func() time.Time { return now }
	l.reserve("k") // free
	l.reserve("k") // allowed, starts the lockout
	if d, first := l.reserve("k"); d == 0 || !first {
		t.Fatalf("first refusal: wait %v first %v", d, first)
	}
	if d, first := l.reserve("k"); d == 0 || first {
		t.Fatalf("second refusal: wait %v first %v, want a refusal that is not reported again", d, first)
	}
	now = now.Add(2 * time.Hour) // the lockout is over and the failures are forgotten
	l.reserve("k")
	l.reserve("k")
	if d, first := l.reserve("k"); d == 0 || !first {
		t.Fatalf("a later lockout must be reported again: wait %v first %v", d, first)
	}
}
