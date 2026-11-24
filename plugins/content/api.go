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
	maxJSONBytes = 1 << 20
	maxTitleLen  = 200 // characters
	maxBodyLen   = 500_000
)

type itemView struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	BodyMD      string `json:"body_md,omitempty"`
	BodyHTML    string `json:"body_html,omitempty"`
	Status      string `json:"status"`
	AuthorID    int64  `json:"author_id"`
	AuthorName  string `json:"author_name"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	PublishedAt *int64 `json:"published_at"`
}

func (p *Plugin) registerAPI() error {
	routes := []struct {
		pattern string
		fn      http.HandlerFunc
	}{
		{"GET /items", p.handleList},
		{"GET /items/{id}", p.handleGet},
		{"POST /items", p.handleCreate},
		{"PUT /items/{id}", p.handleUpdate},
		{"DELETE /items/{id}", p.handleDelete},
		{"POST /preview", p.handlePreview},
		{"GET /settings", p.handleGetSettings},
		{"PUT /settings", p.handlePutSettings},
	}
	for _, rt := range routes {
		if err := p.h.HandleAPI(rt.pattern, rt.fn); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plugin) internalErr(w http.ResponseWriter, what string, err error) {
	p.h.Log().Error(what, "err", err)
	sdk.WriteError(w, http.StatusInternalServerError, "internal error")
}

// principal answers 401 for anonymous requests.
func principal(w http.ResponseWriter, r *http.Request) *sdk.Principal {
	pr := sdk.PrincipalFrom(r.Context())
	if pr == nil {
		sdk.WriteError(w, http.StatusUnauthorized, "not authenticated")
	}
	return pr
}

func canWrite(pr *sdk.Principal, authorID int64) bool {
	return pr.Can(permWrite) || (pr.Can(permWriteOwn) && pr.UserID == authorID)
}

func validKind(k string) bool { return k == "post" || k == "page" }

const itemCols = `id, kind, slug, title, body_md, body_html, status, author_id, author_name, created_at, updated_at, published_at`

type scanner interface{ Scan(dest ...any) error }

func scanItem(s scanner) (itemView, error) {
	var it itemView
	var pub sql.NullInt64
	err := s.Scan(&it.ID, &it.Kind, &it.Slug, &it.Title, &it.BodyMD, &it.BodyHTML, &it.Status,
		&it.AuthorID, &it.AuthorName, &it.CreatedAt, &it.UpdatedAt, &pub)
	if pub.Valid {
		it.PublishedAt = &pub.Int64
	}
	return it, err
}

func itemID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		sdk.WriteError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func (p *Plugin) load(r *http.Request, id int64) (*itemView, error) {
	it, err := scanItem(p.db.QueryRowContext(r.Context(), `SELECT `+itemCols+` FROM content_items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &it, err
}

func (p *Plugin) handleList(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	if !pr.Can(permRead) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	q := r.URL.Query()
	where, args := []string{"1=1"}, []any{}
	if k := q.Get("kind"); k != "" {
		if !validKind(k) {
			sdk.WriteError(w, http.StatusBadRequest, "invalid kind")
			return
		}
		where, args = append(where, "kind = ?"), append(args, k)
	}
	if s := q.Get("status"); s != "" {
		if s != "draft" && s != "published" {
			sdk.WriteError(w, http.StatusBadRequest, "invalid status")
			return
		}
		where, args = append(where, "status = ?"), append(args, s)
	}
	limit, offset := 50, 0
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, 200)
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v > 0 {
		offset = v
	}
	args = append(args, limit, offset)
	rows, err := p.db.QueryContext(r.Context(),
		`SELECT id, kind, slug, title, status, author_id, author_name, created_at, updated_at, published_at
		 FROM content_items WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		p.internalErr(w, "list items", err)
		return
	}
	defer rows.Close()
	out := []itemView{}
	for rows.Next() {
		var it itemView
		var pub sql.NullInt64
		if err := rows.Scan(&it.ID, &it.Kind, &it.Slug, &it.Title, &it.Status, &it.AuthorID, &it.AuthorName, &it.CreatedAt, &it.UpdatedAt, &pub); err != nil {
			p.internalErr(w, "scan item", err)
			return
		}
		if pub.Valid {
			it.PublishedAt = &pub.Int64
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		p.internalErr(w, "list items", err)
		return
	}
	sdk.WriteJSON(w, http.StatusOK, out)
}

func (p *Plugin) handleGet(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	if !pr.Can(permRead) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	it, err := p.load(r, id)
	if err != nil {
		p.internalErr(w, "load item", err)
		return
	}
	if it == nil {
		sdk.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	sdk.WriteJSON(w, http.StatusOK, it)
}

type itemIn struct {
	Title  string `json:"title"`
	Slug   string `json:"slug"`
	BodyMD string `json:"body_md"`
	Status string `json:"status"`
}

type createIn struct {
	Kind string `json:"kind"`
	itemIn
}

// validate checks the fields shared by create and update.
func (in *itemIn) validate() string {
	in.Title = strings.TrimSpace(in.Title)
	if n := utf8.RuneCountInString(in.Title); n == 0 || n > maxTitleLen {
		return fmt.Sprintf("title must be 1-%d characters", maxTitleLen)
	}
	if len(in.BodyMD) > maxBodyLen {
		return fmt.Sprintf("body must be at most %d bytes", maxBodyLen)
	}
	if in.Status != "draft" && in.Status != "published" {
		return "status must be draft or published"
	}
	if in.Slug != "" && (!slugRe.MatchString(in.Slug) || len(in.Slug) > 100) {
		return "slug may contain lowercase letters, digits and single hyphens"
	}
	return ""
}

func reservedSlug(kind, slug string) bool { return kind == "page" && reservedPageSlugs[slug] }

func (p *Plugin) handleCreate(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	if !pr.Can(permWrite) && !pr.Can(permWriteOwn) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	var in createIn
	if !sdk.DecodeJSON(w, r, &in, maxJSONBytes) {
		return
	}
	if !validKind(in.Kind) {
		sdk.WriteError(w, http.StatusBadRequest, "kind must be post or page")
		return
	}
	if msg := in.validate(); msg != "" {
		sdk.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	if in.Status == "published" && !pr.Can(permPublish) {
		sdk.WriteError(w, http.StatusForbidden, "you may not publish")
		return
	}
	if reservedSlug(in.Kind, in.Slug) {
		sdk.WriteError(w, http.StatusBadRequest, "this slug is reserved")
		return
	}
	html, err := renderMarkdown(in.BodyMD)
	if err != nil {
		p.internalErr(w, "render markdown", err)
		return
	}
	now := p.now().Unix()
	var pub any
	if in.Status == "published" {
		pub = now
	}
	insert := func(slug string) (int64, error) {
		res, err := p.db.ExecContext(r.Context(),
			`INSERT INTO content_items(kind, slug, title, body_md, body_html, status, author_id, author_name, created_at, updated_at, published_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			in.Kind, slug, in.Title, in.BodyMD, html, in.Status, pr.UserID, pr.Username, now, now, pub)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}

	var id int64
	if in.Slug != "" {
		id, err = insert(in.Slug)
		if sdk.IsUniqueViolation(err) {
			sdk.WriteError(w, http.StatusConflict, "slug already in use")
			return
		}
	} else {
		base := slugify(in.Title)
		if base == "" || reservedSlug(in.Kind, base) {
			base = in.Kind + "-" + strconv.FormatInt(now, 36)
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
		p.internalErr(w, "create item", err)
		return
	}
	it, err := p.load(r, id)
	if err != nil || it == nil {
		p.internalErr(w, "load created item", err)
		return
	}
	sdk.WriteJSON(w, http.StatusCreated, it)
}

func (p *Plugin) handleUpdate(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	var in itemIn
	if !sdk.DecodeJSON(w, r, &in, maxJSONBytes) {
		return
	}
	cur, err := p.load(r, id)
	if err != nil {
		p.internalErr(w, "load item", err)
		return
	}
	if cur == nil {
		sdk.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if !canWrite(pr, cur.AuthorID) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	if msg := in.validate(); msg != "" {
		sdk.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	// Touching live content (or making something live) is a publish-level act,
	// so an author cannot bypass review by editing a published post.
	if (cur.Status == "published" || in.Status == "published") && !pr.Can(permPublish) {
		sdk.WriteError(w, http.StatusForbidden, "you may not change published content")
		return
	}
	slug := in.Slug
	if slug == "" {
		slug = cur.Slug
	}
	if reservedSlug(cur.Kind, slug) {
		sdk.WriteError(w, http.StatusBadRequest, "this slug is reserved")
		return
	}
	html, err := renderMarkdown(in.BodyMD)
	if err != nil {
		p.internalErr(w, "render markdown", err)
		return
	}
	now := p.now().Unix()
	var pub any // keeps the first publication time across edits
	if in.Status == "published" {
		if cur.PublishedAt != nil {
			pub = *cur.PublishedAt
		} else {
			pub = now
		}
	}
	_, err = p.db.ExecContext(r.Context(),
		`UPDATE content_items SET slug=?, title=?, body_md=?, body_html=?, status=?, updated_at=?, published_at=? WHERE id=?`,
		slug, in.Title, in.BodyMD, html, in.Status, now, pub, id)
	if sdk.IsUniqueViolation(err) {
		sdk.WriteError(w, http.StatusConflict, "slug already in use")
		return
	}
	if err != nil {
		p.internalErr(w, "update item", err)
		return
	}
	it, err := p.load(r, id)
	if err != nil || it == nil {
		p.internalErr(w, "load updated item", err)
		return
	}
	sdk.WriteJSON(w, http.StatusOK, it)
}

func (p *Plugin) handleDelete(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	cur, err := p.load(r, id)
	if err != nil {
		p.internalErr(w, "load item", err)
		return
	}
	if cur == nil {
		sdk.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if !canWrite(pr, cur.AuthorID) || (cur.Status == "published" && !pr.Can(permPublish)) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	if _, err := p.db.ExecContext(r.Context(), `DELETE FROM content_items WHERE id = ?`, id); err != nil {
		p.internalErr(w, "delete item", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePreview renders Markdown exactly as publishing would, for the editor.
func (p *Plugin) handlePreview(w http.ResponseWriter, r *http.Request) {
	pr := principal(w, r)
	if pr == nil {
		return
	}
	if !pr.Can(permWrite) && !pr.Can(permWriteOwn) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	var in struct {
		BodyMD string `json:"body_md"`
	}
	if !sdk.DecodeJSON(w, r, &in, maxJSONBytes) {
		return
	}
	if len(in.BodyMD) > maxBodyLen {
		sdk.WriteError(w, http.StatusBadRequest, "body too large")
		return
	}
	html, err := renderMarkdown(in.BodyMD)
	if err != nil {
		p.internalErr(w, "render markdown", err)
		return
	}
	sdk.WriteJSON(w, http.StatusOK, map[string]string{"html": html})
}
