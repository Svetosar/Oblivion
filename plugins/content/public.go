// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"bytes"
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed theme/*
var themeFS embed.FS

const pageSize = 10

type navItem struct{ Slug, Title string }

type listEntry struct {
	Slug, Title, Excerpt string
	PublishedAt          int64
}

type pageData struct {
	// set by handlers
	PageTitle   string // without the site name; empty on the front page
	Description string
	Path        string // canonical path ("/posts/x", "/?page=2"); empty = no canonical
	OGType      string // "website" or "article"
	OGImage     string // root-relative path of the share image, or ""
	IsHome      bool
	Posts       []listEntry
	Newer       string
	Older       string
	Item        *itemView
	Body        template.HTML // sanitized when saved (see renderMarkdown)

	// filled in by render
	UI                         *tr
	SiteTitle, SiteDescription string
	ContentLang                string
	UIAttr                     template.HTMLAttr // ` lang="xx"` when the UI language differs from the content language
	Head                       head
	Nav                        []navItem
}

var funcs = template.FuncMap{
	"date": func(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02") },
}

func page(name string) *template.Template {
	return template.Must(template.New(name).Funcs(funcs).ParseFS(themeFS, "theme/layout.html", "theme/"+name+".html"))
}

var (
	tplIndex    = page("index")
	tplSingle   = page("single")
	tplNotFound = page("notfound")
)

func (p *Plugin) registerPublic() error {
	routes := []struct {
		pattern string
		fn      http.HandlerFunc
	}{
		{"GET /{$}", p.handleIndex},
		{"GET /posts/{slug}", p.handlePost},
		{"GET /posts/{slug}/", redirectNoSlash},
		{"GET /{slug}", p.handlePage},
		{"GET /{slug}/", redirectNoSlash},
		{"GET /theme/style.css", handleCSS},
		{"GET /theme/favicon.svg", handleFavicon},
		{"GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/theme/favicon.svg", http.StatusMovedPermanently)
		}},
		{"GET /robots.txt", p.handleRobots},
		{"GET /sitemap.xml", p.handleSitemap},
		{"GET /feed.xml", p.handleFeed},
	}
	for _, rt := range routes {
		if err := p.h.HandlePublic(rt.pattern, rt.fn); err != nil {
			return err
		}
	}
	return nil
}

// redirectNoSlash sends /x/ to /x with a permanent redirect: one URL per page.
func redirectNoSlash(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimRight(r.URL.Path, "/")
	if target == "" {
		target = "/"
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

func handleFavicon(w http.ResponseWriter, _ *http.Request) {
	b, err := themeFS.ReadFile("theme/favicon.svg")
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

func handleCSS(w http.ResponseWriter, _ *http.Request) {
	b, err := themeFS.ReadFile("theme/style.css")
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(b)
}

// render fills in the parts every page shares (language, site settings, head
// metadata, navigation) and writes the page.
func (p *Plugin) render(w http.ResponseWriter, r *http.Request, code int, t *template.Template, d pageData) {
	ctx := r.Context()
	st, ui := p.settings(ctx), pickLang(r)
	nav, err := p.nav(r)
	if err != nil {
		p.internalErr(w, "load nav", err)
		return
	}
	d.UI, d.Nav, d.ContentLang = ui, nav, st.ContentLang
	if ui.Lang != st.ContentLang {
		d.UIAttr = template.HTMLAttr(` lang="` + ui.Lang + `"`)
	}
	d.SiteTitle, d.SiteDescription = st.SiteTitle, st.SiteDescription
	if d.SiteTitle == "" {
		d.SiteTitle = ui.DefaultTitle
	}

	d.Head.Title = d.SiteTitle
	if d.PageTitle != "" {
		d.Head.Title = d.PageTitle + " — " + d.SiteTitle
	}
	desc := d.Description
	if desc == "" && d.IsHome {
		desc = d.SiteDescription
	}
	d.Head.Description = desc

	abs := func(path string) string { return st.BaseURL + path }
	if st.BaseURL != "" {
		if d.Path != "" {
			d.Head.Canonical = abs(d.Path)
		}
		d.Head.RSS = abs("/feed.xml")
	}
	if code == http.StatusOK && d.OGType != "" {
		og := &openGraph{Type: d.OGType, Title: d.PageTitle, Description: desc, SiteName: d.SiteTitle, Locale: ui.Locale}
		if og.Title == "" {
			og.Title = d.SiteTitle
		}
		if st.BaseURL != "" {
			og.URL = d.Head.Canonical
			if d.OGImage != "" {
				og.Image = abs(d.OGImage)
			}
		}
		d.Head.OG = og
	}
	if code == http.StatusOK && st.BaseURL != "" {
		d.Head.JSONLD = p.structuredData(d, st, abs)
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", d); err != nil {
		p.internalErr(w, "render page", err)
		return
	}
	// The chrome language follows the visitor; caches must keep variants apart.
	w.Header().Add("Vary", "Accept-Language")
	if code != http.StatusOK {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(code)
		_, _ = buf.WriteTo(w)
		return
	}
	serveBytes(w, r, "text/html; charset=utf-8", buf.Bytes(), "")
}

func (p *Plugin) structuredData(d pageData, st settings, abs func(string) string) template.HTML {
	switch {
	case d.IsHome:
		ld := map[string]any{"@context": "https://schema.org", "@type": "WebSite", "name": d.SiteTitle,
			"url": abs("/"), "inLanguage": st.ContentLang}
		if d.SiteDescription != "" {
			ld["description"] = d.SiteDescription
		}
		return ldScript(ld)
	case d.Item != nil:
		it := d.Item
		ld := map[string]any{"@context": "https://schema.org", "@type": "WebPage", "name": it.Title, "url": abs(d.Path),
			"inLanguage": st.ContentLang, "dateModified": isoDate(it.UpdatedAt)}
		if it.Kind == "post" {
			ld["@type"] = "BlogPosting"
			ld["headline"] = it.Title
			ld["mainEntityOfPage"] = abs(d.Path)
			ld["author"] = map[string]any{"@type": "Person", "name": it.AuthorName}
			if it.PublishedAt != nil {
				ld["datePublished"] = isoDate(*it.PublishedAt)
			}
			if d.Description != "" {
				ld["description"] = d.Description
			}
			if d.OGImage != "" {
				ld["image"] = []string{abs(d.OGImage)}
			}
		}
		return ldScript(ld)
	}
	return ""
}

func (p *Plugin) notFound(w http.ResponseWriter, r *http.Request) {
	p.render(w, r, http.StatusNotFound, tplNotFound, pageData{PageTitle: pickLang(r).NotFound})
}

func (p *Plugin) nav(r *http.Request) ([]navItem, error) {
	rows, err := p.db.QueryContext(r.Context(),
		`SELECT slug, title FROM content_items WHERE kind = 'page' AND status = 'published' ORDER BY published_at, id LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []navItem
	for rows.Next() {
		var n navItem
		if err := rows.Scan(&n.Slug, &n.Title); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (p *Plugin) handleIndex(w http.ResponseWriter, r *http.Request) {
	pg := 1
	if q := r.URL.Query().Get("page"); q != "" {
		v, err := strconv.Atoi(q)
		if err != nil || v < 1 || v > 10_000 {
			p.notFound(w, r)
			return
		}
		pg = v
	}
	rows, err := p.db.QueryContext(r.Context(),
		`SELECT slug, title, lead, published_at FROM content_items WHERE kind = 'post' AND status = 'published'
		 ORDER BY published_at DESC, id DESC LIMIT ? OFFSET ?`, pageSize+1, (pg-1)*pageSize)
	if err != nil {
		p.internalErr(w, "list posts", err)
		return
	}
	defer rows.Close()
	var posts []listEntry
	for rows.Next() {
		var e listEntry
		var lead string
		if err := rows.Scan(&e.Slug, &e.Title, &lead, &e.PublishedAt); err != nil {
			p.internalErr(w, "scan post", err)
			return
		}
		e.Excerpt = excerpt(lead, 200)
		posts = append(posts, e)
	}
	if err := rows.Err(); err != nil {
		p.internalErr(w, "list posts", err)
		return
	}
	if pg > 1 && len(posts) == 0 {
		p.notFound(w, r) // an empty page past the end is a 404, not a thin duplicate
		return
	}
	d := pageData{IsHome: true, OGType: "website", Path: "/"}
	if pg > 1 {
		d.Path = "/?page=" + strconv.Itoa(pg)
		d.PageTitle = pickLang(r).PageN + " " + strconv.Itoa(pg)
	}
	if len(posts) > pageSize {
		posts, d.Older = posts[:pageSize], "/?page="+strconv.Itoa(pg+1)
	}
	if pg == 2 {
		d.Newer = "/"
	} else if pg > 2 {
		d.Newer = "/?page=" + strconv.Itoa(pg-1)
	}
	d.Posts = posts
	p.render(w, r, http.StatusOK, tplIndex, d)
}

func (p *Plugin) showPublished(w http.ResponseWriter, r *http.Request, kind, slug string) {
	var lead, og string
	it, err := scanItem(p.db.QueryRowContext(r.Context(),
		`SELECT `+itemCols+`, lead, og_image FROM content_items WHERE kind = ? AND slug = ? AND status = 'published'`, kind, slug), &lead, &og)
	if errors.Is(err, sql.ErrNoRows) {
		p.notFound(w, r)
		return
	}
	if err != nil {
		p.internalErr(w, "load item", err)
		return
	}
	body := p.finalHTML(r.Context(), it.BodyHTML)
	path, ogType := "/"+it.Slug, "website"
	if kind == "post" {
		path, ogType = "/posts/"+it.Slug, "article"
	}
	p.render(w, r, http.StatusOK, tplSingle, pageData{
		PageTitle: it.Title, Description: excerpt(lead, 160), Path: path, OGType: ogType, OGImage: og,
		Item: &it, Body: template.HTML(body),
	})
}

func (p *Plugin) handlePost(w http.ResponseWriter, r *http.Request) {
	p.showPublished(w, r, "post", r.PathValue("slug"))
}

func (p *Plugin) handlePage(w http.ResponseWriter, r *http.Request) {
	p.showPublished(w, r, "page", r.PathValue("slug"))
}
