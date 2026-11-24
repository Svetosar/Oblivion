// SPDX-License-Identifier: AGPL-3.0-or-later

package plugins

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"oblivion/internal/db"
	"oblivion/sdk"
)

type fake struct {
	mf   sdk.Manifest
	init func(ctx context.Context, h sdk.Host) error
}

func (f *fake) Manifest() sdk.Manifest { return f.mf }
func (f *fake) Init(ctx context.Context, h sdk.Host) error {
	if f.init == nil {
		return nil
	}
	return f.init(ctx, h)
}

func mf(id string, caps ...sdk.Capability) sdk.Manifest {
	return sdk.Manifest{ID: id, Name: id, Version: "0.0.1", APIVersion: sdk.APIVersion, Capabilities: caps}
}

func newMgr(t *testing.T, essential ...string) *Manager {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)), d, essential...)
}

func TestCapabilityDenied(t *testing.T) {
	m := newMgr(t)
	var gotDB, gotKV, gotAPI, gotPub error
	m.Register(&fake{mf: mf("nocaps"), init: func(_ context.Context, h sdk.Host) error {
		_, gotDB = h.DB()
		_, gotKV = h.KV()
		gotAPI = h.HandleAPI("GET /x", http.NotFoundHandler())
		gotPub = h.HandlePublic("GET /x", http.NotFoundHandler())
		return nil
	}})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{"db": gotDB, "kv": gotKV, "api": gotAPI, "public": gotPub} {
		if !errors.Is(err, sdk.ErrCapabilityDenied) {
			t.Errorf("%s: want ErrCapabilityDenied, got %v", name, err)
		}
	}
}

func TestRoutesNamespacedAndReserved(t *testing.T) {
	m := newMgr(t)
	var reserved, traversal, conflict error
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hi")) })
	m.Register(&fake{mf: mf("alpha", sdk.CapHTTPAPI, sdk.CapHTTPPublic), init: func(_ context.Context, h sdk.Host) error {
		if err := h.HandleAPI("GET /ping", ok); err != nil {
			return err
		}
		reserved = h.HandlePublic("GET /api/v1/auth/login", ok)
		traversal = h.HandleAPI("GET /../x", ok)
		_ = h.HandlePublic("GET /{$}", ok)
		conflict = h.HandlePublic("GET /{$}", ok)
		return nil
	}})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(reserved, sdk.ErrReservedRoute) || !errors.Is(traversal, sdk.ErrReservedRoute) {
		t.Errorf("reserved=%v traversal=%v", reserved, traversal)
	}
	if conflict == nil {
		t.Error("conflicting route must return an error, not panic")
	}
	rec := httptest.NewRecorder()
	m.APIHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/alpha/ping", nil))
	if rec.Code != 200 || rec.Body.String() != "hi" {
		t.Errorf("api route: %d %q", rec.Code, rec.Body.String())
	}
}

func TestHooksDeclaredNamespacedIsolated(t *testing.T) {
	m := newMgr(t)
	var undeclared, foreignEmit error
	var seen []string
	prod := mf("prod", sdk.CapHooks)
	m.Register(&fake{mf: prod, init: func(ctx context.Context, h sdk.Host) error {
		foreignEmit = h.DoAction(ctx, "other.thing", nil)
		return nil
	}})
	cm := mf("cons", sdk.CapHooks)
	cm.Hooks = []string{"prod.saved"}
	m.Register(&fake{mf: cm, init: func(_ context.Context, h sdk.Host) error {
		undeclared = h.OnAction("prod.other", func(context.Context, any) error { return nil })
		_ = h.OnAction("prod.saved", func(context.Context, any) error { panic("boom") })
		_ = h.OnAction("prod.saved", func(_ context.Context, p any) error { seen = append(seen, p.(string)); return nil })
		_ = h.OnFilter("prod.saved", func(_ context.Context, v any) (any, error) { return v.(string) + "!", nil })
		return nil
	}})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(undeclared, sdk.ErrNotDeclared) || !errors.Is(foreignEmit, sdk.ErrNotDeclared) {
		t.Errorf("undeclared=%v foreignEmit=%v", undeclared, foreignEmit)
	}
	m.bus.doAction(context.Background(), "prod.saved", "x")
	if !slices.Equal(seen, []string{"x"}) {
		t.Errorf("panicking listener must not stop others, seen=%v", seen)
	}
	if got := m.bus.applyFilters(context.Background(), "prod.saved", "v"); got != "v!" {
		t.Errorf("filter = %v", got)
	}
}

func TestOrderingAndRequirements(t *testing.T) {
	m := newMgr(t)
	var order []string
	rec := func(id string) func(context.Context, sdk.Host) error {
		return func(_ context.Context, h sdk.Host) error { order = append(order, id); return nil }
	}
	a := mf("aaa")
	a.Requires = []string{"svc.b"}
	b := mf("bbb")
	b.Provides = []string{"svc.b"}
	orphan := mf("orphan")
	orphan.Requires = []string{"svc.nobody"}
	m.Register(&fake{mf: a, init: rec("aaa")})
	m.Register(&fake{mf: b, init: rec("bbb")})
	m.Register(&fake{mf: orphan, init: rec("orphan")})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"bbb", "aaa"}) {
		t.Errorf("order = %v (provider first, unmet-requirement plugin dropped)", order)
	}
}

func TestEssentialFailsClosed(t *testing.T) {
	m := newMgr(t, "auth")
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("must refuse to start without essential plugin")
	}
	m2 := newMgr(t, "auth")
	m2.Register(&fake{mf: mf("auth"), init: func(context.Context, sdk.Host) error { return errors.New("nope") }})
	if err := m2.Start(context.Background()); err == nil {
		t.Fatal("essential Init failure must abort start")
	}
	m3 := newMgr(t)
	m3.Register(&fake{mf: mf("flaky"), init: func(context.Context, sdk.Host) error { panic("x") }})
	if err := m3.Start(context.Background()); err != nil {
		t.Fatalf("non-essential panic must be contained: %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	bad := []sdk.Manifest{
		{ID: "Bad!", Name: "x", Version: "1", APIVersion: sdk.APIVersion},
		{ID: "okid", Name: "x", Version: "1", APIVersion: 99},
		{ID: "okid", Name: "x", Version: "1", APIVersion: sdk.APIVersion, Capabilities: []sdk.Capability{"root"}},
		{ID: "okid", Name: "x", Version: "1", APIVersion: sdk.APIVersion, Hooks: []string{"a.b"}},
		{ID: "okid", Name: "x", Version: "1", APIVersion: sdk.APIVersion, Provides: []string{sdk.ServiceIdentity}},
	}
	for i, b := range bad {
		if validate(b) == nil {
			t.Errorf("case %d must be rejected", i)
		}
	}
}

func TestKVIsNamespaced(t *testing.T) {
	m := newMgr(t)
	ctx := context.Background()
	for _, id := range []string{"p1", "p2"} {
		if _, err := m.db.Exec(`INSERT INTO plugins(id, enabled) VALUES (?,1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	k1 := &kvStore{db: m.db, plugin: "p1"}
	k2 := &kvStore{db: m.db, plugin: "p2"}
	if err := k1.Set(ctx, "k", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := k2.Get(ctx, "k"); ok {
		t.Fatal("p2 must not see p1's key")
	}
	if v, ok, _ := k1.Get(ctx, "k"); !ok || string(v) != "one" {
		t.Fatalf("p1 get = %q %v", v, ok)
	}
	if err := k1.Set(ctx, "", nil); err == nil {
		t.Error("empty key must be rejected")
	}
	if err := k1.Set(ctx, "big", make([]byte, maxKVValue+1)); err == nil {
		t.Error("oversize value must be rejected")
	}
}

type fakeIdentity struct {
	p   *sdk.Principal
	err error
}

func (f fakeIdentity) Authenticate(*http.Request) (*sdk.Principal, error) { return f.p, f.err }

func TestIdentityMiddleware(t *testing.T) {
	cases := []struct {
		name string
		id   fakeIdentity
		code int
		user string
	}{
		{"anonymous", fakeIdentity{}, 200, ""},
		{"user", fakeIdentity{p: &sdk.Principal{Username: "bob"}}, 200, "bob"},
		{"forbidden", fakeIdentity{err: sdk.ErrForbidden}, 403, ""},
		{"internal error fails closed", fakeIdentity{err: errors.New("db down")}, 500, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newMgr(t)
			m.identity = c.id
			var user string
			h := m.IdentityMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if p := sdk.PrincipalFrom(r.Context()); p != nil {
					user = p.Username
				}
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
			if rec.Code != c.code || user != c.user {
				t.Errorf("code=%d user=%q", rec.Code, user)
			}
		})
	}
}

func TestPrincipalCan(t *testing.T) {
	var nilP *sdk.Principal
	if nilP.Can("x") {
		t.Error("nil principal can nothing")
	}
	if !(&sdk.Principal{Perms: map[string]bool{"*": true}}).Can("anything") {
		t.Error("* grants all")
	}
	if (&sdk.Principal{Perms: map[string]bool{"a": true}}).Can("b") {
		t.Error("b not granted")
	}
}
