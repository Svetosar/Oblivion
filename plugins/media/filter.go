// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The content plugin lets other plugins post-process article HTML through the
// "content.html" filter. This plugin uses it to add width/height (so the page
// does not jump while images load: Cumulative Layout Shift) and lazy loading to
// images it serves. No dependency between the plugins: without this plugin the
// HTML simply stays as it is.
const htmlHook = "content.html"

var (
	imgTag   = regexp.MustCompile(`<img\b[^>]*>`)
	imgSrc   = regexp.MustCompile(`\ssrc="/media/([0-9a-f]{32}\.(?:jpg|png|gif|webp))"`)
	hasAttrs = regexp.MustCompile(`\s(?:width|height|loading)=`)
)

type dims struct {
	w, h int
	miss time.Time // non-zero: the name was not found; remembered until then
}

// dimCache remembers image sizes. Stored names are immutable, so hits never go
// stale; it is simply emptied when full. Unknown names (a typo, a deleted file)
// are remembered briefly too, so a post referencing one does not hit the
// database on every view; they expire because the upload might come later.
type dimCache struct {
	mu sync.Mutex
	m  map[string]dims
}

const (
	dimCacheMax = 2000
	missTTL     = time.Minute
)

func (p *Plugin) dimensions(ctx context.Context, name string) (dims, bool) {
	now := p.now()
	p.dc.mu.Lock()
	d, ok := p.dc.m[name]
	p.dc.mu.Unlock()
	if ok && d.miss.IsZero() {
		return d, true
	}
	if ok && now.Before(d.miss) {
		return dims{}, false
	}
	d = dims{}
	err := p.db.QueryRowContext(ctx, `SELECT width, height FROM media_items WHERE stored_name = ?`, name).Scan(&d.w, &d.h)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		d = dims{miss: now.Add(missTTL)}
	case err != nil:
		return dims{}, false // a real failure is not cached
	}
	p.dc.mu.Lock()
	if p.dc.m == nil || len(p.dc.m) >= dimCacheMax {
		p.dc.m = map[string]dims{}
	}
	p.dc.m[name] = d
	p.dc.mu.Unlock()
	return d, d.miss.IsZero()
}

func (p *Plugin) filterHTML(ctx context.Context, v any) (any, error) {
	html, ok := v.(string)
	if !ok || !strings.Contains(html, "/media/") {
		return v, nil
	}
	return imgTag.ReplaceAllStringFunc(html, func(tag string) string {
		m := imgSrc.FindStringSubmatch(tag)
		if m == nil || hasAttrs.MatchString(tag) {
			return tag
		}
		d, ok := p.dimensions(ctx, m[1])
		if !ok {
			return tag
		}
		extra := fmt.Sprintf(` width="%d" height="%d" loading="lazy" decoding="async"`, d.w, d.h)
		if strings.HasSuffix(tag, "/>") {
			return tag[:len(tag)-2] + extra + "/>"
		}
		return tag[:len(tag)-1] + extra + ">"
	}), nil
}
