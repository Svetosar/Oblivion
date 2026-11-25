// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"strings"
	"sync"
	"time"
)

// A public nickname must never reveal an account name: with the login known, an
// attacker only has to guess a password. NameTaken answers, for other plugins,
// whether a nickname equals or contains a login, ignoring the ways of writing it
// that a plain comparison misses.

// accountsTTL is how long the list of logins is reused. Users made through this
// plugin's API are seen at once (the list is dropped); users written by another
// process, such as the host helper that runs in a one-shot container, within this
// time.
const accountsTTL = 30 * time.Second

type accountSet struct {
	mu     sync.Mutex
	loaded time.Time
	names  []string // skeletons of all logins
}

func (a *accountSet) invalidate() {
	a.mu.Lock()
	a.names = nil
	a.mu.Unlock()
}

// skeletons returns the skeleton of every login, loading them when the list is
// missing or old.
func (p *Plugin) skeletons(ctx context.Context) ([]string, error) {
	a := &p.accounts
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.names != nil && p.now().Sub(a.loaded) < accountsTTL {
		return a.names, nil
	}
	rows, err := p.db.QueryContext(ctx, `SELECT username FROM auth_users LIMIT 10000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		names = append(names, skeleton(u))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.names, a.loaded = names, p.now()
	return names, nil
}

var (
	confusables = strings.NewReplacer("0", "o", "1", "l", "3", "e", "4", "a", "5", "s", "7", "t", "8", "b", "i", "l")
	ligatures   = strings.NewReplacer("rn", "m", "vv", "w")
)

func isSeparator(r rune) bool { return r == ' ' || r == '.' || r == '_' || r == '-' }

// skeleton reduces a name to what it looks like: lower case, no separators,
// look-alike digits and letters folded together (0/o, 1/i/l, 5/s, rn/m, vv/w).
// It is a heuristic, not a proof: it narrows what can be written, it does not
// make every disguise impossible.
func skeleton(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if !isSeparator(r) {
			b.WriteRune(r)
		}
	}
	return ligatures.Replace(confusables.Replace(b.String()))
}

// minContained is the shortest login that is also refused when it only appears
// inside a longer nickname ("superadmin"). Shorter logins ("bob") would block
// innocent nicknames ("Bobby") and are matched as a whole or as a word only.
const minContained = 5

// NameTaken reports whether nick is, or contains, the login of any user. An error
// means the answer is unknown: callers must treat that as "taken".
func (p *Plugin) NameTaken(ctx context.Context, nick string) (bool, error) {
	whole := skeleton(nick)
	if whole == "" {
		return false, nil
	}
	logins, err := p.skeletons(ctx)
	if err != nil {
		return true, err
	}
	words := strings.FieldsFunc(nick, isSeparator)
	for _, l := range logins {
		if l == "" {
			continue
		}
		if whole == l || (len(l) >= minContained && strings.Contains(whole, l)) {
			return true, nil
		}
		for _, w := range words {
			if skeleton(w) == l {
				return true, nil
			}
		}
	}
	return false, nil
}
