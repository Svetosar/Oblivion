// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"strings"
	"testing"
)

// A password used alone needs at least 15 characters (NIST SP 800-63B rev. 4)
// and may contain only printable ASCII: Latin letters, digits, symbols, space.
// Cyrillic and other non-ASCII text is refused when a password is set, because
// the same text typed on another keyboard layout, or composed differently in
// Unicode, would not match the stored hash.
func TestValidatePassword(t *testing.T) {
	cases := []struct {
		name string
		pw   string
		ok   bool
	}{
		{"14 ASCII", strings.Repeat("a", 14), false},
		{"15 ASCII", strings.Repeat("a", 15), true},
		{"12 ASCII (the old minimum)", strings.Repeat("a", 12), false},
		{"passphrase with spaces", "correct horse battery", true},
		{"symbols and digits", "Tr0ub4dor&3-xkcd!", true},
		{"all printable punctuation", "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~ 09azAZ", true},
		{"256 bytes", strings.Repeat("a", 256), true},
		{"257 bytes", strings.Repeat("a", 257), false},

		{"Cyrillic only, long enough", strings.Repeat("ж", 20), false},
		{"Cyrillic mixed into ASCII", "correct-horse-ж-battery", false},
		{"Cyrillic look-alike of a Latin letter", "correct-horse-bаttery", false}, // Cyrillic а
		{"accented Latin", "correct-horse-bétery", false},
		{"emoji", "correct-horse-🐴-battery", false},
		{"no-break space", "correct horse-battery", false},
		{"tab", "correct\thorse-battery", false},
		{"newline", "correct\nhorse-battery", false},
		{"NUL", "correct\x00horse-battery", false},
		{"DEL", "correct\x7fhorse-battery", false},
		{"invalid UTF-8 byte", "correct-horse-\xffbattery", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		err := validatePassword(c.pw)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
		}
	}
}

// The message must tell the user what to do, in both failure cases.
func TestValidatePasswordMessages(t *testing.T) {
	if err := validatePassword("short"); err == nil || !strings.Contains(err.Error(), "15") {
		t.Errorf("short password: %v", err)
	}
	if err := validatePassword(strings.Repeat("ж", 20)); err == nil || !strings.Contains(strings.ToLower(err.Error()), "latin") {
		t.Errorf("Cyrillic password: %v", err)
	}
}

// The rule applies when a password is set, never when one is checked: users who
// chose a shorter or non-ASCII password before keep logging in.
func TestOldPasswordsStillVerify(t *testing.T) {
	for _, pw := range []string{"twelve-chars", "пароль-на-русском"} {
		hash, err := hashPassword(t.Context(), pw)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := verifyPassword(t.Context(), pw, hash); err != nil || !ok {
			t.Fatalf("%q: verify = %v, %v", pw, ok, err)
		}
	}
}
