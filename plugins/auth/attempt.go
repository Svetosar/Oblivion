// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"net/http"
	"sync"
)

// perNetHashes is how many password checks one client network may have in
// flight. It is below hashSlots on purpose: a single client must not be able to
// occupy every slot and starve everybody else.
const perNetHashes = 1

// flights counts in-flight password checks per limiter key. Entries exist only
// while a check runs, so the map is bounded by the number of live requests.
type flights struct {
	mu sync.Mutex
	n  map[string]int
}

func (f *flights) enter(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == nil {
		f.n = map[string]int{}
	}
	if f.n[key] >= perNetHashes {
		return false
	}
	f.n[key]++
	return true
}

func (f *flights) leave(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n[key]--; f.n[key] <= 0 {
		delete(f.n, key)
	}
}

// attempt is one credential check guarded by both limiters. begin reserves the
// try before any hashing; finish settles it. The default outcome is "refund"
// (early return, 503, client gone) so only real failures are counted.
type attempt struct {
	p      *Plugin
	ik, uk string
	state  int // 0 aborted, 1 failed, 2 succeeded
}

// begin answers 429 and returns nil when the attempt must not run.
func (p *Plugin) begin(w http.ResponseWriter, ik, uk string) *attempt {
	if d := p.ipLimit.reserve(ik); d > 0 {
		tooMany(w, d)
		return nil
	}
	if d := p.userLimit.reserve(uk); d > 0 {
		p.ipLimit.refund(ik)
		tooMany(w, d)
		return nil
	}
	if !p.inflight.enter(ik) {
		p.ipLimit.refund(ik)
		p.userLimit.refund(uk)
		w.Header().Set("Retry-After", "1")
		writeErr(w, http.StatusTooManyRequests, "another sign-in from this network is still being checked")
		return nil
	}
	return &attempt{p: p, ik: ik, uk: uk}
}

func (a *attempt) failed()    { a.state = 1 }
func (a *attempt) succeeded() { a.state = 2 }

func (a *attempt) finish() {
	a.p.inflight.leave(a.ik)
	switch a.state {
	case 0:
		a.p.ipLimit.refund(a.ik)
		a.p.userLimit.refund(a.uk)
	case 2:
		a.p.ipLimit.refund(a.ik) // success is not a failure, but it does not forgive earlier ones
		a.p.userLimit.reset(a.uk)
	}
}
