// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"net/netip"
	"sort"
	"sync"
	"time"
)

// limiter is an in-memory brute-force limiter with exponential lockout.
// Keys are chosen by the caller (client network, username). The table size is
// hard-bounded. State is lost on restart, which is acceptable for a
// single-process deployment. window must be >= cap so a lockout never outlives
// the failure record that caused it.
type limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	max     int           // tracked keys cap (memory bound)
	free    int           // failures allowed before lockout starts
	base    time.Duration // first lockout
	cap     time.Duration // maximum lockout
	window  time.Duration // failures older than this are forgotten
	entries map[string]*entry
}

type entry struct {
	fails     int
	last      time.Time
	until     time.Time
	announced bool // the current lockout has been reported once
}

func newLimiter(free int, base, cap, window time.Duration) *limiter {
	return &limiter{now: time.Now, max: 10000, free: free, base: base, cap: cap, window: window, entries: map[string]*entry{}}
}

// check returns 0 if the key may proceed, else how long it must wait.
func (l *limiter) check(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return 0
	}
	if d := e.until.Sub(l.now()); d > 0 {
		return d
	}
	return 0
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failLocked(key, l.now())
}

func (l *limiter) failLocked(key string, now time.Time) {
	e := l.entries[key]
	if e == nil || now.Sub(e.last) > l.window {
		if len(l.entries) >= l.max {
			l.evict(now)
		}
		e = &entry{}
		l.entries[key] = e
	}
	e.fails++
	e.last = now
	if e.fails > l.free {
		d := l.base << min(e.fails-l.free-1, 20)
		if d <= 0 || d > l.cap {
			d = l.cap
		}
		e.until = now.Add(d)
		if d := e.until.Sub(now); d > 0 && e.fails == l.free+1 {
			e.announced = false // a new lockout begins
		}
	}
}

// reserve atomically checks the key and, if it may proceed, counts one failure
// in advance. Concurrent requests therefore cannot all slip past check before
// any of them has been recorded as failed. Returns 0 when reserved, else the
// wait. A reservation that turns out to be a success is returned with refund.
//
// first is true for the first refusal of a lockout, so that it can be reported
// once instead of once per blocked request.
func (l *limiter) reserve(key string) (wait time.Duration, first bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if e := l.entries[key]; e != nil {
		if d := e.until.Sub(now); d > 0 {
			first = !e.announced
			e.announced = true
			return d, first
		}
	}
	l.failLocked(key, now)
	return 0, false
}

// refund takes back one reservation (the attempt did not fail). It does not
// clear earlier failures, so a success still cannot be used to reset the counter.
func (l *limiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return
	}
	if e.fails--; e.fails <= 0 {
		delete(l.entries, key)
		return
	}
	if e.fails <= l.free {
		e.until = time.Time{} // the lockout was caused by this very reservation
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// evict keeps the table strictly below max. Stale entries go first; if that is
// not enough, the oldest-touched 10% are dropped even when locked out. Dropping
// a lockout lets an attacker holding >max distinct source networks retry sooner,
// but unbounded memory growth would be a worse failure (and the per-username
// limiter still applies). Called with l.mu held.
func (l *limiter) evict(now time.Time) {
	for k, e := range l.entries {
		if now.Sub(e.last) > l.window && !e.until.After(now) {
			delete(l.entries, k)
		}
	}
	if len(l.entries) < l.max {
		return
	}
	type kl struct {
		k string
		t time.Time
	}
	all := make([]kl, 0, len(l.entries))
	for k, e := range l.entries {
		all = append(all, kl{k, e.last})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for _, x := range all[:max(len(all)/10, 1)] {
		delete(l.entries, x.k)
	}
}

// limitKey normalizes a client address into a limiter key. IPv6 clients are
// grouped by /64, because a single subscriber typically controls a whole /64
// and could otherwise rotate through 2^64 addresses to dodge per-IP limits.
func limitKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is4() {
		return a.String()
	}
	pf, err := a.Prefix(64)
	if err != nil {
		return ip
	}
	return pf.String()
}
