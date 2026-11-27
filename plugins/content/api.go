// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
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
	// Version counts saves, starting at 1. Updates must send the one they loaded.
	Version int64 `json:"version"`
	// Byline is the author's nickname shown on the site ("" = none). The account
	// name (AuthorName) is for the admin only and is never published.
	Byline string `json:"byline"`
	// Newspaper fields. RubricID is nil for "no rubric"; Placement: 0 none, 1 the main
	// story of the front page, 2 important (an editorial decision, see checkNewspaper).
	RubricID     *int64 `json:"rubric_id"`
	Dek          string `json:"dek"`
	CoverMedia   string `json:"cover_media"`
	CoverCaption string `json:"cover_caption"`
	Placement    int    `json:"placement"`
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
		{"GET /rubrics", p.handleListRubrics},
		{"POST /rubrics", p.handleCreateRubric},
		{"PUT /rubrics/order", p.handleOrderRubrics},
		{"PUT /rubrics/{id}", p.handleUpdateRubric},
		{"DELETE /rubrics/{id}", p.handleDeleteRubric},
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

const itemCols = `id, kind, slug, title, body_md, body_html, status, author_id, author_name, created_at, updated_at, published_at, version, byline, rubric_id, dek, cover_media, cover_caption, placement`

type scanner interface{ Scan(dest ...any) error }

// scanItem reads itemCols; extra receives any further selected columns.
func scanItem(s scanner, extra ...any) (itemView, error) {
	var it itemView
	var pub, rubric sql.NullInt64
	err := s.Scan(append([]any{&it.ID, &it.Kind, &it.Slug, &it.Title, &it.BodyMD, &it.BodyHTML, &it.Status,
		&it.AuthorID, &it.AuthorName, &it.CreatedAt, &it.UpdatedAt, &pub, &it.Version, &it.Byline,
		&rubric, &it.Dek, &it.CoverMedia, &it.CoverCaption, &it.Placement}, extra...)...)
	if pub.Valid {
		it.PublishedAt = &pub.Int64
	}
	if rubric.Valid {
		it.RubricID = &rubric.Int64
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
		`SELECT id, kind, slug, title, status, author_id, author_name, created_at, updated_at, published_at, rubric_id, placement
		 FROM content_items WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		p.internalErr(w, "list items", err)
		return
	}
	defer rows.Close()
	out := []itemView{}
	for rows.Next() {
		var it itemView
		var pub, rubric sql.NullInt64
		if err := rows.Scan(&it.ID, &it.Kind, &it.Slug, &it.Title, &it.Status, &it.AuthorID, &it.AuthorName, &it.CreatedAt, &it.UpdatedAt, &pub, &rubric, &it.Placement); err != nil {
			p.internalErr(w, "scan item", err)
			return
		}
		if pub.Valid {
			it.PublishedAt = &pub.Int64
		}
		if rubric.Valid {
			it.RubricID = &rubric.Int64
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
	Title        string `json:"title"`
	Slug         string `json:"slug"`
	BodyMD       string `json:"body_md"`
	Status       string `json:"status"`
	Byline       string `json:"byline"`
	RubricID     *int64 `json:"rubric_id"`
	Dek          string `json:"dek"`
	CoverMedia   string `json:"cover_media"`
	CoverCaption string `json:"cover_caption"`
	Placement    int    `json:"placement"`
}

const maxDekLen = 300 // characters; also the cover caption

// coverRe is the shape of a stored media file name (see the media plugin): 32 hex digits and an extension.
var coverRe = regexp.MustCompile(`^[0-9a-f]{32}\.(?:jpg|png|gif|webp)$`)

type createIn struct {
	Kind string `json:"kind"`
	itemIn
}

type updateIn struct {
	itemIn
	Version int64 `json:"version"`
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
	in.Dek, in.CoverCaption = strings.TrimSpace(in.Dek), strings.TrimSpace(in.CoverCaption)
	if utf8.RuneCountInString(in.Dek) > maxDekLen {
		return fmt.Sprintf("dek must be at most %d characters", maxDekLen)
	}
	if utf8.RuneCountInString(in.CoverCaption) > maxDekLen {
		return fmt.Sprintf("cover caption must be at most %d characters", maxDekLen)
	}
	if in.CoverMedia != "" && !coverRe.MatchString(in.CoverMedia) {
		return "cover must be the file name of an uploaded image"
	}
	if in.Placement < 0 || in.Placement > 2 {
		return "placement must be 0 (none), 1 (main) or 2 (important)"
	}
	return ""
}

// checkNewspaper checks what needs the database or the caller's rights: the rubric must exist,
// and the front page is an editorial decision, so only someone who may publish can change an
// article's placement (cur is nil for a new article). It answers for the client.
func (p *Plugin) checkNewspaper(w http.ResponseWriter, r *http.Request, pr *sdk.Principal, in *itemIn, cur *itemView) bool {
	was := 0
	if cur != nil {
		was = cur.Placement
	}
	if in.Placement != was && !pr.Can(permPublish) {
		sdk.WriteError(w, http.StatusForbidden, "you may not change the placement on the front page")
		return false
	}
	if in.RubricID != nil {
		var one int
		err := p.db.QueryRowContext(r.Context(), `SELECT 1 FROM content_rubrics WHERE id = ?`, *in.RubricID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			sdk.WriteError(w, http.StatusBadRequest, "no such rubric")
			return false
		}
		if err != nil {
			p.internalErr(w, "check rubric", err)
			return false
		}
	}
	return true
}

// A nickname: Latin letters, digits, space and . _ - , 2 to 32 characters. No
// Cyrillic, like logins and passwords: look-alike letters are the easiest way to
// smuggle a login into a nickname.
var bylineRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{1,31}$`)

// checkByline validates the nickname and refuses anything that equals or contains
// a login. It answers for the client and reports whether the save may go on.
// When the account service cannot answer, the nickname is refused (503).
func (p *Plugin) checkByline(w http.ResponseWriter, r *http.Request, byline string) bool {
	if byline == "" {
		return true
	}
	if !bylineRe.MatchString(byline) || strings.TrimSpace(byline) != byline {
		sdk.WriteError(w, http.StatusBadRequest, "nickname: 2-32 characters, Latin letters, digits, space . _ - ; it must start with a letter or digit")
		return false
	}
	taken, err := p.acc.NameTaken(r.Context(), byline)
	if err != nil {
		p.h.Log().Error("check nickname", "err", err)
		sdk.WriteError(w, http.StatusServiceUnavailable, "cannot check the nickname right now, try again in a moment")
		return false
	}
	if taken {
		sdk.WriteError(w, http.StatusBadRequest, "this nickname matches a login name; pick another one")
		return false
	}
	return true
}

// publicByline is the nickname to publish with item, or "". Posts only, and only
// while it still differs from every login: an account created after the article
// was written must not turn the nickname into a login. Any doubt hides it.
func (p *Plugin) publicByline(r *http.Request, it *itemView) string {
	if it.Kind != "post" || it.Byline == "" {
		return ""
	}
	if taken, err := p.acc.NameTaken(r.Context(), it.Byline); err != nil || taken {
		return ""
	}
	return it.Byline
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
	if !p.checkByline(w, r, in.Byline) || !p.checkNewspaper(w, r, pr, &in.itemIn, nil) {
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
	lead, og := derive(html)
	now := p.now().Unix()
	var pub any
	if in.Status == "published" {
		pub = now
	}
	insert := func(slug string) (int64, error) {
		defer p.version.Add(1)
		res, err := p.db.ExecContext(r.Context(),
			`INSERT INTO content_items(kind, slug, title, body_md, body_html, lead, og_image, derived, status, author_id, author_name, byline, created_at, updated_at, published_at,
			                          rubric_id, dek, cover_media, cover_caption, placement)
			 VALUES (?,?,?,?,?,?,?,1,?,?,?,?,?,?,?,?,?,?,?,?)`,
			in.Kind, slug, in.Title, in.BodyMD, html, lead, og, in.Status, pr.UserID, pr.Username, in.Byline, now, now, pub,
			in.RubricID, in.Dek, in.CoverMedia, in.CoverCaption, in.Placement)
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
	var in updateIn
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
	if !p.checkByline(w, r, in.Byline) || !p.checkNewspaper(w, r, pr, &in.itemIn, cur) {
		return
	}
	// Without the version we could not tell a stale editor from a current one,
	// and a client that leaves it out would silently overwrite newer text.
	if in.Version < 1 {
		sdk.WriteError(w, http.StatusPreconditionRequired, "version is required: send the version you loaded")
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
	if cur.Version != in.Version { // cheap early exit, before rendering
		replyToLostUpdate(w, cur)
		return
	}
	html, err := renderMarkdown(in.BodyMD)
	if err != nil {
		p.internalErr(w, "render markdown", err)
		return
	}
	lead, og := derive(html)
	now := p.now().Unix()
	var pub any // keeps the first publication time across edits
	if in.Status == "published" {
		if cur.PublishedAt != nil {
			pub = *cur.PublishedAt
		} else {
			pub = now
		}
	}
	// The version test and the write are ONE statement: two requests carrying the
	// same version cannot both pass, whatever happens between load and here.
	res, err := p.db.ExecContext(r.Context(),
		`UPDATE content_items SET slug=?, title=?, body_md=?, body_html=?, lead=?, og_image=?, derived=1, status=?, byline=?, updated_at=?, published_at=?, version=version+1,
		        rubric_id=?, dek=?, cover_media=?, cover_caption=?, placement=?
		 WHERE id=? AND version=?`,
		slug, in.Title, in.BodyMD, html, lead, og, in.Status, in.Byline, now, pub,
		in.RubricID, in.Dek, in.CoverMedia, in.CoverCaption, in.Placement, id, in.Version)
	p.version.Add(1) // before answering, so the next request cannot see a stale document
	if sdk.IsUniqueViolation(err) {
		sdk.WriteError(w, http.StatusConflict, "slug already in use")
		return
	}
	if err != nil {
		p.internalErr(w, "update item", err)
		return
	}
	if n, err := res.RowsAffected(); err != nil {
		p.internalErr(w, "update item", err)
		return
	} else if n == 0 {
		// Someone saved or deleted the item after we loaded it a moment ago.
		now, err := p.load(r, id)
		if err != nil {
			p.internalErr(w, "reload item", err)
			return
		}
		replyToLostUpdate(w, now)
		return
	}
	it, err := p.load(r, id)
	if err != nil || it == nil {
		p.internalErr(w, "load updated item", err)
		return
	}
	sdk.WriteJSON(w, http.StatusOK, it)
}

// replyToLostUpdate answers an update that must not be applied: the item changed
// since the editor loaded it (412), or no longer exists (404). 409 stays reserved
// for a slug that is already taken, so clients can tell the two apart.
//
// cur is the item as it is now; nil means it was deleted in the meantime.
func replyToLostUpdate(w http.ResponseWriter, cur *itemView) {
	if cur == nil {
		sdk.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	sdk.WriteError(w, http.StatusPreconditionFailed, "this item was changed by someone else after you opened it")
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
	_, err = p.db.ExecContext(r.Context(), `DELETE FROM content_items WHERE id = ?`, id)
	p.version.Add(1)
	if err != nil {
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
