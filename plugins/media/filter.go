// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
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

type dims struct{ w, h int }

// dimCache remembers image sizes. Stored names are immutable, so entries never
// go stale; it is simply emptied when full.
type dimCache struct {
	mu sync.Mutex
	m  map[string]dims
}

const dimCacheMax = 2000

func (p *Plugin) dimensions(ctx context.Context, name string) (dims, bool) {
	p.dc.mu.Lock()
	d, ok := p.dc.m[name]
	p.dc.mu.Unlock()
	if ok {
		return d, true
	}
	if err := p.db.QueryRowContext(ctx, `SELECT width, height FROM media_items WHERE stored_name = ?`, name).Scan(&d.w, &d.h); err != nil {
		return dims{}, false
	}
	p.dc.mu.Lock()
	if p.dc.m == nil || len(p.dc.m) >= dimCacheMax {
		p.dc.m = map[string]dims{}
	}
	p.dc.m[name] = d
	p.dc.mu.Unlock()
	return d, true
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
