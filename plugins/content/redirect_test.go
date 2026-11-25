// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"strings"
	"testing"
)

// A trailing-slash redirect must never leave the site. The slash-less path is
// built from the request path, and an encoded slash (%2F) decodes inside a
// wildcard segment: "/%2F%2Fevil.com/" would otherwise become "//evil.com",
// which browsers read as another host.
func TestTrailingSlashRedirectNeverLeavesTheSite(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{
		"/%2F%2Fevil.com/",
		"/%2Fevil.com/",
		"/%5C%5Cevil.com/", // backslash: some browsers treat it as a slash
		"/posts/%2F%2Fevil.com/",
		"/posts/%2Fevil.com/",
		"/%2F%2Fevil.com%2Fx/",
		"/ok/%2F/",
	} {
		w := e.get(path, "")
		loc := w.Header().Get("Location")
		if strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, "/\\") || strings.Contains(loc, "://") {
			t.Errorf("GET %s: %d, Location %q leaves the site", path, w.Code, loc)
		}
	}
}

// The legitimate redirect keeps working, with and without a query.
func TestTrailingSlashRedirectStillWorks(t *testing.T) {
	e := newEnv(t)
	for path, want := range map[string]string{
		"/about/":          "/about",
		"/posts/hello/":    "/posts/hello",
		"/about/?utm=x":    "/about?utm=x",
		"/posts/a-b-c/?x=": "/posts/a-b-c?x=",
	} {
		w := e.get(path, "")
		if w.Code != 301 || w.Header().Get("Location") != want {
			t.Errorf("GET %s: %d Location %q, want 301 %q", path, w.Code, w.Header().Get("Location"), want)
		}
	}
}
