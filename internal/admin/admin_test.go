// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	root := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html>INDEX")},
		"assets/app.js": {Data: []byte("js")},
		"favicon.svg":   {Data: []byte("svg")},
	}
	h := handler(root)
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		return w
	}
	for _, p := range []string{"/admin/", "/admin", "/admin/index.html", "/admin/some/client/route", "/admin/../etc/passwd", "/admin/assets/../index.html"} {
		w := get(p)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "INDEX") {
			t.Errorf("%s: %d %q", p, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: entry point must not be cached: %q", p, w.Header().Get("Cache-Control"))
		}
	}
	if w := get("/admin/assets/app.js"); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %v", w.Code, w.Header())
	}
	if w := get("/admin/assets/missing.js"); w.Code != 404 {
		t.Errorf("missing asset must 404 (not fall back to HTML): %d", w.Code)
	}
	if w := get("/admin/favicon.svg"); w.Code != 200 || w.Body.String() != "svg" {
		t.Errorf("plain file: %d %q", w.Code, w.Body.String())
	}
}

func TestNotBuilt(t *testing.T) {
	w := httptest.NewRecorder()
	handler(fstest.MapFS{".gitkeep": {}}).ServeHTTP(w, httptest.NewRequest("GET", "/admin/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
}
