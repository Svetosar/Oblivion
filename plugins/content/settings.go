// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"oblivion/sdk"
)

const (
	permSettings = "content.settings" // held by administrators ("*") only
	settingsKey  = "settings"
)

// settings are the site-wide options editable in the admin.
type settings struct {
	SiteTitle       string `json:"site_title"`
	SiteDescription string `json:"site_description"`
	// BaseURL is the public address, e.g. https://example.com (no path). It is
	// needed for canonical links, sitemap and RSS, which must be absolute. It is
	// deliberately NOT derived from the Host header: that would let anyone poison
	// canonical links and caches.
	BaseURL string `json:"base_url"`
	// ContentLang is the language the posts are written in (the <article> lang).
	ContentLang string `json:"content_lang"`
}

var langTagRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)

func (s *settings) normalize() string {
	s.SiteTitle = strings.TrimSpace(s.SiteTitle)
	s.SiteDescription = strings.TrimSpace(s.SiteDescription)
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	s.ContentLang = strings.TrimSpace(s.ContentLang)
	if s.ContentLang == "" {
		s.ContentLang = "ru"
	}
	switch {
	case utf8.RuneCountInString(s.SiteTitle) > 100:
		return "site title: at most 100 characters"
	case utf8.RuneCountInString(s.SiteDescription) > 300:
		return "site description: at most 300 characters"
	case !langTagRe.MatchString(s.ContentLang):
		return "content language must be a language code such as ru or en"
	}
	if s.BaseURL != "" {
		u, err := url.Parse(s.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" ||
			u.RawQuery != "" || u.Fragment != "" || u.User != nil || len(s.BaseURL) > 200 {
			return "base URL must look like https://example.com (no path, query or credentials)"
		}
	}
	return ""
}

type settingsStore struct {
	mu     sync.RWMutex
	cached *settings
}

func (p *Plugin) settings(ctx context.Context) settings {
	p.st.mu.RLock()
	c := p.st.cached
	p.st.mu.RUnlock()
	if c != nil {
		return *c
	}
	// Load under the write lock, so a concurrent PUT cannot be overwritten by a
	// stale read that started before it.
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	if p.st.cached != nil {
		return *p.st.cached
	}
	s := settings{ContentLang: "ru"}
	if b, ok, err := p.kv.Get(ctx, settingsKey); err == nil && ok {
		if json.Unmarshal(b, &s) != nil || s.normalize() != "" {
			s = settings{ContentLang: "ru"} // stored value unusable: fall back to defaults
		}
	} else if err != nil {
		p.h.Log().Error("load settings", "err", err)
		return s // do not cache a failure
	}
	p.st.cached = &s
	return s
}

func (p *Plugin) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	if !pr.Can(permRead) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	sdk.WriteJSON(w, http.StatusOK, p.settings(r.Context()))
}

func (p *Plugin) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	if !pr.Can(permSettings) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	var in settings
	if !sdk.DecodeJSON(w, r, &in, 16<<10) {
		return
	}
	if msg := in.normalize(); msg != "" {
		sdk.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	b, _ := json.Marshal(in)
	// One lock across the write and the cache swap: two concurrent PUTs must
	// leave the cache equal to what the store holds.
	p.st.mu.Lock()
	err := p.kv.Set(r.Context(), settingsKey, b)
	if err == nil {
		p.st.cached = &in
	}
	p.st.mu.Unlock()
	if err != nil {
		p.internalErr(w, "save settings", err)
		return
	}
	sdk.WriteJSON(w, http.StatusOK, in)
}
