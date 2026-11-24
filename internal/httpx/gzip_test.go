// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(h http.HandlerFunc, acceptEnc string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/", nil)
	if acceptEnc != "" {
		r.Header.Set("Accept-Encoding", acceptEnc)
	}
	w := httptest.NewRecorder()
	Gzip(h).ServeHTTP(w, r)
	return w
}

func body(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Header().Get("Content-Encoding") != "gzip" {
		return w.Body.String()
	}
	zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("not valid gzip: %v", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var big = strings.Repeat("<p>hello world</p>\n", 100)

func TestGzipCompressesHTML(t *testing.T) {
	h := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Content-Length", "1900")
		_, _ = io.WriteString(w, big)
	}
	w := serve(h, "gzip, deflate, br")
	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Length") != "" {
		t.Fatalf("headers: %v", w.Header())
	}
	if w.Header().Get("ETag") != `W/"abc"` {
		t.Fatalf("ETag must become weak: %q", w.Header().Get("ETag"))
	}
	if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatal("missing Vary")
	}
	if got := body(t, w); got != big {
		t.Fatalf("round trip mismatch (%d bytes)", len(got))
	}
	if w.Body.Len() >= len(big)/2 {
		t.Fatalf("not actually compressed: %d of %d", w.Body.Len(), len(big))
	}
}

func TestGzipLeavesThingsAlone(t *testing.T) {
	type tc struct {
		name, enc string
		h         http.HandlerFunc
	}
	for _, c := range []tc{
		{"client does not accept", "", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, big)
		}},
		{"gzip;q=0", "gzip;q=0", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, big)
		}},
		{"image", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, big)
		}},
		{"no content type", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["Content-Type"] = nil
			_, _ = io.WriteString(w, big)
		}},
		{"already encoded", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, big)
		}},
		{"tiny known length", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "2")
			_, _ = io.WriteString(w, "ok")
		}},
		{"partial content", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Range", "bytes 0-3/100")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, big)
		}},
		{"not modified", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotModified)
		}},
	} {
		w := serve(c.h, c.enc)
		if w.Header().Get("Content-Encoding") == "gzip" {
			t.Errorf("%s: must not be gzip-encoded", c.name)
		}
	}
	if w := serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, big)
	}, "gzip"); w.Header().Get("Content-Encoding") != "br" || w.Body.String() != big {
		t.Error("an already encoded response must pass through untouched")
	}
	if w := serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, big)
	}, "br"); w.Body.String() != big {
		t.Error("body changed for a client that only accepts br")
	}
}

func TestGzipOnlyHeadersThenWriteHeader(t *testing.T) {
	// Handlers that call WriteHeader first (e.g. 404 pages) are handled the same.
	w := serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, strings.Repeat(`{"error":"x"}`, 100))
	}, "gzip")
	if w.Code != 404 || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status %d headers %v", w.Code, w.Header())
	}
	if got := body(t, w); !strings.HasPrefix(got, `{"error":"x"}`) {
		t.Fatalf("body %q", got[:20])
	}
}

func TestGzipExposesUnwrap(t *testing.T) {
	var inner http.ResponseWriter
	h := func(w http.ResponseWriter, _ *http.Request) {
		inner = w.(interface{ Unwrap() http.ResponseWriter }).Unwrap()
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	Gzip(http.HandlerFunc(h)).ServeHTTP(rec, r)
	if inner != http.ResponseWriter(rec) {
		t.Fatal("Unwrap must return the underlying writer")
	}
}
