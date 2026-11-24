// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"oblivion/internal/config"
	"oblivion/internal/db"
	"oblivion/internal/plugins"
	"oblivion/sdk"
)

type uploader struct{}

func (uploader) Manifest() sdk.Manifest {
	return sdk.Manifest{ID: "up", Name: "up", Version: "0", APIVersion: sdk.APIVersion,
		Capabilities: []sdk.Capability{sdk.CapHTTPAPI, sdk.CapHTTPPublic}, MaxBodyBytes: 4096}
}
func (uploader) Init(_ context.Context, h sdk.Host) error {
	read := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := h.HandleAPI("POST /x", read); err != nil {
		return err
	}
	return h.HandlePublic("POST /form", read)
}

func TestServerWiringAndBodyLimits(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	mgr := plugins.New(log, d)
	mgr.Register(uploader{})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{MaxBodyBytes: 1024, SecureCookie: true}, log, d, mgr)

	do := func(method, path string, n int) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(strings.Repeat("x", n)))
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, r)
		return w
	}
	if w := do("GET", "/healthz", 0); w.Code != 200 {
		t.Fatalf("healthz %d", w.Code)
	}
	if w := do("POST", "/api/v1/up/x", 4000); w.Code != 204 {
		t.Fatalf("plugin may exceed the default up to its declared limit: %d", w.Code)
	}
	if w := do("POST", "/api/v1/up/x", 5000); w.Code != 413 {
		t.Fatalf("plugin limit not enforced: %d", w.Code)
	}
	if w := do("POST", "/form", 2000); w.Code != 413 {
		t.Fatalf("a plugin's raised limit must not apply to its public routes: %d", w.Code)
	}
	if w := do("POST", "/form", 500); w.Code != 204 {
		t.Fatalf("default limit rejects small bodies: %d", w.Code)
	}
	w := do("GET", "/healthz", 0)
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Strict-Transport-Security"} {
		if w.Header().Get(h) == "" {
			t.Errorf("missing security header %s", h)
		}
	}
	if w := do("GET", "/nothing-here", 0); w.Code != 404 {
		t.Fatalf("unknown path: %d", w.Code)
	}
}

type denier struct{ err error }

func (denier) Manifest() sdk.Manifest {
	return sdk.Manifest{ID: "deny", Name: "d", Version: "0", APIVersion: sdk.APIVersion,
		Capabilities: []sdk.Capability{sdk.CapIdentity}, Provides: []string{sdk.ServiceIdentity}}
}
func (d denier) Init(_ context.Context, h sdk.Host) error           { return h.Provide(sdk.ServiceIdentity, d) }
func (d denier) Authenticate(*http.Request) (*sdk.Principal, error) { return nil, d.err }

// Error answers produced before any handler runs must carry the security headers too.
func TestSecurityHeadersOnEveryAnswer(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, err := range map[string]error{"403 forbidden": sdk.ErrForbidden, "500 identity failure": io.ErrUnexpectedEOF} {
		d, derr := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
		if derr != nil {
			t.Fatal(derr)
		}
		mgr := plugins.New(log, d)
		mgr.Register(denier{err: err})
		if e := mgr.Start(context.Background()); e != nil {
			t.Fatal(e)
		}
		srv := New(config.Config{MaxBodyBytes: 1024, SecureCookie: true}, log, d, mgr)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/anything", nil))
		if w.Code != 403 && w.Code != 500 {
			t.Fatalf("%s: status %d", name, w.Code)
		}
		for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Strict-Transport-Security", "Referrer-Policy"} {
			if w.Header().Get(h) == "" {
				t.Errorf("%s: missing %s", name, h)
			}
		}
		d.Close()
	}
}

// The real middleware chain wraps the ResponseWriter several times. Handlers that
// extend their own deadlines through http.ResponseController (the media upload
// route) only work if every wrapper exposes Unwrap.
type slowUploader struct{}

func (slowUploader) Manifest() sdk.Manifest {
	return sdk.Manifest{ID: "slow", Name: "slow", Version: "0", APIVersion: sdk.APIVersion,
		Capabilities: []sdk.Capability{sdk.CapHTTPAPI}}
}
func (slowUploader) Init(_ context.Context, h sdk.Host) error {
	return h.HandleAPI("POST /up", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			http.Error(w, "cannot extend deadline: "+err.Error(), http.StatusInternalServerError)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("ok ", 1) + strconv.Itoa(len(b))))
	}))
}

func TestResponseControllerWorksThroughTheWholeChain(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	mgr := plugins.New(log, d)
	mgr.Register(slowUploader{})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{MaxBodyBytes: 1 << 20}, log, d, mgr)
	srv.ReadTimeout = 300 * time.Millisecond // far shorter than the slow body below
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.Config.ReadTimeout = srv.ReadTimeout
	ts.Start()
	defer ts.Close()

	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("first"))
		time.Sleep(700 * time.Millisecond)
		_, _ = pw.Write([]byte("second"))
		_ = pw.Close()
	}()
	resp, err := http.Post(ts.URL+"/api/v1/slow/up", "application/octet-stream", pr)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok 11" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}
