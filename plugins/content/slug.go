// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"regexp"
	"strings"
)

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// reservedPageSlugs are first path segments that pages must never shadow.
var reservedPageSlugs = map[string]bool{
	"api": true, "admin": true, "healthz": true, "posts": true, "theme": true,
	"static": true, "media": true, "feed": true, "sitemap": true,
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t",
	'у': "u", 'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "",
	'э': "e", 'ю': "yu", 'я': "ya",
}

// slugify makes a URL slug from a title: Latin letters and digits, Cyrillic is
// transliterated, everything else becomes a separator. May return "".
func slugify(title string) string {
	var b strings.Builder
	sep := func() {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	for _, r := range strings.ToLower(title) {
		if t, ok := translit[r]; ok {
			b.WriteString(t) // may be empty (hard/soft sign)
		} else if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			sep()
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-")
	}
	return s
}
