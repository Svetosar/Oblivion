// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const rubrics = "/api/v1/content/rubrics"

func (e *env) rubric(user, name, color string) map[string]any {
	e.t.Helper()
	w := e.do(user, "POST", rubrics, map[string]any{"name": name, "color": color})
	want(e.t, w, 201)
	m := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) listRubrics(user string) []map[string]any {
	e.t.Helper()
	w := e.do(user, "GET", rubrics, nil)
	want(e.t, w, 200)
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func TestRubricsCreateListAndPermissions(t *testing.T) {
	e := newEnv(t)
	want(t, e.do("", "GET", rubrics, nil), 401)
	want(t, e.do(alice, "POST", rubrics, map[string]any{"name": "Политика", "color": "blue"}), 403)

	r := e.rubric(editor, "Политика", "blue")
	if r["slug"] != "politika" || r["name"] != "Политика" || r["color"] != "blue" {
		t.Fatalf("rubric: %v", r)
	}
	e.rubric(editor, "Экономика", "green")
	// The same name again gets a free slug instead of failing.
	if again := e.rubric(editor, "Политика", "blue"); again["slug"] == "politika" || again["slug"] == "" {
		t.Fatalf("second rubric with the same name: %v", again)
	}
	// Authors may read the list (they pick a rubric for their draft).
	got := e.listRubrics(alice)
	if len(got) != 3 || got[0]["name"] != "Политика" || got[1]["name"] != "Экономика" {
		t.Fatalf("list order: %v", got)
	}
	if got[0]["position"].(float64) >= got[1]["position"].(float64) {
		t.Fatalf("new rubrics go to the end: %v", got)
	}
}

func TestRubricValidation(t *testing.T) {
	e := newEnv(t)
	bad := []map[string]any{
		{"name": "", "color": "blue"},
		{"name": strings.Repeat("я", 61), "color": "blue"},
		{"name": "Наука", "color": "#ff0000"}, // only the fixed palette: contrast is checked for those
		{"name": "Наука", "color": ""},
		{"name": "Наука", "color": "blue", "slug": "Bad Slug"},
	}
	for _, b := range bad {
		if w := e.do(editor, "POST", rubrics, b); w.Code != 400 {
			t.Errorf("%v: status %d, want 400: %s", b, w.Code, w.Body)
		}
	}
	e.rubric(editor, "Наука", "teal")
	if w := e.do(editor, "POST", rubrics, map[string]any{"name": "Другая", "color": "teal", "slug": "nauka"}); w.Code != 409 {
		t.Errorf("explicit duplicate slug: %d, want 409", w.Code)
	}
}

func TestRubricsAreCapped(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < maxRubrics; i++ {
		e.rubric(editor, fmt.Sprintf("Рубрика %d", i), "blue")
	}
	if w := e.do(editor, "POST", rubrics, map[string]any{"name": "Лишняя", "color": "blue"}); w.Code != 409 {
		t.Fatalf("over the cap: %d, want 409: %s", w.Code, w.Body)
	}
}

func TestRubricUpdateAndOrder(t *testing.T) {
	e := newEnv(t)
	a := e.rubric(editor, "Первая", "blue")
	b := e.rubric(editor, "Вторая", "green")
	c := e.rubric(editor, "Третья", "purple")
	aid := fmt.Sprint(int64(a["id"].(float64)))

	w := e.do(editor, "PUT", rubrics+"/"+aid, map[string]any{"name": "Главная", "slug": "glavnaya", "color": "brown"})
	want(t, w, 200)
	if got := e.listRubrics(editor)[0]; got["name"] != "Главная" || got["slug"] != "glavnaya" || got["color"] != "brown" {
		t.Fatalf("after update: %v", got)
	}
	want(t, e.do(alice, "PUT", rubrics+"/"+aid, map[string]any{"name": "x", "color": "blue"}), 403)
	want(t, e.do(editor, "PUT", rubrics+"/99999", map[string]any{"name": "x", "color": "blue"}), 404)

	ids := []any{c["id"], a["id"], b["id"]}
	want(t, e.do(editor, "PUT", rubrics+"/order", map[string]any{"ids": ids}), 204)
	got := e.listRubrics(editor)
	if got[0]["name"] != "Третья" || got[1]["name"] != "Главная" || got[2]["name"] != "Вторая" {
		t.Fatalf("order: %v", got)
	}
	// The order must name every rubric exactly once: a partial or doubled list is refused.
	for _, bad := range [][]any{{a["id"]}, {a["id"], a["id"], b["id"]}, {a["id"], b["id"], c["id"], float64(99999)}} {
		if w := e.do(editor, "PUT", rubrics+"/order", map[string]any{"ids": bad}); w.Code != 400 {
			t.Errorf("order %v: %d, want 400", bad, w.Code)
		}
	}
}

func TestRubricDeleteIsForAdministratorsAndKeepsItems(t *testing.T) {
	e := newEnv(t)
	r := e.rubric(editor, "Спорт", "olive")
	rid := fmt.Sprint(int64(r["id"].(float64)))
	it := e.create(editor, "post", "Матч", "published", map[string]any{"rubric_id": r["id"]})

	want(t, e.do(editor, "DELETE", rubrics+"/"+rid, nil), 403)
	want(t, e.do("", "DELETE", rubrics+"/"+rid, nil), 401)
	v := e.p.version.Load()
	want(t, e.do(admin, "DELETE", rubrics+"/"+rid, nil), 204)
	if e.p.version.Load() == v {
		t.Error("deleting a rubric changes public pages: the version must be bumped before answering")
	}
	want(t, e.do(admin, "DELETE", rubrics+"/"+rid, nil), 404)
	// The article stays, without a rubric.
	w := e.do(editor, "GET", fmt.Sprintf("/api/v1/content/items/%d", id(it)), nil)
	want(t, w, 200)
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["rubric_id"] != nil {
		t.Fatalf("rubric_id after the rubric was deleted: %v", got["rubric_id"])
	}
}

func TestItemNewspaperFields(t *testing.T) {
	e := newEnv(t)
	r := e.rubric(editor, "Город", "slate")
	cover := strings.Repeat("a", 32) + ".jpg"
	it := e.create(editor, "post", "Главное событие", "published", map[string]any{
		"rubric_id": r["id"], "dek": "Подзаголовок", "cover_media": cover, "cover_caption": "Фото: редакция", "placement": 1,
	})
	if it["rubric_id"] != r["id"] || it["dek"] != "Подзаголовок" || it["cover_media"] != cover || it["cover_caption"] != "Фото: редакция" || it["placement"] != float64(1) {
		t.Fatalf("created item: %v", it)
	}
	// A plain item has none of it.
	plain := e.create(editor, "post", "Обычная", "draft", nil)
	if plain["rubric_id"] != nil || plain["dek"] != "" || plain["cover_media"] != "" || plain["placement"] != float64(0) {
		t.Fatalf("defaults: %v", plain)
	}

	bad := []map[string]any{
		{"rubric_id": 99999},
		{"dek": strings.Repeat("я", 301)},
		{"cover_media": "../../etc/passwd"},
		{"cover_media": strings.Repeat("a", 32)}, // no extension
		{"cover_media": strings.Repeat("A", 32) + ".jpg"},
		{"cover_caption": strings.Repeat("я", 301)},
		{"placement": 3}, {"placement": -1},
	}
	for _, b := range bad {
		body := map[string]any{"kind": "post", "title": "x", "body_md": "x", "status": "draft"}
		for k, v := range b {
			body[k] = v
		}
		if w := e.do(editor, "POST", "/api/v1/content/items", body); w.Code != 400 {
			t.Errorf("%v: status %d, want 400: %s", b, w.Code, w.Body)
		}
	}
	// Cover captions and subtitles are plain text: HTML is shown as text by the theme, and the
	// admin API stores exactly what was sent.
	e.create(editor, "post", "С разметкой", "draft", map[string]any{"dek": "<b>x</b>"})
}

func TestPlacementIsAnEditorialDecision(t *testing.T) {
	e := newEnv(t)
	r := e.rubric(editor, "Мир", "slate")
	// An author may choose a rubric and write a subtitle, but not put the story on the front page.
	d := e.create(alice, "post", "Моя заметка", "draft", map[string]any{"rubric_id": r["id"], "dek": "Коротко"})
	if d["rubric_id"] != r["id"] {
		t.Fatalf("author draft: %v", d)
	}
	body := func(placement int) map[string]any {
		return map[string]any{"title": "Моя заметка", "body_md": "x", "status": "draft", "version": d["version"], "rubric_id": r["id"], "dek": "Коротко", "placement": placement}
	}
	path := fmt.Sprintf("/api/v1/content/items/%d", id(d))
	want(t, e.do(alice, "PUT", path, body(1)), 403)
	if w := e.do(alice, "POST", "/api/v1/content/items", map[string]any{"kind": "post", "title": "y", "body_md": "y", "status": "draft", "placement": 2}); w.Code != 403 {
		t.Errorf("author creating with a placement: %d, want 403", w.Code)
	}
	// An editor sets it; the author's later edit that leaves it unchanged is allowed.
	ed := e.do(editor, "PUT", path, body(2))
	want(t, ed, 200)
	var cur map[string]any
	_ = json.Unmarshal(ed.Body.Bytes(), &cur)
	keep := body(2)
	keep["version"] = cur["version"]
	want(t, e.do(alice, "PUT", path, keep), 200)
	clear := body(0)
	clear["version"] = cur["version"].(float64) + 1
	want(t, e.do(alice, "PUT", path, clear), 403)
}
