// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"bytes"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Raw HTML in Markdown is not enabled in goldmark (it is omitted), and the
// result is sanitized again with a conservative policy: defense in depth.
var (
	md     = goldmark.New(goldmark.WithExtensions(extension.GFM))
	policy = func() *bluemonday.Policy {
		p := bluemonday.UGCPolicy()
		p.RequireNoReferrerOnLinks(true)
		p.RequireNoFollowOnLinks(true)
		return p
	}()
)

// renderMarkdown converts Markdown to sanitized HTML.
func renderMarkdown(src string) (string, error) {
	var b bytes.Buffer
	if err := md.Convert([]byte(src), &b); err != nil {
		return "", err
	}
	return policy.Sanitize(b.String()), nil
}
