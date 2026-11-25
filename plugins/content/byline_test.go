// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The public author is a nickname typed with the article. The account name is
// never published: anyone who knew it would only need a password.

func (e *env) publicPages(slug string) string {
	e.t.Helper()
	var all strings.Builder
	for _, path := range []string{"/posts/" + slug, "/", "/feed.xml", "/sitemap.xml"} {
		all.WriteString(e.get(path, "").Body.String())
	}
	return all.String()
}

func TestBylineIsShownInsteadOfTheLogin(t *testing.T) {
	e := newEnv(t)
	e.setBase("https://example.com")
	it := e.create(editor, "post", "Пост", "published", map[string]any{"slug": "p", "byline": "Marina K."})
	if it["byline"] != "Marina K." {
		t.Fatalf("byline = %v", it["byline"])
	}
	page := e.get("/posts/p", "").Body.String()
	if !strings.Contains(page, "Автор: Marina K.") {
		t.Errorf("the page does not show the byline:\n%s", page)
	}
	ld := between(t, page, `<script type="application/ld+json">`, `</script>`)
	var post map[string]any
	if err := json.Unmarshal([]byte(ld), &post); err != nil {
		t.Fatal(err)
	}
	if a, _ := post["author"].(map[string]any); a["name"] != "Marina K." {
		t.Errorf("JSON-LD author = %v", post["author"])
	}
}

// No byline, no author: nothing falls back to the account name.
func TestWithoutBylineNobodyIsNamed(t *testing.T) {
	e := newEnv(t)
	e.setBase("https://example.com")
	e.create(alice, "post", "Черновик автора", "draft", nil)
	e.create(editor, "post", "Пост", "published", map[string]any{"slug": "p"})
	pages := e.publicPages("p")
	if strings.Contains(pages, "Автор") || strings.Contains(pages, `"author"`) {
		t.Errorf("an author is shown without a byline:\n%s", pages)
	}
	for _, login := range []string{">ed<", `"ed"`, "Автор: ed", `>alice<`, "alice"} {
		if strings.Contains(pages, login) {
			t.Errorf("the login %q is public:\n%s", login, pages)
		}
	}
	// The admin API still shows who wrote it.
	w := e.do(admin, "GET", "/api/v1/content/items?kind=post", nil)
	if !strings.Contains(w.Body.String(), `"author_name":"ed"`) {
		t.Errorf("the admin list lost the author name: %s", w.Body.String())
	}
}

func TestBylineFormat(t *testing.T) {
	e := newEnv(t)
	bad := []string{
		"Иван",                  // Cyrillic is not allowed, in nicknames as in logins
		"Ivan Иванов",           //
		"<b>x</b>",              //
		"a",                     // too short
		strings.Repeat("a", 33), // too long
		" Marina",               // leading space
		".Marina",               // must start with a letter or digit
		"a@b.c",                 //
		"Marina🐴",               //
		"Ma rina",               // no-break space
		"Marina\nK",             //
	}
	for _, b := range bad {
		w := e.do(editor, "POST", "/api/v1/content/items", map[string]any{"kind": "post", "title": "T", "body_md": "x", "status": "draft", "byline": b})
		if w.Code != 400 {
			t.Errorf("byline %q: status %d, want 400: %s", b, w.Code, w.Body.String())
		}
	}
	for _, g := range []string{"", "ab", "Marina K.", "John_Doe-7", "Mr. X 2nd", strings.Repeat("a", 32)} {
		w := e.do(editor, "POST", "/api/v1/content/items", map[string]any{"kind": "post", "title": "T", "body_md": "x", "status": "draft", "byline": g})
		if w.Code != 201 {
			t.Errorf("byline %q: status %d, want 201: %s", g, w.Code, w.Body.String())
		}
	}
}

func TestBylineMayNotBeALogin(t *testing.T) {
	e := newEnv(t)
	for _, b := range []string{"admin", "ADMIN", "Admin", "a.d.m.i.n", "a d m i n", "root", "ed", "Alice"} {
		w := e.do(editor, "POST", "/api/v1/content/items", map[string]any{"kind": "post", "title": "T", "body_md": "x", "status": "draft", "byline": b})
		if w.Code != 400 || !strings.Contains(w.Body.String(), "login") {
			t.Errorf("byline %q: status %d: %s; want 400 mentioning the login", b, w.Code, w.Body.String())
		}
	}
	// The same check on update.
	it := e.create(editor, "post", "T", "draft", nil)
	path := fmt.Sprintf("/api/v1/content/items/%d", id(it))
	w := e.putVersioned(editor, path, map[string]any{"title": "T", "body_md": "x", "status": "draft", "byline": "admin"})
	if w.Code != 400 {
		t.Errorf("update with a login as byline: %d: %s", w.Code, w.Body.String())
	}
	if got := e.item(path)["byline"]; got != "" {
		t.Errorf("a refused byline was stored: %v", got)
	}
	want(t, e.putVersioned(editor, path, map[string]any{"title": "T", "body_md": "x", "status": "draft", "byline": "Marina"}), 200)
	// An update without the field clears it, like every other field of the form.
	want(t, e.putVersioned(editor, path, map[string]any{"title": "T", "body_md": "x", "status": "draft"}), 200)
	if got := e.item(path)["byline"]; got != "" {
		t.Errorf("byline after an update without it: %v", got)
	}
}

// When the check cannot be made the nickname is not accepted and not shown.
func TestBylineFailsClosed(t *testing.T) {
	e := newEnv(t)
	e.create(editor, "post", "Пост", "published", map[string]any{"slug": "p", "byline": "Marina"})
	e.acc.mu.Lock()
	e.acc.err = errors.New("auth is down")
	e.acc.mu.Unlock()

	w := e.do(editor, "POST", "/api/v1/content/items", map[string]any{"kind": "post", "title": "T", "body_md": "x", "status": "draft", "byline": "Someone"})
	if w.Code != 503 {
		t.Errorf("save while the check is down: %d: %s; want 503", w.Code, w.Body.String())
	}
	// An empty byline needs no check.
	want(t, e.do(editor, "POST", "/api/v1/content/items", map[string]any{"kind": "post", "title": "T2", "body_md": "x", "status": "draft"}), 201)
	if page := e.get("/posts/p", "").Body.String(); strings.Contains(page, "Marina") {
		t.Errorf("a byline is shown although it cannot be checked:\n%s", page)
	}
	want(t, e.get("/posts/p", ""), 200) // the page itself still works
}

// A nickname that was free when written stops being shown once somebody gets that login.
func TestBylineHiddenWhenALoginAppearsLater(t *testing.T) {
	e := newEnv(t)
	e.setBase("https://example.com")
	e.create(editor, "post", "Пост", "published", map[string]any{"slug": "p", "byline": "newcomer"})
	if !strings.Contains(e.get("/posts/p", "").Body.String(), "Автор: newcomer") {
		t.Fatal("the byline is not shown while it is free")
	}
	e.acc.add("newcomer")
	for _, path := range []string{"/posts/p", "/feed.xml"} {
		if b := e.get(path, "").Body.String(); strings.Contains(b, "newcomer") {
			t.Errorf("%s still names the new login:\n%s", path, b)
		}
	}
	if ld := between(t, e.get("/posts/p", "").Body.String(), `<script type="application/ld+json">`, `</script>`); strings.Contains(ld, "author") {
		t.Errorf("JSON-LD still has an author: %s", ld)
	}
}

func TestPagesDoNotShowAuthors(t *testing.T) {
	e := newEnv(t)
	e.create(editor, "page", "О нас", "published", map[string]any{"slug": "about", "byline": "Marina"})
	if b := e.get("/about", "").Body.String(); strings.Contains(b, "Автор") || strings.Contains(b, "Marina") {
		t.Errorf("a page shows an author:\n%s", b)
	}
}
