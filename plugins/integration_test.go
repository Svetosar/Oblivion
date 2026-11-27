// SPDX-License-Identifier: AGPL-3.0-or-later

package plugins_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"oblivion/internal/config"
	"oblivion/internal/db"
	"oblivion/internal/httpx"
	iplugins "oblivion/internal/plugins"
	"oblivion/internal/server"
	officialplugins "oblivion/plugins"
	"oblivion/sdk"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestWholeSystem boots the real server with every official plugin, exactly as
// cmd/oblivion does, and walks the path from first-run setup to a reader
// opening a published post. It is the test that notices when plugins stop
// working together.
func TestWholeSystem(t *testing.T) {
	logs := &syncBuf{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	cfg := config.Config{SecureCookie: false, MaxBodyBytes: 1 << 20, DataDir: t.TempDir()}
	mgr := iplugins.New(log, d, officialplugins.EssentialIDs...)
	mgr.SetDataDir(cfg.DataDir)
	mgr.SetRuntime(sdk.Runtime{SecureCookies: false, ClientIP: httpx.ClientIPFunc(nil)})
	for _, p := range officialplugins.All() {
		mgr.Register(p)
	}
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Close(context.Background())
	ts := httptest.NewServer(server.New(cfg, log, d, mgr).Handler)
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Transport: &http.Transport{DisableCompression: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var csrf string
	do := func(method, path string, body io.Reader, ctype string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, body)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		if csrf != "" && method != "GET" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("%s: bad gzip: %v", path, err)
			}
			b, _ = io.ReadAll(zr)
		}
		return resp, b
	}
	js := func(v any) io.Reader { b, _ := json.Marshal(v); return bytes.NewReader(b) }
	expect := func(resp *http.Response, body []byte, code int) {
		t.Helper()
		if resp.StatusCode != code {
			t.Fatalf("%s %s: status %d, want %d: %.300s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, code, body)
		}
	}

	// 1. First run: the setup token is in the log, and only then.
	m := regexp.MustCompile(`setup_token=(\S+)`).FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatalf("no setup token in log:\n%s", logs)
	}
	resp, b := do("GET", "/api/v1/auth/setup", nil, "", nil)
	expect(resp, b, 200)
	resp, b = do("POST", "/api/v1/auth/setup", js(map[string]string{"token": m[1], "username": "admin", "password": "a very long password"}), "application/json", nil)
	expect(resp, b, 201)
	var sess struct{ CSRF string }
	_ = json.Unmarshal(b, &sess)
	csrf = sess.CSRF

	// 2. Settings, an image and a post that uses it.
	resp, b = do("PUT", "/api/v1/content/settings", js(map[string]string{"site_title": "Дневник", "site_description": "Заметки", "base_url": "https://example.com", "content_lang": "ru"}), "application/json", nil)
	expect(resp, b, 200)

	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for x := 0; x < 40; x++ {
		img.Set(x, x%30, color.RGBA{200, 10, 10, 255})
	}
	var pngBytes bytes.Buffer
	_ = png.Encode(&pngBytes, img)
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="волна.png"`)
	pw, _ := mw.CreatePart(h)
	_, _ = pw.Write(pngBytes.Bytes())
	_ = mw.Close()
	resp, b = do("POST", "/api/v1/media/items", &mp, mw.FormDataContentType(), nil)
	expect(resp, b, 201)
	var media struct {
		URL           string
		Width, Height int
	}
	_ = json.Unmarshal(b, &media)
	if media.Width != 40 || media.Height != 30 {
		t.Fatalf("media: %+v", media)
	}

	post := map[string]any{"kind": "post", "title": "Море", "slug": "more", "status": "published",
		"body_md": "# Раздел\n\nПервый абзац про море.\n\n![волна](" + media.URL + ")"}
	resp, b = do("POST", "/api/v1/content/items", js(post), "application/json", nil)
	expect(resp, b, 201)

	// The public author is a nickname. With the real auth plugin behind the
	// content plugin, no spelling of a login gets through, and the login is
	// never published.
	for _, nick := range []string{"admin", "ADMIN", "a.d.m.i.n", "adm1n", "Ivan admin", "Иван"} {
		bad := map[string]any{"kind": "post", "title": "x", "slug": "x", "status": "draft", "body_md": "x", "byline": nick}
		resp, b = do("POST", "/api/v1/content/items", js(bad), "application/json", nil)
		if resp.StatusCode != 400 {
			t.Errorf("nickname %q: status %d, want 400: %.200s", nick, resp.StatusCode, b)
		}
	}
	signed := map[string]any{"kind": "post", "title": "Подписанный", "slug": "signed", "status": "published", "body_md": "Текст.", "byline": "Marina K."}
	resp, b = do("POST", "/api/v1/content/items", js(signed), "application/json", nil)
	expect(resp, b, 201)
	resp, b = do("GET", "/posts/signed", nil, "", nil)
	expect(resp, b, 200)
	if !strings.Contains(string(b), "Marina K.") || strings.Contains(string(b), ">admin<") || strings.Contains(string(b), `"admin"`) {
		t.Errorf("the public post must show the nickname and never the login:\n%s", b)
	}
	resp, b = do("GET", "/posts/more", nil, "", nil)
	expect(resp, b, 200)
	if strings.Contains(string(b), "admin") {
		t.Errorf("a post without a nickname names the login:\n%s", b)
	}

	// Images and the favicon are meant to be shown on other sites (feed readers, embeds):
	// they are cross-origin. Pages and the API stay same-origin.
	for path, want := range map[string]string{
		media.URL: "cross-origin", "/theme/favicon.svg": "cross-origin",
		"/posts/more": "same-origin", "/api/v1/auth/setup": "same-origin", "/theme/style.css": "same-origin",
	} {
		resp, b = do("GET", path, nil, "", nil)
		expect(resp, b, 200)
		if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got != want {
			t.Errorf("%s: Cross-Origin-Resource-Policy = %q, want %q", path, got, want)
		}
	}

	// 3. A reader (no cookies, Russian browser, gzip) opens the post.
	reader := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	req, _ := http.NewRequest("GET", ts.URL+"/posts/more", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	rr, err := reader.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Body.Close()
	zr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("page not gzip-compressed: %v (encoding %q)", err, rr.Header.Get("Content-Encoding"))
	}
	pageB, _ := io.ReadAll(zr)
	page := string(pageB)
	for _, need := range []string{
		`<html lang="ru">`, "<title>Море — Дневник</title>",
		`<link rel="canonical" href="https://example.com/posts/more">`,
		`<meta property="og:image" content="https://example.com` + media.URL + `">`,
		`width="40" height="30" decoding="async"`, // the media plugin's filter ran through the content plugin (the first image is eager)
		"<h2>Раздел</h2>", `"@type":"BlogPosting"`,
	} {
		if !strings.Contains(page, need) {
			t.Errorf("page lacks %q:\n%s", need, page)
		}
	}
	if strings.Count(page, "<h1") != 1 {
		t.Error("page must have exactly one h1")
	}
	for _, hdr := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "ETag"} {
		if rr.Header.Get(hdr) == "" {
			t.Errorf("missing header %s", hdr)
		}
	}
	if vary := strings.Join(rr.Header.Values("Vary"), ","); !strings.Contains(vary, "Accept-Language") || !strings.Contains(vary, "Accept-Encoding") {
		t.Errorf("Vary = %q", vary)
	}

	// 4. The image itself, the machine-readable surfaces, and what must not exist.
	if resp, b = do("GET", media.URL, nil, "", nil); resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("image: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for path, marker := range map[string]string{
		"/sitemap.xml": "https://example.com/posts/more", "/feed.xml": "<title>Море</title>", "/robots.txt": "Sitemap: https://example.com/sitemap.xml",
		"/": "<h1 class=\"sr-only\">Дневник</h1>", "/admin/": "",
	} {
		resp, b = do("GET", path, nil, "", nil)
		if path == "/admin/" {
			if resp.StatusCode != 200 && resp.StatusCode != 503 { // 503 when the SPA has not been built
				t.Errorf("/admin/: %d", resp.StatusCode)
			}
			continue
		}
		if resp.StatusCode != 200 || !strings.Contains(string(b), marker) {
			t.Errorf("%s: %d, missing %q in %.200s", path, resp.StatusCode, marker, b)
		}
	}
	// Mutations without the CSRF token are refused even with a valid session.
	saved := csrf
	csrf = ""
	resp, b = do("POST", "/api/v1/content/items", js(post), "application/json", nil)
	expect(resp, b, 403)
	csrf = saved

	// 5. Logging out ends the session.
	resp, b = do("POST", "/api/v1/auth/logout", nil, "", nil)
	expect(resp, b, 204)
	resp, b = do("GET", "/api/v1/auth/me", nil, "", nil)
	expect(resp, b, 401)
	resp, b = do("GET", "/api/v1/content/items", nil, "", nil)
	expect(resp, b, 401)
}
