// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpx contains HTTP middleware shared by the kernel.
package httpx

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
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
// no User-Agent, no Referer are written.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
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
