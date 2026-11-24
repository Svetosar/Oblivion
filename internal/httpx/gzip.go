// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"compress/gzip"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

var gzPool = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// Gzip compresses text-like responses (HTML, CSS, JS, JSON, XML, SVG) for
// clients that accept gzip. Images and other already-compressed types, tiny
// bodies, ranges, 204/304 and responses that are already encoded are left alone.
// A strong ETag becomes weak when compressed, as the bytes differ.
// Responses marked Cache-Control: no-store are never compressed: those are the
// per-user ones (every API reply, with session-bound CSRF tokens), and
// compressing a secret next to attacker-influenced text enables BREACH.
func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		next.ServeHTTP(gw, r)
		gw.finish()
	})
}

func acceptsGzip(h string) bool {
	for _, part := range strings.Split(h, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		return qValue(params) > 0
	}
	return false
}

// qValue reads the q parameter of one Accept-Encoding entry (default 1).
// Anything unparsable counts as 1, as most servers do; q=0, 0.0, 0.000 mean
// "not acceptable".
func qValue(params string) float64 {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || q < 0 || q > 1 {
			return 1
		}
		return q
	}
	return 1
}

func compressible(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch {
	case strings.HasPrefix(mt, "text/"):
		return true
	case mt == "application/json", mt == "application/javascript", mt == "application/xml",
		mt == "application/rss+xml", mt == "application/ld+json", mt == "image/svg+xml":
		return true
	}
	return false
}

const gzipMinSize = 256

type gzipWriter struct {
	http.ResponseWriter
	decided bool
	gz      *gzip.Writer
}

// decide inspects the headers once, right before they are sent.
func (g *gzipWriter) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	ct := h.Get("Content-Type")
	if ct != "" && compressible(ct) {
		h.Add("Vary", "Accept-Encoding") // the variant depends on the request even if we skip it below
	}
	if status < 200 || status == http.StatusNoContent || status == http.StatusNotModified || status == http.StatusPartialContent ||
		h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" || ct == "" || !compressible(ct) ||
		strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-store") {
		return
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n := atoi(cl); n >= 0 && n < gzipMinSize {
			return
		}
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("ETag", "W/"+et)
	}
	g.gz = gzPool.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func (g *gzipWriter) WriteHeader(code int) {
	g.decide(code)
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	g.decide(http.StatusOK)
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real connection.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) finish() {
	if g.gz != nil {
		_ = g.gz.Close()
		gzPool.Put(g.gz)
		g.gz = nil
	}
}
