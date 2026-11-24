// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oblivion/internal/db"
	"oblivion/internal/plugins"
	"oblivion/sdk"
)

// fakeIdentity authenticates by the X-Test-User header: "role:id:name".
type fakeIdentity struct{}

func (fakeIdentity) Manifest() sdk.Manifest {
	return sdk.Manifest{ID: "fakeid", Name: "fake", Version: "0", APIVersion: sdk.APIVersion,
		Capabilities: []sdk.Capability{sdk.CapIdentity}, Provides: []string{sdk.ServiceIdentity}}
}
func (f fakeIdentity) Init(_ context.Context, h sdk.Host) error {
	return h.Provide(sdk.ServiceIdentity, f)
}
func (fakeIdentity) Authenticate(r *http.Request) (*sdk.Principal, error) {
	v := r.Header.Get("X-Test-User")
	if v == "" {
		return nil, nil
	}
	var role, name string
	var id int64
	if _, err := fmt.Sscanf(strings.ReplaceAll(v, ":", " "), "%s %d %s", &role, &id, &name); err != nil {
		return nil, err
	}
	perms := map[string]bool{}
	switch role {
	case "admin":
		perms["*"] = true
	case "editor":
		perms[permRead], perms[permWrite], perms[permPublish] = true, true, true
	case "author":
		perms[permRead], perms[permWriteOwn] = true, true
	}
	return &sdk.Principal{UserID: id, Username: name, Roles: []string{role}, Perms: perms}, nil
}

type env struct {
	t   *testing.T
	p   *Plugin
	h   http.Handler // API + public, behind identity
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &env{t: t, p: New(), now: time.Unix(1_800_000_000, 0)}
	e.p.now = func() time.Time { return e.now }
	m := plugins.New(slog.New(slog.NewTextHandler(io.Discard, nil)), d)
	m.Register(fakeIdentity{})
	m.Register(e.p)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", m.APIHandler())
	mux.Handle("/", m.PublicHandler())
	e.h = m.IdentityMiddleware(mux)
	return e
}

func (e *env) do(user, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, "http://cms.test"+path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

const (
	admin  = "admin:1:root"
	editor = "editor:2:ed"
	alice  = "author:3:alice"
	bob    = "author:4:bob"
)

func want(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, code, w.Body.String())
	}
}

func (e *env) create(user, kind, title, status string, extra map[string]any) map[string]any {
	e.t.Helper()
	body := map[string]any{"kind": kind, "title": title, "body_md": "Hello **world**", "status": status}
	for k, v := range extra {
		body[k] = v
	}
	w := e.do(user, "POST", "/api/v1/content/items", body)
	want(e.t, w, 201)
	m := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func id(m map[string]any) int64 { return int64(m["id"].(float64)) }

func TestSlugify(t *testing.T) {
	for in, out := range map[string]string{
		"Hello, World!":          "hello-world",
		"  Привет, мир  ":        "privet-mir",
		"Съезд Ёжиков":           "sezd-ezhikov",
		"日本語":                    "",
		"a---b":                  "a-b",
		"Mixed Привет 2026":      "mixed-privet-2026",
		strings.Repeat("a", 200): strings.Repeat("a", 80),
	} {
		if got := slugify(in); got != out {
			t.Errorf("slugify(%q) = %q, want %q", in, got, out)
		}
	}
}

func TestMarkdownIsSanitized(t *testing.T) {
	for name, src := range map[string]string{
		"raw script": "<script>alert(1)</script>",
		"js link":    "[x](javascript:alert(1))",
		"onerror":    `<img src=x onerror=alert(1)>`,
		"iframe":     `<iframe src="https://evil.example"></iframe>`,
		"data link":  "[x](data:text/html;base64,PHNjcmlwdD4=)",
		"style attr": `<p style="background:url(javascript:alert(1))">x</p>`,
		"entity-js":  "[x](&#106;avascript:alert(1))",
	} {
		html, err := renderMarkdown(src)
		if err != nil {
			t.Fatal(err)
		}
		low := strings.ToLower(html)
		for _, bad := range []string{"<script", "javascript:", "onerror", "<iframe", "data:text", "style="} {
			if strings.Contains(low, bad) {
				t.Errorf("%s: output contains %q: %s", name, bad, html)
			}
		}
	}
	html, _ := renderMarkdown("**bold** and [link](https://example.com)\n\n| a | b |\n|---|---|\n| 1 | 2 |")
	for _, ok := range []string{"<strong>bold</strong>", `href="https://example.com"`, "noopener", "<table>"} {
		if ok == "noopener" {
			ok = "noreferrer" // we require no-referrer on links (privacy)
		}
		if !strings.Contains(html, ok) {
			t.Errorf("expected %q in %s", ok, html)
		}
	}
}

func TestAnonymousCannotUseAPI(t *testing.T) {
	e := newEnv(t)
	want(t, e.do("", "GET", "/api/v1/content/items", nil), 401)
	want(t, e.do("", "POST", "/api/v1/content/items", map[string]any{"kind": "post", "title": "x", "status": "draft"}), 401)
	want(t, e.do("", "POST", "/api/v1/content/preview", map[string]any{"body_md": "x"}), 401)
}

func TestCreateAndPublicRender(t *testing.T) {
	e := newEnv(t)
	post := e.create(editor, "post", "My First Post", "published", map[string]any{"body_md": "Hello **world** <script>x</script>"})
	if post["slug"] != "my-first-post" {
		t.Fatalf("slug = %v", post["slug"])
	}
	e.create(editor, "page", "About", "published", nil)
	e.create(editor, "post", "Secret draft", "draft", nil)

	w := e.do("", "GET", "/posts/my-first-post", nil)
	want(t, w, 200)
	body := w.Body.String()
	if !strings.Contains(body, "<strong>world</strong>") || strings.Contains(body, "<script>") {
		t.Fatalf("bad render: %s", body)
	}
	if !strings.Contains(body, `href="/about"`) {
		t.Fatalf("page missing from nav: %s", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type %q", ct)
	}

	want(t, e.do("", "GET", "/about", nil), 200)
	idx := e.do("", "GET", "/", nil)
	want(t, idx, 200)
	if !strings.Contains(idx.Body.String(), "My First Post") || strings.Contains(idx.Body.String(), "Secret draft") {
		t.Fatalf("index wrong: %s", idx.Body)
	}
	// Drafts are invisible; kinds do not cross over.
	want(t, e.do("", "GET", "/posts/secret-draft", nil), 404)
	want(t, e.do("", "GET", "/my-first-post", nil), 404)
	want(t, e.do("", "GET", "/posts/about", nil), 404)
	want(t, e.do("", "GET", "/nope", nil), 404)

	css := e.do("", "GET", "/theme/style.css", nil)
	want(t, css, 200)
	if !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css content-type %q", css.Header().Get("Content-Type"))
	}
}

func TestTitleIsEscapedInTheme(t *testing.T) {
	e := newEnv(t)
	e.create(editor, "post", `<img src=x onerror=alert(1)>`, "published", map[string]any{"slug": "xss"})
	for _, path := range []string{"/", "/posts/xss"} {
		w := e.do("", "GET", path, nil)
		want(t, w, 200)
		if strings.Contains(w.Body.String(), "<img src=x") {
			t.Fatalf("%s: unescaped title: %s", path, w.Body)
		}
	}
}

func TestSlugRules(t *testing.T) {
	e := newEnv(t)
	post := func(u string, extra map[string]any) *httptest.ResponseRecorder {
		body := map[string]any{"kind": "post", "title": "T", "body_md": "x", "status": "draft"}
		for k, v := range extra {
			body[k] = v
		}
		return e.do(u, "POST", "/api/v1/content/items", body)
	}
	want(t, post(editor, map[string]any{"slug": "Bad Slug"}), 400)
	want(t, post(editor, map[string]any{"slug": "-bad"}), 400)
	want(t, post(editor, map[string]any{"slug": "same"}), 201)
	want(t, post(editor, map[string]any{"slug": "same"}), 409)
	// A page and a post may share a slug.
	want(t, e.do(editor, "POST", "/api/v1/content/items", map[string]any{"kind": "page", "title": "T", "body_md": "x", "status": "draft", "slug": "same"}), 201)

	// Pages can never shadow system routes.
	for _, s := range []string{"api", "admin", "healthz", "posts", "theme"} {
		want(t, e.do(editor, "POST", "/api/v1/content/items", map[string]any{"kind": "page", "title": "T", "body_md": "x", "status": "draft", "slug": s}), 400)
	}
	// Auto slugs avoid reserved names and collisions.
	a := e.create(editor, "page", "Admin", "draft", nil)
	if a["slug"] == "admin" {
		t.Fatal("auto slug produced a reserved name")
	}
	s1 := e.create(editor, "post", "Same Title", "draft", nil)
	s2 := e.create(editor, "post", "Same Title", "draft", nil)
	if s1["slug"] != "same-title" || s2["slug"] != "same-title-2" {
		t.Fatalf("slugs: %v %v", s1["slug"], s2["slug"])
	}
	// Titles with no usable characters still get a slug.
	n := e.create(editor, "post", "日本語", "draft", nil)
	if n["slug"] == "" {
		t.Fatal("empty slug")
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	bad := []map[string]any{
		{"kind": "video", "title": "T", "body_md": "x", "status": "draft"},
		{"kind": "post", "title": "", "body_md": "x", "status": "draft"},
		{"kind": "post", "title": strings.Repeat("x", 201), "body_md": "x", "status": "draft"},
		{"kind": "post", "title": "T", "body_md": strings.Repeat("x", maxBodyLen+1), "status": "draft"},
		{"kind": "post", "title": "T", "body_md": "x", "status": "archived"},
		{"kind": "post", "title": "T", "body_md": "x", "status": "draft", "bogus": 1},
	}
	for i, b := range bad {
		if w := e.do(editor, "POST", "/api/v1/content/items", b); w.Code != 400 {
			t.Errorf("case %d: status %d, body %s", i, w.Code, w.Body)
		}
	}
}

func TestAuthorPermissions(t *testing.T) {
	e := newEnv(t)
	// An author may create drafts but not publish.
	want(t, e.do(alice, "POST", "/api/v1/content/items", map[string]any{"kind": "post", "title": "T", "body_md": "x", "status": "published"}), 403)
	mine := e.create(alice, "post", "Alice draft", "draft", nil)
	theirs := e.create(bob, "post", "Bob draft", "draft", nil)

	path := func(m map[string]any) string { return fmt.Sprintf("/api/v1/content/items/%d", id(m)) }
	upd := map[string]any{"title": "Edited", "body_md": "y", "status": "draft"}
	want(t, e.do(alice, "PUT", path(mine), upd), 200)
	want(t, e.do(alice, "PUT", path(theirs), upd), 403)
	want(t, e.do(alice, "DELETE", path(theirs), nil), 403)
	// Cannot publish via update either.
	upd["status"] = "published"
	want(t, e.do(alice, "PUT", path(mine), upd), 403)
	// An editor can; afterwards the author can no longer touch or delete it.
	want(t, e.do(editor, "PUT", path(mine), upd), 200)
	upd["status"] = "draft"
	want(t, e.do(alice, "PUT", path(mine), upd), 403)
	want(t, e.do(alice, "DELETE", path(mine), nil), 403)
	want(t, e.do(editor, "DELETE", path(mine), nil), 204)
	want(t, e.do(editor, "GET", path(mine), nil), 404)
	want(t, e.do(alice, "DELETE", path(mine), nil), 404)
}

func TestPublishedAtSemantics(t *testing.T) {
	e := newEnv(t)
	it := e.create(editor, "post", "T", "draft", nil)
	if it["published_at"] != nil {
		t.Fatalf("draft has published_at: %v", it["published_at"])
	}
	path := fmt.Sprintf("/api/v1/content/items/%d", id(it))
	put := func(status string) map[string]any {
		w := e.do(editor, "PUT", path, map[string]any{"title": "T", "body_md": "x", "status": status})
		want(t, w, 200)
		m := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	e.now = e.now.Add(time.Hour)
	first := put("published")["published_at"]
	if first == nil {
		t.Fatal("published item without published_at")
	}
	e.now = e.now.Add(time.Hour)
	if again := put("published")["published_at"]; again != first {
		t.Fatalf("edit moved published_at: %v -> %v", first, again)
	}
	if back := put("draft")["published_at"]; back != nil {
		t.Fatalf("unpublished item kept published_at: %v", back)
	}
	want(t, e.do("", "GET", "/posts/t", nil), 404)
}

func TestListAndPreview(t *testing.T) {
	e := newEnv(t)
	e.create(editor, "post", "One", "draft", nil)
	e.create(editor, "page", "Two", "published", nil)
	var got []map[string]any
	w := e.do(alice, "GET", "/api/v1/content/items?kind=page", nil)
	want(t, w, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 1 || got[0]["title"] != "Two" {
		t.Fatalf("filter: %s", w.Body)
	}
	if _, has := got[0]["body_md"]; has {
		t.Fatal("list must not include bodies")
	}
	want(t, e.do(alice, "GET", "/api/v1/content/items?kind=bad", nil), 400)
	want(t, e.do(alice, "GET", "/api/v1/content/items?status=bad", nil), 400)

	w = e.do(alice, "POST", "/api/v1/content/preview", map[string]any{"body_md": "# Hi <script>x</script>"})
	want(t, w, 200)
	var pv struct{ HTML string }
	if err := json.Unmarshal(w.Body.Bytes(), &pv); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pv.HTML, "<script") || !strings.Contains(pv.HTML, "<h1>Hi") {
		t.Fatalf("preview: %q", pv.HTML)
	}
}

func TestPagination(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < pageSize+3; i++ {
		e.now = e.now.Add(time.Minute)
		e.create(editor, "post", fmt.Sprintf("Post %02d", i), "published", nil)
	}
	p1 := e.do("", "GET", "/", nil).Body.String()
	if !strings.Contains(p1, `href="/?page=2"`) || strings.Contains(p1, "Newer") {
		t.Fatalf("page 1 pager wrong: %s", p1)
	}
	p2 := e.do("", "GET", "/?page=2", nil).Body.String()
	if !strings.Contains(p2, "Post 00") || strings.Contains(p2, "Older") || !strings.Contains(p2, `href="/"`) {
		t.Fatalf("page 2 wrong: %s", p2)
	}
	want(t, e.do("", "GET", "/?page=99999999999", nil), 200) // junk page numbers are clamped, never an error
	want(t, e.do("", "GET", "/?page=-3", nil), 200)
}
