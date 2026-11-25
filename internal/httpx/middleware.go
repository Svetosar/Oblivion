// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpx contains HTTP middleware shared by the kernel.
package httpx

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"sync"
	"time"

	"oblivion/internal/brand"
)

// SecurityHeaders sets a strict baseline of response headers.
// CSP has no 'unsafe-inline'; the admin SPA and themes must comply.
func SecurityHeaders(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			if secure {
				// No includeSubDomains and no preload: HSTS is irrevocable for the
				// whole max-age, and the operator's other hosts are not ours to bind.
				h.Set("Strict-Transport-Security", "max-age=63072000")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBody limits request bodies to n bytes.
func MaxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// Recover turns panics into 500s without leaking details to the client.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					log.Error("panic", "value", v, "stack", string(debug.Stack()))
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

// Unwrap lets http.ResponseController reach the real connection (deadlines, flushing).
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog logs method, path and status. Privacy: no IP, no query string,
// no User-Agent, no Referer are written. A successful /healthz is not logged: the
// container healthcheck calls it every 30 seconds and would bury everything else.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			if r.URL.Path == "/healthz" && sw.status == http.StatusOK {
				return
			}
			log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		})
	}
}

// ForwardedHint warns, once, when a request carries a forwarding header
// (X-Forwarded-For, Forwarded, X-Real-IP) from a peer that is not a trusted proxy.
// Without OBLIVION_TRUSTED_PROXIES every visitor behind a reverse proxy looks like
// the proxy itself, and the sign-in limiter would lock all of them out together.
// The warning names the setting and no address.
func ForwardedHint(log *slog.Logger, trusted []netip.Prefix) func(http.Handler) http.Handler {
	var once sync.Once
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a.Unmap()) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header
			if h.Get("X-Forwarded-For") != "" || h.Get("Forwarded") != "" || h.Get("X-Real-IP") != "" {
				host, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					host = r.RemoteAddr
				}
				if peer, err := netip.ParseAddr(host); err != nil || !isTrusted(peer) {
					once.Do(func() {
						log.Warn("requests arrive with forwarding headers from a peer that is not a trusted proxy: " +
							"if a reverse proxy runs in front, set " + brand.EnvPrefix + "TRUSTED_PROXIES to its address, " +
							"otherwise all visitors look like one client and sign-in limits will hit everybody at once")
					})
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Chain applies middleware so that the first one is the outermost.
func Chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}
