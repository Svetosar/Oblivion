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
	"time"
)

//go:embed theme/*
var themeFS embed.FS

const (
	siteTitle = "My site" // TODO: make configurable (settings plugin, post-beta)
	pageSize  = 10
)

type navItem struct{ Slug, Title string }

type listEntry struct {
	Slug, Title string
	PublishedAt int64
}

type pageData struct {
	SiteTitle string
	Title     string
	Nav       []navItem
	Posts     []listEntry
	Newer     string
	Older     string
	Item      *itemView
	Body      template.HTML // sanitized when saved (see renderMarkdown)
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
		{"GET /{slug}", p.handlePage},
		{"GET /theme/style.css", handleCSS},
	}
	for _, rt := range routes {
		if err := p.h.HandlePublic(rt.pattern, rt.fn); err != nil {
			return err
		}
	}
	return nil
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

func (p *Plugin) render(w http.ResponseWriter, r *http.Request, code int, t *template.Template, d pageData) {
	nav, err := p.nav(r)
	if err != nil {
		p.internalErr(w, "load nav", err)
		return
	}
	d.SiteTitle, d.Nav = siteTitle, nav
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", d); err != nil {
		p.internalErr(w, "render page", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(code)
	_, _ = buf.WriteTo(w)
}

func (p *Plugin) notFound(w http.ResponseWriter, r *http.Request) {
	p.render(w, r, http.StatusNotFound, tplNotFound, pageData{Title: "Not found"})
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
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 1 {
		pg = min(v, 10_000)
	}
	rows, err := p.db.QueryContext(r.Context(),
		`SELECT slug, title, published_at FROM content_items WHERE kind = 'post' AND status = 'published'
		 ORDER BY published_at DESC, id DESC LIMIT ? OFFSET ?`, pageSize+1, (pg-1)*pageSize)
	if err != nil {
		p.internalErr(w, "list posts", err)
		return
	}
	defer rows.Close()
	var posts []listEntry
	for rows.Next() {
		var e listEntry
		if err := rows.Scan(&e.Slug, &e.Title, &e.PublishedAt); err != nil {
			p.internalErr(w, "scan post", err)
			return
		}
		posts = append(posts, e)
	}
	if err := rows.Err(); err != nil {
		p.internalErr(w, "list posts", err)
		return
	}
	d := pageData{}
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
	it, err := scanItem(p.db.QueryRowContext(r.Context(),
		`SELECT `+itemCols+` FROM content_items WHERE kind = ? AND slug = ? AND status = 'published'`, kind, slug))
	if errors.Is(err, sql.ErrNoRows) {
		p.notFound(w, r)
		return
	}
	if err != nil {
		p.internalErr(w, "load item", err)
		return
	}
	p.render(w, r, http.StatusOK, tplSingle, pageData{Title: it.Title, Item: &it, Body: template.HTML(it.BodyHTML)})
}

func (p *Plugin) handlePost(w http.ResponseWriter, r *http.Request) {
	p.showPublished(w, r, "post", r.PathValue("slug"))
}

func (p *Plugin) handlePage(w http.ResponseWriter, r *http.Request) {
	p.showPublished(w, r, "page", r.PathValue("slug"))
}
