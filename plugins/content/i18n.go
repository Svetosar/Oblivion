// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// tr holds the UI strings of the public theme. Posts are never translated:
// only the chrome around them follows the visitor's browser language.
type tr struct {
	Lang, Locale  string
	DefaultTitle  string
	Newer, Older  string
	Latest        string // heading of the list under the lead story
	NothingYet    string
	PageN         string // "Page 2"
	NotFound      string
	NotFoundText  string
	BackHome      string
	By            string
	Feed          string
	SkipToContent string
}

var (
	langRU = &tr{Lang: "ru", Locale: "ru_RU", DefaultTitle: "Мой сайт", Newer: "← Новее", Older: "Старше →", Latest: "Все новости",
		NothingYet: "Пока ничего не опубликовано.", PageN: "Страница", NotFound: "Страница не найдена",
		NotFoundText: "Такой страницы нет. Возможно, она удалена или адрес введён с ошибкой.",
		BackHome:     "На главную", By: "Автор", Feed: "RSS-лента", SkipToContent: "К содержимому"}
	langEN = &tr{Lang: "en", Locale: "en_US", DefaultTitle: "My site", Newer: "← Newer", Older: "Older →", Latest: "More stories",
		NothingYet: "Nothing has been published yet.", PageN: "Page", NotFound: "Page not found",
		NotFoundText: "There is no such page. It may have been removed, or the address is mistyped.",
		BackHome:     "Back to the front page", By: "By", Feed: "RSS feed", SkipToContent: "Skip to content"}
)

// pickLang chooses the UI language from Accept-Language: the supported language
// with the highest q wins; Russian is the default.
func pickLang(r *http.Request) *tr {
	type cand struct {
		lang string
		q    float64
		pos  int
	}
	var cands []cand
	for i, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, q := part, 1.0
		if semi := strings.IndexByte(part, ';'); semi >= 0 {
			tag = part[:semi]
			if v := strings.TrimSpace(part[semi+1:]); strings.HasPrefix(v, "q=") {
				if f, err := strconv.ParseFloat(v[2:], 64); err == nil {
					q = f
				}
			}
		}
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if (primary == "ru" || primary == "en") && q > 0 {
			cands = append(cands, cand{primary, q, i})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].q > cands[j].q })
	if len(cands) > 0 && cands[0].lang == "en" {
		return langEN
	}
	return langRU
}
