// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"oblivion/sdk"
)

const (
	maxRubrics    = 30 // a newspaper has a handful; the cap keeps menus and queries small
	maxRubricName = 60
	maxRubricSlug = 60
)

// rubricColors is the fixed palette: names, never free colours. The theme maps each name
// to a hex value whose contrast on the page background was checked.
var rubricColors = map[string]bool{
	"blue": true, "green": true, "purple": true, "teal": true,
	"brown": true, "olive": true, "slate": true, "crimson": true,
}

type rubricView struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	Position int64  `json:"position"`
	Items    int64  `json:"items"` // how many articles (any status) are in it
}

type rubricIn struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Color string `json:"color"`
}

func (in *rubricIn) validate() string {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n == 0 || n > maxRubricName {
		return fmt.Sprintf("name must be 1-%d characters", maxRubricName)
	}
	if !rubricColors[in.Color] {
		return "color must be one of: blue, green, purple, teal, brown, olive, slate, crimson"
	}
	if in.Slug != "" && (!slugRe.MatchString(in.Slug) || len(in.Slug) > maxRubricSlug) {
		return "slug may contain lowercase letters, digits and single hyphens"
	}
	return ""
}

// rubricWriter answers 401/403 and returns the principal when the caller may edit rubrics.
// Authors may not: rubrics shape the whole site, which is an editor's job.
func rubricWriter(w http.ResponseWriter, r *http.Request) *sdk.Principal {
	pr := principal(w, r)
	if pr == nil {
		return nil
	}
	if !pr.Can(permWrite) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return nil
	}
	return pr
}

func (p *Plugin) handleListRubrics(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	if !pr.Can(permRead) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	out, err := p.rubricList(r)
	if err != nil {
		p.internalErr(w, "list rubrics", err)
		return
	}
	sdk.WriteJSON(w, http.StatusOK, out)
}

func (p *Plugin) rubricList(r *http.Request) ([]rubricView, error) {
	rows, err := p.db.QueryContext(r.Context(),
		`SELECT r.id, r.slug, r.name, r.color, r.position, COUNT(i.id)
		 FROM content_rubrics r LEFT JOIN content_items i ON i.rubric_id = r.id
		 GROUP BY r.id ORDER BY r.position, r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rubricView{}
	for rows.Next() {
		var v rubricView
		if err := rows.Scan(&v.ID, &v.Slug, &v.Name, &v.Color, &v.Position, &v.Items); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (p *Plugin) rubric(r *http.Request, id int64) (*rubricView, error) {
	var v rubricView
	err := p.db.QueryRowContext(r.Context(),
		`SELECT r.id, r.slug, r.name, r.color, r.position, (SELECT COUNT(*) FROM content_items WHERE rubric_id = r.id)
		 FROM content_rubrics r WHERE r.id = ?`, id).Scan(&v.ID, &v.Slug, &v.Name, &v.Color, &v.Position, &v.Items)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

func (p *Plugin) handleCreateRubric(w http.ResponseWriter, r *http.Request) {
	if rubricWriter(w, r) == nil {
		return
	}
	var in rubricIn
	if !sdk.DecodeJSON(w, r, &in, 1<<14) {
		return
	}
	if msg := in.validate(); msg != "" {
		sdk.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	var n int
	if err := p.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM content_rubrics`).Scan(&n); err != nil {
		p.internalErr(w, "count rubrics", err)
		return
	}
	if n >= maxRubrics {
		sdk.WriteError(w, http.StatusConflict, fmt.Sprintf("at most %d rubrics: delete or reuse one", maxRubrics))
		return
	}
	insert := func(slug string) (int64, error) {
		defer p.version.Add(1)
		res, err := p.db.ExecContext(r.Context(),
			`INSERT INTO content_rubrics(slug, name, color, position)
			 VALUES (?, ?, ?, COALESCE((SELECT MAX(position) + 1 FROM content_rubrics), 0))`, slug, in.Name, in.Color)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	var (
		id  int64
		err error
	)
	if in.Slug != "" {
		if id, err = insert(in.Slug); sdk.IsUniqueViolation(err) {
			sdk.WriteError(w, http.StatusConflict, "slug already in use")
			return
		}
	} else {
		base := slugify(in.Name)
		if base == "" {
			base = "rubric"
		}
		for i := 1; i <= 20; i++ {
			slug := base
			if i > 1 {
				slug = base + "-" + strconv.Itoa(i)
			}
			if id, err = insert(slug); !sdk.IsUniqueViolation(err) {
				break
			}
		}
		if sdk.IsUniqueViolation(err) {
			sdk.WriteError(w, http.StatusConflict, "could not find a free slug; set one explicitly")
			return
		}
	}
	if err != nil {
		p.internalErr(w, "create rubric", err)
		return
	}
	v, err := p.rubric(r, id)
	if err != nil || v == nil {
		p.internalErr(w, "load rubric", err)
		return
	}
	sdk.WriteJSON(w, http.StatusCreated, v)
}

func (p *Plugin) handleUpdateRubric(w http.ResponseWriter, r *http.Request) {
	if rubricWriter(w, r) == nil {
		return
	}
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	var in rubricIn
	if !sdk.DecodeJSON(w, r, &in, 1<<14) {
		return
	}
	if msg := in.validate(); msg != "" {
		sdk.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	cur, err := p.rubric(r, id)
	if err != nil {
		p.internalErr(w, "load rubric", err)
		return
	}
	if cur == nil {
		sdk.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	slug := in.Slug
	if slug == "" {
		slug = cur.Slug
	}
	_, err = p.db.ExecContext(r.Context(), `UPDATE content_rubrics SET slug=?, name=?, color=? WHERE id=?`, slug, in.Name, in.Color, id)
	p.version.Add(1) // before answering: menus and rubric pages change
	if sdk.IsUniqueViolation(err) {
		sdk.WriteError(w, http.StatusConflict, "slug already in use")
		return
	}
	if err != nil {
		p.internalErr(w, "update rubric", err)
		return
	}
	v, err := p.rubric(r, id)
	if err != nil || v == nil {
		p.internalErr(w, "load rubric", err)
		return
	}
	sdk.WriteJSON(w, http.StatusOK, v)
}

// handleOrderRubrics stores the order of the menu. The list must name every rubric
// exactly once, so a stale editor cannot silently drop or duplicate one.
func (p *Plugin) handleOrderRubrics(w http.ResponseWriter, r *http.Request) {
	if rubricWriter(w, r) == nil {
		return
	}
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !sdk.DecodeJSON(w, r, &in, 1<<14) {
		return
	}
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		p.internalErr(w, "order rubrics", err)
		return
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM content_rubrics`).Scan(&n); err != nil {
		p.internalErr(w, "order rubrics", err)
		return
	}
	seen := map[int64]bool{}
	for _, id := range in.IDs {
		seen[id] = true
	}
	if len(in.IDs) != n || len(seen) != n {
		sdk.WriteError(w, http.StatusBadRequest, "ids must list every rubric exactly once")
		return
	}
	for pos, id := range in.IDs {
		res, err := tx.ExecContext(r.Context(), `UPDATE content_rubrics SET position = ? WHERE id = ?`, pos, id)
		if err != nil {
			p.internalErr(w, "order rubrics", err)
			return
		}
		if c, _ := res.RowsAffected(); c != 1 {
			sdk.WriteError(w, http.StatusBadRequest, "ids must list every rubric exactly once")
			return
		}
	}
	err = tx.Commit()
	p.version.Add(1)
	if err != nil {
		p.internalErr(w, "order rubrics", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteRubric is for administrators: it changes the structure of the whole site.
// The articles of the rubric stay (their rubric_id becomes NULL, see the migration).
func (p *Plugin) handleDeleteRubric(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	if !pr.Can(permSettings) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	res, err := p.db.ExecContext(r.Context(), `DELETE FROM content_rubrics WHERE id = ?`, id)
	p.version.Add(1)
	if err != nil {
		p.internalErr(w, "delete rubric", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		sdk.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
