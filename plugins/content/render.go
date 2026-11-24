// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"bytes"
	"regexp"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Raw HTML in Markdown is not enabled in goldmark (it is omitted), and the
// result is sanitized again with a conservative policy: defense in depth.
var (
	md = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(headingShift{}, 100))),
	)
	policy         = newPolicy()
	filteredPolicy = func() *bluemonday.Policy {
		// Output of content.html filters: the same policy, plus the image hints
		// the media plugin adds (width/height are already allowed by UGC).
		p := newPolicy()
		p.AllowAttrs("loading").Matching(regexp.MustCompile(`^(lazy|eager)$`)).OnElements("img")
		p.AllowAttrs("decoding").Matching(regexp.MustCompile(`^(async|sync|auto)$`)).OnElements("img")
		return p
	}()
)

func newPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.RequireNoReferrerOnLinks(true)
	p.RequireNoFollowOnLinks(true)
	return p
}

// renderMarkdown converts Markdown to sanitized HTML.
func renderMarkdown(src string) (string, error) {
	var b bytes.Buffer
	if err := md.Convert([]byte(src), &b); err != nil {
		return "", err
	}
	return policy.Sanitize(b.String()), nil
}

// headingShift demotes every heading by one level (# becomes h2 ... ###### stays
// h6). The page title is the single <h1> of a post, which search engines and
// screen readers expect; authors can still write "# Heading" naturally.
type headingShift struct{}

func (headingShift) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering && h.Level < 6 {
			h.Level++
		}
		return ast.WalkContinue, nil
	})
}
