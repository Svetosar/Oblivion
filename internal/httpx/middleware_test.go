// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func headersOf(secure bool) http.Header {
	h := SecurityHeaders(secure)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	return w.Header()
}

// HSTS is a promise about the whole host for years and cannot be taken back.
// includeSubDomains would extend it to every sibling service of the operator
// (a mail or staging host that is still plain HTTP), so the application must
// not make that decision on its own.
func TestHSTSDoesNotCoverSubdomains(t *testing.T) {
	v := headersOf(true).Get("Strict-Transport-Security")
	if !strings.HasPrefix(v, "max-age=") {
		t.Fatalf("HSTS = %q, want a max-age", v)
	}
	for _, bad := range []string{"includeSubDomains", "preload"} {
		if strings.Contains(strings.ToLower(v), strings.ToLower(bad)) {
			t.Errorf("HSTS %q must not contain %s", v, bad)
		}
	}
}

// Without TLS in front (local development) the header must not be sent at all:
// a browser would then insist on HTTPS for localhost.
func TestNoHSTSWithoutSecure(t *testing.T) {
	if v := headersOf(false).Get("Strict-Transport-Security"); v != "" {
		t.Fatalf("HSTS = %q without Secure", v)
	}
}

func TestBaselineSecurityHeaders(t *testing.T) {
	h := headersOf(true)
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	csp := h.Get("Content-Security-Policy")
	for _, must := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, must) {
			t.Errorf("CSP %q lacks %q", csp, must)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP %q must not allow inline or eval", csp)
	}
}
