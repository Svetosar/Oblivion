// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

// evilFilter is a plugin whose content.html filter injects markup; the content
// plugin must not trust what filters return.
type evilFilter struct{}

func (evilFilter) Manifest() sdk.Manifest {
	return sdk.Manifest{ID: "evil", Name: "Evil", Version: "0", APIVersion: sdk.APIVersion,
		Capabilities: []sdk.Capability{sdk.CapHooks}, Hooks: []string{"content.html"}}
}
func (evilFilter) Init(_ context.Context, h sdk.Host) error {
	return h.OnFilter("content.html", func(_ context.Context, v any) (any, error) {
		return v.(string) + `<script>alert(1)</script><img src="/media/` + strings.Repeat("a", 32) +
			`.png" onerror="alert(2)" width="10" height="10" loading="lazy" decoding="async">` +
			`<a href="javascript:alert(3)">x</a>`, nil
	})
}
func (evilFilter) Close(context.Context) error { return nil }

func newEnv(t *testing.T) *env {
	return newEnvWith(t)
}

func newEnvWith(t *testing.T, extra ...sdk.Plugin) *env {
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
	for _, x := range extra {
		m.Register(x)
	}
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
	if strings.Contains(pv.HTML, "<script") || !strings.Contains(pv.HTML, "<h2>Hi") {
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
	if !strings.Contains(p1, `href="/?page=2"`) || strings.Contains(p1, "Новее") {
		t.Fatalf("page 1 pager wrong: %s", p1)
	}
	p2 := e.do("", "GET", "/?page=2", nil).Body.String()
	if !strings.Contains(p2, "Post 00") || strings.Contains(p2, "Старше") || !strings.Contains(p2, `href="/"`) {
		t.Fatalf("page 2 wrong: %s", p2)
	}
	// Pages past the end and junk numbers are 404s, not thin duplicate pages.
	for _, q := range []string{"3", "99999999999", "-3", "0", "abc"} {
		want(t, e.do("", "GET", "/?page="+q, nil), 404)
	}
}

// ---- SEO, language, settings ----------------------------------------------

func (e *env) setBase(base string) {
	e.t.Helper()
	w := e.do(admin, "PUT", "/api/v1/content/settings", map[string]any{
		"site_title": "Дневник", "site_description": "Заметки обо всём", "base_url": base, "content_lang": "ru"})
	want(e.t, w, 200)
}

func (e *env) get(path, acceptLang string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://cms.test"+path, nil)
	if acceptLang != "" {
		r.Header.Set("Accept-Language", acceptLang)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func TestDefaultsAreRussianAndSafeWithoutBaseURL(t *testing.T) {
	e := newEnv(t)
	e.create(editor, "post", "Первый пост", "published", map[string]any{"body_md": "# Раздел\n\nТекст поста."})
	w := e.get("/posts/pervyy-post", "")
	want(t, w, 200)
	b := w.Body.String()
	for _, need := range []string{`<html lang="ru">`, "<title>Первый пост — Мой сайт</title>", `<meta name="description" content="Текст поста.">`, `<article lang="ru">`, "Автор: ed"} {
		if !strings.Contains(b, need) {
			t.Errorf("missing %q in:\n%s", need, b)
		}
	}
	// Without a configured base URL nothing absolute may be invented.
	for _, bad := range []string{"canonical", "application/ld+json", "og:url", "og:image", "application/rss+xml", "cms.test"} {
		if strings.Contains(b, bad) {
			t.Errorf("unexpected %q without base URL", bad)
		}
	}
	if !strings.Contains(b, `og:title`) || !strings.Contains(b, `og:locale" content="ru_RU"`) {
		t.Error("open graph basics missing")
	}
	want(t, e.get("/sitemap.xml", ""), 404)
	want(t, e.get("/feed.xml", ""), 404)
	rb := e.get("/robots.txt", "")
	want(t, rb, 200)
	if !strings.Contains(rb.Body.String(), "Disallow: /admin/") || strings.Contains(rb.Body.String(), "Sitemap:") {
		t.Fatalf("robots: %q", rb.Body)
	}
	// One <h1> per page: "# Раздел" in the body becomes <h2>.
	if strings.Count(b, "<h1") != 1 || !strings.Contains(b, "<h2>Раздел</h2>") {
		t.Errorf("heading structure wrong:\n%s", b)
	}
	// The front page has its own <h1>.
	if idx := e.get("/", "").Body.String(); strings.Count(idx, "<h1") != 1 {
		t.Errorf("front page must have exactly one h1:\n%s", idx)
	}
}

func TestHeadMetadataWithBaseURL(t *testing.T) {
	e := newEnv(t)
	e.setBase("https://example.com/")
	e.create(editor, "post", "Заметка про море", "published", map[string]any{
		"body_md": "Описание поста.\n\n![волна](/media/0123456789abcdef0123456789abcdef.png)", "slug": "more"})
	e.create(editor, "page", "О сайте", "published", map[string]any{"slug": "about"})

	b := e.get("/posts/more", "").Body.String()
	for _, need := range []string{
		`<link rel="canonical" href="https://example.com/posts/more">`,
		`<meta property="og:type" content="article">`,
		`<meta property="og:url" content="https://example.com/posts/more">`,
		`<meta property="og:image" content="https://example.com/media/0123456789abcdef0123456789abcdef.png">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<link rel="alternate" type="application/rss+xml" title="Дневник" href="https://example.com/feed.xml">`,
		"<title>Заметка про море — Дневник</title>",
	} {
		if !strings.Contains(b, need) {
			t.Errorf("missing %q in:\n%s", need, b)
		}
	}
	// JSON-LD is valid JSON with the right type and absolute URLs.
	ld := between(t, b, `<script type="application/ld+json">`, `</script>`)
	var post map[string]any
	if err := json.Unmarshal([]byte(ld), &post); err != nil {
		t.Fatalf("invalid JSON-LD %q: %v", ld, err)
	}
	if post["@type"] != "BlogPosting" || post["headline"] != "Заметка про море" || post["mainEntityOfPage"] != "https://example.com/posts/more" ||
		post["inLanguage"] != "ru" || post["datePublished"] == nil {
		t.Errorf("JSON-LD: %v", post)
	}
	if a, _ := post["author"].(map[string]any); a["name"] != "ed" {
		t.Errorf("author: %v", post["author"])
	}

	page := e.get("/about", "").Body.String()
	if !strings.Contains(page, `"@type":"WebPage"`) || !strings.Contains(page, `og:type" content="website"`) {
		t.Errorf("page metadata wrong:\n%s", page)
	}
	home := e.get("/", "").Body.String()
	if !strings.Contains(home, `"@type":"WebSite"`) || !strings.Contains(home, `<link rel="canonical" href="https://example.com/">`) ||
		!strings.Contains(home, `<meta name="description" content="Заметки обо всём">`) || !strings.Contains(home, "<h1>Дневник</h1>") {
		t.Errorf("home metadata wrong:\n%s", home)
	}
	// A hostile Host header must not leak into canonical links.
	r := httptest.NewRequest("GET", "http://evil.example/posts/more", nil)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "evil.example") {
		t.Fatal("Host header reflected into the page")
	}
}

func between(t *testing.T, s, a, b string) string {
	t.Helper()
	i := strings.Index(s, a)
	if i < 0 {
		t.Fatalf("%q not found in %s", a, s)
	}
	s = s[i+len(a):]
	j := strings.Index(s, b)
	if j < 0 {
		t.Fatalf("%q not found after %q", b, a)
	}
	return s[:j]
}

func TestStructuredDataCannotBreakOutOfScript(t *testing.T) {
	e := newEnv(t)
	e.setBase("https://example.com")
	e.create(editor, "post", `</script><script>alert(1)</script>`, "published", map[string]any{"slug": "x"})
	b := e.get("/posts/x", "").Body.String()
	if strings.Contains(b, "<script>alert") || strings.Count(b, "<script") != 1 {
		t.Fatalf("title escaped out of JSON-LD:\n%s", b)
	}
}

func TestSitemapRobotsAndFeed(t *testing.T) {
	e := newEnv(t)
	e.setBase("https://example.com")
	e.create(editor, "post", "Пост", "published", map[string]any{"slug": "post", "body_md": "Привет, **мир**!\n\n![](/media/0123456789abcdef0123456789abcdef.png)"})
	e.create(editor, "page", "Страница", "published", map[string]any{"slug": "page"})
	e.create(editor, "post", "Черновик", "draft", map[string]any{"slug": "draft"})

	sm := e.get("/sitemap.xml", "")
	want(t, sm, 200)
	var set struct {
		URLs []struct {
			Loc     string `xml:"loc"`
			LastMod string `xml:"lastmod"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(sm.Body.Bytes(), &set); err != nil {
		t.Fatalf("sitemap is not XML: %v\n%s", err, sm.Body)
	}
	var locs []string
	for _, u := range set.URLs {
		locs = append(locs, u.Loc)
	}
	if strings.Join(locs, " ") != "https://example.com/ https://example.com/posts/post https://example.com/page" {
		t.Errorf("sitemap locs: %v", locs)
	}
	if strings.Contains(sm.Body.String(), "draft") {
		t.Error("draft in sitemap")
	}

	rb := e.get("/robots.txt", "").Body.String()
	if !strings.Contains(rb, "Sitemap: https://example.com/sitemap.xml") {
		t.Errorf("robots: %q", rb)
	}

	fd := e.get("/feed.xml", "")
	want(t, fd, 200)
	var rss struct {
		Channel struct {
			Title string `xml:"title"`
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				GUID        string `xml:"guid"`
				PubDate     string `xml:"pubDate"`
				Description string `xml:"description"`
				Content     string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(fd.Body.Bytes(), &rss); err != nil {
		t.Fatalf("feed is not XML: %v\n%s", err, fd.Body)
	}
	if rss.Channel.Title != "Дневник" || len(rss.Channel.Items) != 1 {
		t.Fatalf("feed: %+v", rss.Channel)
	}
	it := rss.Channel.Items[0]
	if it.Link != "https://example.com/posts/post" || it.Description != "Привет, мир!" ||
		!strings.Contains(it.Content, `src="https://example.com/media/0123`) {
		t.Errorf("feed item: %+v", it)
	}
	if _, err := time.Parse(time.RFC1123Z, it.PubDate); err != nil {
		t.Errorf("pubDate %q: %v", it.PubDate, err)
	}
}

func TestLanguageNegotiation(t *testing.T) {
	e := newEnv(t)
	e.create(editor, "post", "Пост", "published", map[string]any{"slug": "p"})
	for _, tc := range []struct{ header, lang, marker string }{
		{"", "ru", "К содержимому"},
		{"ru-RU,ru;q=0.9", "ru", "К содержимому"},
		{"en-US,en;q=0.9", "en", "Skip to content"},
		{"en", "en", "Skip to content"},
		{"de-DE,de;q=0.9", "ru", "К содержимому"},     // unsupported: default
		{"de, en;q=0.5", "en", "Skip to content"},     // best supported
		{"en;q=0.5, ru;q=0.9", "ru", "К содержимому"}, // q decides
		{"en;q=0", "ru", "К содержимому"},             // q=0 means "not acceptable"
		{"garbage;;;q=x,,", "ru", "К содержимому"},
	} {
		w := e.get("/posts/p", tc.header)
		b := w.Body.String()
		if !strings.Contains(b, tc.marker) {
			t.Errorf("Accept-Language %q: want UI %s", tc.header, tc.lang)
		}
		// <html lang> is the content language; chrome carries its own lang only when it differs.
		if !strings.Contains(b, `<html lang="ru">`) {
			t.Errorf("Accept-Language %q: <html lang> must be the content language", tc.header)
		}
		if got := strings.Contains(b, `class="skip" href="#main" lang="en"`); got != (tc.lang == "en") {
			t.Errorf("Accept-Language %q: chrome lang attribute present=%v, UI %s", tc.header, got, tc.lang)
		}
		if !strings.Contains(b, `<article lang="ru">`) {
			t.Errorf("Accept-Language %q: the article itself must stay in the content language", tc.header)
		}
		if !strings.Contains(w.Header().Get("Vary"), "Accept-Language") {
			t.Errorf("Accept-Language %q: missing Vary", tc.header)
		}
	}
	if b := e.get("/nope", "en").Body.String(); !strings.Contains(b, "Page not found") {
		t.Error("404 not translated")
	}
}

func TestETagAndConditionalRequests(t *testing.T) {
	e := newEnv(t)
	e.create(editor, "post", "Пост", "published", map[string]any{"slug": "p"})
	w := e.get("/posts/p", "")
	etag := w.Header().Get("ETag")
	if etag == "" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers: %v", w.Header())
	}
	cond := func(path, lang, inm string) int {
		r := httptest.NewRequest("GET", "http://cms.test"+path, nil)
		r.Header.Set("If-None-Match", inm)
		if lang != "" {
			r.Header.Set("Accept-Language", lang)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		return rec.Code
	}
	if c := cond("/posts/p", "", etag); c != 304 {
		t.Fatalf("matching ETag: %d", c)
	}
	if c := cond("/posts/p", "", "W/"+etag); c != 304 {
		t.Fatalf("weak ETag form (what gzip proxies send): %d", c)
	}
	if c := cond("/posts/p", "en", etag); c != 200 {
		t.Fatalf("a different language variant must not be served as 304: %d", c)
	}
	// Editing the post changes the ETag.
	e.now = e.now.Add(time.Hour)
	want(t, e.do(editor, "PUT", "/api/v1/content/items/1", map[string]any{"title": "Пост 2", "slug": "p", "body_md": "new", "status": "published"}), 200)
	if c := cond("/posts/p", "", etag); c != 200 {
		t.Fatalf("stale ETag after edit: %d", c)
	}
}

func TestTrailingSlashRedirects(t *testing.T) {
	e := newEnv(t)
	e.create(editor, "post", "Пост", "published", map[string]any{"slug": "p"})
	e.create(editor, "page", "Страница", "published", map[string]any{"slug": "pg"})
	for from, to := range map[string]string{"/posts/p/": "/posts/p", "/pg/": "/pg", "/pg/?utm=1": "/pg?utm=1"} {
		w := e.get(from, "")
		if w.Code != 301 || w.Header().Get("Location") != to {
			t.Errorf("%s: %d -> %q, want 301 -> %q", from, w.Code, w.Header().Get("Location"), to)
		}
	}
}

func TestSettingsAPI(t *testing.T) {
	e := newEnv(t)
	put := func(user string, body map[string]any) *httptest.ResponseRecorder {
		return e.do(user, "PUT", "/api/v1/content/settings", body)
	}
	good := map[string]any{"site_title": "T", "site_description": "D", "base_url": "https://example.com", "content_lang": "ru"}
	want(t, put("", good), 401)
	want(t, put(editor, good), 403) // only administrators change site-wide settings
	want(t, put(alice, good), 403)
	want(t, put(admin, good), 200)
	want(t, e.do(alice, "GET", "/api/v1/content/settings", nil), 200) // readable for the admin UI
	want(t, e.do("", "GET", "/api/v1/content/settings", nil), 401)

	for name, bad := range map[string]map[string]any{
		"path in url":   {"base_url": "https://example.com/blog"},
		"no scheme":     {"base_url": "example.com"},
		"js scheme":     {"base_url": "javascript:alert(1)"},
		"credentials":   {"base_url": "https://u:p@example.com"},
		"query":         {"base_url": "https://example.com?x=1"},
		"long title":    {"site_title": strings.Repeat("я", 101)},
		"bad lang":      {"content_lang": "Russian"},
		"unknown field": {"nope": 1},
	} {
		if w := put(admin, bad); w.Code != 400 {
			t.Errorf("%s: status %d, body %s", name, w.Code, w.Body)
		}
	}
	// Trailing slash is normalized, empty language defaults to ru.
	w := put(admin, map[string]any{"base_url": "https://example.com///", "content_lang": ""})
	want(t, w, 200)
	if !strings.Contains(w.Body.String(), `"base_url":"https://example.com"`) || !strings.Contains(w.Body.String(), `"content_lang":"ru"`) {
		t.Errorf("normalization: %s", w.Body)
	}
	// Settings survive a restart: they live in the database, not in memory.
	e.p.st.mu.Lock()
	e.p.st.cached = nil
	e.p.st.mu.Unlock()
	if got := e.p.settings(context.Background()); got.BaseURL != "https://example.com" {
		t.Fatalf("settings not persisted: %+v", got)
	}
}

func TestExcerpt(t *testing.T) {
	for in, out := range map[string]string{
		"<p>Hello <strong>world</strong></p>":                  "Hello world",
		"<p>a &amp; b &lt;c&gt;</p>":                           "a & b <c>",
		"<h2>Заголовок</h2>\n<p>Текст   тут</p>":               "Текст тут", // prose wins over headings
		"<h2>Только</h2><ul><li>заголовок</li><li>и</li></ul>": "Только заголовок и",
		"<h2>A</h2><p>one</p><ul><li>x</li></ul><p>two</p>":    "one two",
		"<p></p><p>  </p>": "",
		"":                 "",
	} {
		if got := excerpt(leadText(in), 100); got != out {
			t.Errorf("excerpt(%q) = %q, want %q", in, got, out)
		}
	}
	if n := len([]rune(leadText("<p>" + strings.Repeat("слово ", 500) + "</p>"))); n > leadMax {
		t.Errorf("lead has %d characters, max %d", n, leadMax)
	}
	long := excerpt(leadText("<p>"+strings.Repeat("слово ", 100)+"</p>"), 50)
	if !strings.HasSuffix(long, "…") || len([]rune(long)) > 51 || strings.Contains(long, "сло…") && !strings.Contains(long, "слово…") {
		t.Errorf("long excerpt: %q", long)
	}
}

func TestFavicon(t *testing.T) {
	e := newEnv(t)
	w := e.get("/theme/favicon.svg", "")
	want(t, w, 200)
	if w.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(w.Body.String(), "<svg") {
		t.Fatalf("favicon: %v %q", w.Header(), w.Body)
	}
	if r := e.get("/favicon.ico", ""); r.Code != 301 || r.Header().Get("Location") != "/theme/favicon.svg" {
		t.Fatalf("/favicon.ico: %d %q", r.Code, r.Header().Get("Location"))
	}
	if !strings.Contains(e.get("/", "").Body.String(), `<link rel="icon" href="/theme/favicon.svg"`) {
		t.Fatal("pages do not link the favicon")
	}
}

// Concurrent PUTs must leave the in-memory cache equal to the stored value.
func TestSettingsConcurrentPutsStayConsistent(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.do(admin, "PUT", "/api/v1/content/settings", map[string]any{"site_title": fmt.Sprintf("T%d", i), "content_lang": "ru"})
		}()
	}
	wg.Wait()
	cached := e.p.settings(context.Background()).SiteTitle
	raw, ok, err := e.p.kv.Get(context.Background(), settingsKey)
	if err != nil || !ok {
		t.Fatal("settings not stored", err)
	}
	var stored settings
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if cached != stored.SiteTitle {
		t.Fatalf("cache %q != store %q", cached, stored.SiteTitle)
	}
}

func TestContentHTMLFilterOutputIsSanitizedAgain(t *testing.T) {
	e := newEnvWith(t, evilFilter{})
	e.create(editor, "post", "Hello", "published", map[string]any{"slug": "p"})
	for _, path := range []string{"/posts/p", "/feed.xml"} {
		e.setBase("https://example.com")
		b := e.get(path, "").Body.String()
		for _, bad := range []string{"alert(", "onerror", "javascript:"} {
			if strings.Contains(strings.ToLower(b), bad) {
				t.Errorf("%s: filter output was not sanitized, found %q", path, bad)
			}
		}
		if path == "/posts/p" && !strings.Contains(b, `loading="lazy"`) {
			t.Errorf("%s: legitimate image hints were stripped", path)
		}
	}
}

// Rows saved before the derived columns existed are filled in at startup.
func TestBackfillDerived(t *testing.T) {
	e := newEnv(t)
	img := strings.Repeat("a", 32)
	_, err := e.p.db.Exec(`INSERT INTO content_items(kind, slug, title, body_md, body_html, status, author_id, author_name, created_at, updated_at, published_at)
		VALUES ('post','old','Old','x',?, 'published', 1,'a',1,1,1)`, `<h2>H</h2><p>Старый текст.</p><img src="/media/`+img+`.png">`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.p.backfillDerived(context.Background()); err != nil {
		t.Fatal(err)
	}
	var lead, og string
	var derived int
	if err := e.p.db.QueryRow(`SELECT lead, og_image, derived FROM content_items WHERE slug='old'`).Scan(&lead, &og, &derived); err != nil {
		t.Fatal(err)
	}
	if lead != "Старый текст." || og != "/media/"+img+".png" || derived != 1 {
		t.Fatalf("lead=%q og=%q derived=%d", lead, og, derived)
	}
	if b := e.get("/", "").Body.String(); !strings.Contains(b, "<p>Старый текст.</p>") {
		t.Error("index does not use the stored lead")
	}
	// A second run changes nothing and terminates.
	if err := e.p.backfillDerived(context.Background()); err != nil {
		t.Fatal(err)
	}
}
