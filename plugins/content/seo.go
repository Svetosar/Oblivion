// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"html"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
)

var (
	stripTags = bluemonday.StrictPolicy()
	spaceRe   = regexp.MustCompile(`\s+`)
	firstImg  = regexp.MustCompile(`<img[^>]*\ssrc="(/media/[0-9a-f]{32}\.(?:jpg|png|gif|webp))"`)
)

// excerpt returns plain text of at most max characters taken from sanitized
// HTML, cut at a word boundary: used for meta description and feed summaries.
func excerpt(htmlIn string, max int) string {
	t := html.UnescapeString(stripTags.Sanitize(htmlIn))
	t = strings.TrimSpace(spaceRe.ReplaceAllString(t, " "))
	if utf8.RuneCountInString(t) <= max {
		return t
	}
	r := []rune(t)[:max]
	if i := strings.LastIndex(string(r), " "); i > max/2 {
		return strings.TrimRight(string(r)[:i], " ,.;:-—") + "…"
	}
	return string(r) + "…"
}

// ogImage returns the path of the first uploaded image used in the post, or "".
func ogImage(htmlIn string) string {
	if m := firstImg.FindStringSubmatch(htmlIn); m != nil {
		return m[1]
	}
	return ""
}

// head is everything the theme puts into <head> besides the stylesheet.
type head struct {
	Title, Description, Canonical, RSS string
	OG                                 *openGraph
	JSONLD                             template.HTML // a complete <script type="application/ld+json"> element
}

type openGraph struct {
	Type, Title, Description, URL, Image, SiteName, Locale string
}

// ldScript renders structured data. json.Marshal escapes <, > and & as \u00XX,
// so the payload cannot close the script element.
func ldScript(v any) template.HTML {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return template.HTML(`<script type="application/ld+json">` + string(b) + `</script>`)
}

func isoDate(ts int64) string { return time.Unix(ts, 0).UTC().Format(time.RFC3339) }

// ---- robots / sitemap / feed ----------------------------------------------

func (p *Plugin) handleRobots(w http.ResponseWriter, r *http.Request) {
	st := p.settings(r.Context())
	var b strings.Builder
	b.WriteString("User-agent: *\nAllow: /\nDisallow: /admin/\nDisallow: /api/\n")
	if st.BaseURL != "" {
		b.WriteString("\nSitemap: " + st.BaseURL + "/sitemap.xml\n")
	}
	serveBytes(w, r, "text/plain; charset=utf-8", []byte(b.String()), "")
}

type urlEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type urlSet struct {
	XMLName xml.Name   `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
	URLs    []urlEntry `xml:"url"`
}

func (p *Plugin) handleSitemap(w http.ResponseWriter, r *http.Request) {
	st := p.settings(r.Context())
	if st.BaseURL == "" {
		p.notFound(w, r)
		return
	}
	rows, err := p.db.QueryContext(r.Context(),
		`SELECT kind, slug, updated_at FROM content_items WHERE status = 'published' ORDER BY id LIMIT 50000`)
	if err != nil {
		p.internalErr(w, "sitemap", err)
		return
	}
	defer rows.Close()
	set := urlSet{URLs: []urlEntry{{Loc: st.BaseURL + "/"}}}
	for rows.Next() {
		var kind, slug string
		var upd int64
		if err := rows.Scan(&kind, &slug, &upd); err != nil {
			p.internalErr(w, "sitemap", err)
			return
		}
		path := "/" + slug
		if kind == "post" {
			path = "/posts/" + slug
		}
		set.URLs = append(set.URLs, urlEntry{Loc: st.BaseURL + path, LastMod: time.Unix(upd, 0).UTC().Format("2006-01-02")})
	}
	if err := rows.Err(); err != nil {
		p.internalErr(w, "sitemap", err)
		return
	}
	out, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		p.internalErr(w, "sitemap", err)
		return
	}
	serveBytes(w, r, "application/xml; charset=utf-8", append([]byte(xml.Header), out...), "")
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
	Content     string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	Language      string    `xml:"language"`
	LastBuildDate string    `xml:"lastBuildDate,omitempty"`
	AtomLink      atomLink  `xml:"http://www.w3.org/2005/Atom link"`
	Items         []rssItem `xml:"item"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssDoc struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

// absolutize makes root-relative links in sanitized HTML absolute: feed readers
// have no base URL to resolve them against.
func absolutize(h, base string) string {
	return strings.NewReplacer(`src="/`, `src="`+base+`/`, `href="/`, `href="`+base+`/`).Replace(h)
}

func (p *Plugin) handleFeed(w http.ResponseWriter, r *http.Request) {
	st := p.settings(r.Context())
	if st.BaseURL == "" {
		p.notFound(w, r)
		return
	}
	rows, err := p.db.QueryContext(r.Context(),
		`SELECT slug, title, body_html, published_at FROM content_items WHERE kind = 'post' AND status = 'published'
		 ORDER BY published_at DESC, id DESC LIMIT 20`)
	if err != nil {
		p.internalErr(w, "feed", err)
		return
	}
	defer rows.Close()
	ui := langRU
	if st.ContentLang == "en" {
		ui = langEN
	}
	title := st.SiteTitle
	if title == "" {
		title = ui.DefaultTitle
	}
	ch := rssChannel{Title: title, Link: st.BaseURL + "/", Description: st.SiteDescription, Language: st.ContentLang,
		AtomLink: atomLink{Href: st.BaseURL + "/feed.xml", Rel: "self", Type: "application/rss+xml"}}
	if ch.Description == "" {
		ch.Description = title
	}
	var newest int64
	for rows.Next() {
		var slug, t, body string
		var pub int64
		if err := rows.Scan(&slug, &t, &body, &pub); err != nil {
			p.internalErr(w, "feed", err)
			return
		}
		body = p.finalHTML(r.Context(), body)
		link := st.BaseURL + "/posts/" + slug
		ch.Items = append(ch.Items, rssItem{Title: t, Link: link, GUID: link, PubDate: time.Unix(pub, 0).UTC().Format(time.RFC1123Z),
			Description: excerpt(body, 300), Content: absolutize(body, st.BaseURL)})
		newest = max(newest, pub)
	}
	if err := rows.Err(); err != nil {
		p.internalErr(w, "feed", err)
		return
	}
	if newest > 0 {
		ch.LastBuildDate = time.Unix(newest, 0).UTC().Format(time.RFC1123Z)
	}
	out, err := xml.MarshalIndent(rssDoc{Version: "2.0", Channel: ch}, "", "  ")
	if err != nil {
		p.internalErr(w, "feed", err)
		return
	}
	serveBytes(w, r, "application/rss+xml; charset=utf-8", append([]byte(xml.Header), out...), "")
}

// ---- conditional responses --------------------------------------------------

// serveBytes writes body with a content-derived ETag and answers 304 when the
// client already has it. Pages stay revalidated ("no-cache"): cheap, always fresh.
func serveBytes(w http.ResponseWriter, r *http.Request, ctype string, body []byte, vary string) {
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("ETag", etag)
	h.Set("Cache-Control", "no-cache")
	if vary != "" {
		h.Add("Vary", vary)
	}
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(body)
}

func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}

// finalHTML lets other plugins post-process rendered article HTML (for example
// the media plugin adds image dimensions). It is a filter, not a dependency:
// without listeners the HTML passes through unchanged.
func (p *Plugin) finalHTML(ctx context.Context, h string) string {
	v, err := p.h.ApplyFilters(ctx, "content.html", h)
	if s, ok := v.(string); ok && err == nil {
		return s
	}
	return h
}
