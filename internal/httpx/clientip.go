// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPFunc returns a function resolving the client address of a request.
// X-Forwarded-For is consulted only if the direct peer is in trusted; the chain
// is walked from the right and the first address that is not itself a trusted
// proxy wins (entries to its left are attacker-controllable). On any doubt the
// direct peer address is returned.
func ClientIPFunc(trusted []netip.Prefix) func(*http.Request) string {
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a.Unmap()) {
				return true
			}
		}
		return false
	}
	return func(r *http.Request) string {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		peer, err := netip.ParseAddr(host)
		if err != nil || len(trusted) == 0 || !isTrusted(peer) {
			return host
		}
		parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				return host // malformed entry: refuse to guess
			}
			if !isTrusted(a) {
				return a.Unmap().String()
			}
		}
		return host
	}
}
