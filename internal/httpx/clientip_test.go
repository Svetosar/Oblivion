// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	f := ClientIPFunc(trusted)
	cases := []struct{ name, remote, xff, want string }{
		{"no proxy trusted peer ignored header", "203.0.113.5:1234", "1.2.3.4", "203.0.113.5"},
		{"trusted proxy uses xff", "10.0.0.2:1", "198.51.100.7", "198.51.100.7"},
		{"spoofed left entry ignored", "10.0.0.2:1", "6.6.6.6, 198.51.100.7", "198.51.100.7"},
		{"chain of trusted proxies", "10.0.0.2:1", "198.51.100.7, 10.0.0.9", "198.51.100.7"},
		{"malformed entry falls back to peer", "10.0.0.2:1", "garbage", "10.0.0.2"},
		{"no header", "10.0.0.2:1", "", "10.0.0.2"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := f(r); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
	// With no trusted proxies configured the header is never honored.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:1"
	r.Header.Set("X-Forwarded-For", "1.1.1.1")
	if got := ClientIPFunc(nil)(r); got != "10.0.0.2" {
		t.Errorf("untrusted: %s", got)
	}
}
