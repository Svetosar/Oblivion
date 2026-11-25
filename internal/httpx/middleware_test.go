// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

func logged(t *testing.T, h http.Handler, reqs ...*http.Request) string {
	t.Helper()
	for _, r := range reqs {
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	return ""
}

func newLog() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf
}

// The container healthcheck calls /healthz every 30 seconds; a line each time
// buries everything else. Failures stay visible.
func TestAccessLogSkipsSuccessfulHealthchecks(t *testing.T) {
	log, buf := newLog()
	status := 200
	h := AccessLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	logged(t, h, httptest.NewRequest("GET", "/healthz", nil))
	if buf.Len() != 0 {
		t.Fatalf("a successful healthcheck was logged: %s", buf)
	}
	logged(t, h, httptest.NewRequest("GET", "/posts/x", nil))
	if !strings.Contains(buf.String(), "path=/posts/x") {
		t.Fatalf("an ordinary request was not logged: %s", buf)
	}
	buf.Reset()
	status = 503
	logged(t, h, httptest.NewRequest("GET", "/healthz", nil))
	if !strings.Contains(buf.String(), "status=503") {
		t.Fatalf("a failing healthcheck must be logged: %s", buf)
	}
}

func req(remote string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// Behind a reverse proxy that is not trusted, every visitor looks like the proxy:
// the sign-in limiter would lock everybody out after ten failures. Say so, once.
func TestForwardedHintWarnsOnceAboutAnUntrustedProxy(t *testing.T) {
	log, buf := newLog()
	h := ForwardedHint(log, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	logged(t, h, req("192.0.2.1:5000", nil), req("192.0.2.1:5000", nil))
	if buf.Len() != 0 {
		t.Fatalf("a request without forwarding headers caused a warning: %s", buf)
	}
	logged(t, h, req("192.0.2.1:5000", map[string]string{"X-Forwarded-For": "203.0.113.7"}), req("192.0.2.1:5001", map[string]string{"X-Forwarded-For": "203.0.113.8"}))
	out := buf.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, "TRUSTED_PROXIES") {
		t.Fatalf("want exactly one warning that names the setting, got: %s", out)
	}
	for _, leak := range []string{"203.0.113", "192.0.2.1"} {
		if strings.Contains(out, leak) {
			t.Errorf("the warning contains an address (%s): %s", leak, out)
		}
	}
}

func TestForwardedHintStaysQuietWhenTheProxyIsTrusted(t *testing.T) {
	log, buf := newLog()
	trusted := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	h := ForwardedHint(log, trusted)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	logged(t, h, req("192.0.2.1:5000", map[string]string{"X-Forwarded-For": "203.0.113.7"}))
	if buf.Len() != 0 {
		t.Fatalf("a trusted proxy caused a warning: %s", buf)
	}
	// A forwarding header from a peer that is not trusted is spoofing or a mistake.
	logged(t, h, req("198.51.100.4:5000", map[string]string{"X-Forwarded-For": "203.0.113.7"}))
	if strings.Count(buf.String(), "level=WARN") != 1 {
		t.Fatalf("want a warning for an untrusted peer: %s", buf)
	}
}
