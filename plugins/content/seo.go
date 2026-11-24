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
	"sync"
	"time"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
)

var (
	stripTags = bluemonday.StrictPolicy()
	spaceRe   = regexp.MustCompile(`\s+`)
	firstImg  = regexp.MustCompile(`<img[^>]*\ssrc="(/media/[0-9a-f]{32}\.(?:jpg|png|gif|webp))"`)
)

// leadMax bounds the stored plain-text lead; every excerpt is cut from it.
const leadMax = 600

var paragraphRe = regexp.MustCompile(`(?s)<p>(.*?)</p>`)

// blockEnd matches the end of block-level elements; a space goes there so the
// text of neighbouring items does not run together once the tags are gone.
var blockEnd = regexp.MustCompile(`(?i)</(?:p|h[1-6]|li|blockquote|pre|td|th|tr|div)>|<br\s*/?>`)

func plainText(htmlIn string) string {
	t := html.UnescapeString(stripTags.Sanitize(blockEnd.ReplaceAllString(htmlIn, "$0 ")))
	return strings.TrimSpace(spaceRe.ReplaceAllString(t, " "))
}

// leadText returns the plain text of the first paragraphs of sanitized HTML (at
// most leadMax characters). Headings and list items are skipped when there is
// prose, so a description never reads "Heading item item item". Without any
// paragraph the whole text is used.
func leadText(htmlIn string) string {
	var parts []string
	n := 0
	for _, m := range paragraphRe.FindAllStringSubmatch(htmlIn, -1) {
		if t := plainText(m[1]); t != "" {
			parts = append(parts, t)
			if n += utf8.RuneCountInString(t) + 1; n >= leadMax {
				break
			}
		}
	}
	t := strings.Join(parts, " ")
	if t == "" {
		t = plainText(htmlIn)
	}
	if utf8.RuneCountInString(t) > leadMax {
		t = string([]rune(t)[:leadMax])
	}
	return t
}

// derive computes what is stored next to the body at save time.
func derive(bodyHTML string) (lead, og string) { return leadText(bodyHTML), ogImage(bodyHTML) }

// excerpt cuts plain text (see leadText) to at most max characters at a word
// boundary: used for meta description and feed summaries.
func excerpt(t string, max int) string {
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
	out, err := p.cachedDoc("sitemap", func() ([]byte, error) {
		rows, err := p.db.QueryContext(r.Context(),
			`SELECT kind, slug, updated_at FROM content_items WHERE status = 'published' ORDER BY id LIMIT 50000`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		set := urlSet{URLs: []urlEntry{{Loc: st.BaseURL + "/"}}}
		for rows.Next() {
			var kind, slug string
			var upd int64
			if err := rows.Scan(&kind, &slug, &upd); err != nil {
				return nil, err
			}
			path := "/" + slug
			if kind == "post" {
				path = "/posts/" + slug
			}
			set.URLs = append(set.URLs, urlEntry{Loc: st.BaseURL + path, LastMod: time.Unix(upd, 0).UTC().Format("2006-01-02")})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		out, err := xml.MarshalIndent(set, "", "  ")
		return append([]byte(xml.Header), out...), err
	})
	if err != nil {
		p.internalErr(w, "sitemap", err)
		return
	}
	serveBytes(w, r, "application/xml; charset=utf-8", out, "")
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
	out, err := p.cachedDoc("feed", func() ([]byte, error) {
		rows, err := p.db.QueryContext(r.Context(),
			`SELECT slug, title, body_html, lead, published_at FROM content_items WHERE kind = 'post' AND status = 'published'
			 ORDER BY published_at DESC, id DESC LIMIT 20`)
		if err != nil {
			return nil, err
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
			var slug, t, body, lead string
			var pub int64
			if err := rows.Scan(&slug, &t, &body, &lead, &pub); err != nil {
				return nil, err
			}
			body = p.finalHTML(r.Context(), body)
			link := st.BaseURL + "/posts/" + slug
			ch.Items = append(ch.Items, rssItem{Title: t, Link: link, GUID: link, PubDate: time.Unix(pub, 0).UTC().Format(time.RFC1123Z),
				Description: excerpt(lead, 300), Content: absolutize(body, st.BaseURL)})
			newest = max(newest, pub)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if newest > 0 {
			ch.LastBuildDate = time.Unix(newest, 0).UTC().Format(time.RFC1123Z)
		}
		out, err := xml.MarshalIndent(rssDoc{Version: "2.0", Channel: ch}, "", "  ")
		return append([]byte(xml.Header), out...), err
	})
	if err != nil {
		p.internalErr(w, "feed", err)
		return
	}
	serveBytes(w, r, "application/rss+xml; charset=utf-8", out, "")
}

// docCache keeps rendered sitemap/feed documents. Any write to content or
// settings bumps Plugin.version, which invalidates everything at once.
type docCache struct {
	mu  sync.Mutex
	ver int64
	m   map[string][]byte
}

func (p *Plugin) cachedDoc(key string, build func() ([]byte, error)) ([]byte, error) {
	v := p.version.Load() // read before building: a write during the build leaves the entry stale
	p.docs.mu.Lock()
	if b, ok := p.docs.m[key]; ok && p.docs.ver == v {
		p.docs.mu.Unlock()
		return b, nil
	}
	p.docs.mu.Unlock()
	b, err := build()
	if err != nil {
		return nil, err
	}
	p.docs.mu.Lock()
	if v >= p.docs.ver {
		if v > p.docs.ver || p.docs.m == nil {
			p.docs.ver, p.docs.m = v, map[string][]byte{}
		}
		p.docs.m[key] = b
	}
	p.docs.mu.Unlock()
	return b, nil
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
//
// Whatever the filters return is untrusted: a buggy or hostile filter must not
// be able to inject script. A changed result is therefore sanitized again with
// the article policy plus the few image attributes filters legitimately add.
// An unchanged result was sanitized when saved and is not processed twice.
func (p *Plugin) finalHTML(ctx context.Context, h string) string {
	v, err := p.h.ApplyFilters(ctx, "content.html", h)
	s, ok := v.(string)
	if err != nil || !ok {
		return h
	}
	if s == h {
		return s
	}
	return filteredPolicy.Sanitize(s)
}
