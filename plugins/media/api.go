// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif" // register decoders for image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	_ "golang.org/x/image/webp"

	"oblivion/sdk"
)

type itemView struct {
	ID         int64  `json:"id"`
	URL        string `json:"url"`
	Filename   string `json:"filename"`
	Mime       string `json:"mime"`
	Size       int64  `json:"size"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	UploaderID int64  `json:"uploader_id"`
	CreatedAt  int64  `json:"created_at"`
}

const cols = `id, stored_name, filename, mime, size, width, height, uploader_id, created_at`

func scan(s interface{ Scan(...any) error }) (itemView, string, error) {
	var it itemView
	var name string
	err := s.Scan(&it.ID, &name, &it.Filename, &it.Mime, &it.Size, &it.Width, &it.Height, &it.UploaderID, &it.CreatedAt)
	it.URL = "/media/" + name
	return it, name, err
}

func (p *Plugin) internalErr(w http.ResponseWriter, what string, err error) {
	p.h.Log().Error(what, "err", err)
	sdk.WriteError(w, http.StatusInternalServerError, "internal error")
}

// need returns the principal if it holds perm; otherwise it answers 401/403.
func need(w http.ResponseWriter, r *http.Request, perm string) *sdk.Principal {
	pr := sdk.PrincipalFrom(r.Context())
	if pr == nil {
		sdk.WriteError(w, http.StatusUnauthorized, "not authenticated")
		return nil
	}
	if !pr.Can(perm) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return nil
	}
	return pr
}

// cleanFilename keeps a display-only name: base name, no control characters, bounded.
func cleanFilename(s string) string {
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100])
	}
	if s = strings.TrimSpace(s); s == "" {
		return "image"
	}
	return s
}

func (p *Plugin) handleUpload(w http.ResponseWriter, r *http.Request) {
	pr := need(w, r, permWrite)
	if pr == nil {
		return
	}
	// The server-wide ReadTimeout would cut off slow uploads of big photos, and
	// WriteTimeout, which counts from the request headers, would cut off the
	// answer once the body has finally arrived (the file is stored, the client
	// sees a broken connection and uploads it again). Extend both.
	// Not supported by every ResponseWriter (tests); then the defaults stay.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(p.now().Add(uploadDeadline))
	_ = rc.SetWriteDeadline(p.now().Add(uploadDeadline + writeGrace))
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-r.Context().Done():
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		sdk.WriteError(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	var data []byte
	var filename string
	for found := false; !found; {
		part, err := mr.NextPart()
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				sdk.WriteError(w, http.StatusRequestEntityTooLarge, "upload too large")
			} else {
				sdk.WriteError(w, http.StatusBadRequest, "missing file field")
			}
			return
		}
		if part.FormName() != "file" {
			continue
		}
		filename = part.FileName()
		if data, err = io.ReadAll(io.LimitReader(part, maxUpload+1)); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				sdk.WriteError(w, http.StatusRequestEntityTooLarge, "upload too large")
			} else {
				sdk.WriteError(w, http.StatusBadRequest, "could not read upload")
			}
			return
		}
		found = true
	}
	if len(data) > maxUpload {
		sdk.WriteError(w, http.StatusRequestEntityTooLarge, "image larger than 10 MiB")
		return
	}
	kind, ok := sniff(data)
	if !ok {
		sdk.WriteError(w, http.StatusUnsupportedMediaType, "only JPEG, PNG, GIF and WebP images are accepted")
		return
	}
	clean, err := strip(kind, data)
	if err != nil {
		sdk.WriteError(w, http.StatusBadRequest, "invalid or corrupt image")
		return
	}
	cfg, _, err := image.DecodeConfig(bytesReader(clean))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		sdk.WriteError(w, http.StatusBadRequest, "invalid image or too many pixels")
		return
	}

	var used int64
	if err := p.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(size), 0) FROM media_items WHERE uploader_id = ?`, pr.UserID).Scan(&used); err != nil {
		p.internalErr(w, "quota query", err)
		return
	}
	if used+int64(len(clean)) > p.quota {
		sdk.WriteError(w, http.StatusRequestEntityTooLarge, "storage quota exceeded: delete some images first")
		return
	}

	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		p.internalErr(w, "random name", err)
		return
	}
	name := hex.EncodeToString(rnd[:]) + kind.ext
	f, err := p.files.Create(name)
	if err != nil {
		p.internalErr(w, "create file", err)
		return
	}
	_, werr := f.Write(clean)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = p.files.Remove(name)
		p.internalErr(w, "write file", werr)
		return
	}
	now := p.now().Unix()
	res, err := p.db.ExecContext(r.Context(),
		`INSERT INTO media_items(stored_name, filename, mime, size, width, height, uploader_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		name, cleanFilename(filename), kind.mime, len(clean), cfg.Width, cfg.Height, pr.UserID, now)
	if err != nil {
		_ = p.files.Remove(name)
		p.internalErr(w, "insert media", err)
		return
	}
	id, _ := res.LastInsertId()
	it, _, err := scan(p.db.QueryRowContext(r.Context(), `SELECT `+cols+` FROM media_items WHERE id = ?`, id))
	if err != nil {
		p.internalErr(w, "reload media", err)
		return
	}
	sdk.WriteJSON(w, http.StatusCreated, it)
}

func (p *Plugin) handleList(w http.ResponseWriter, r *http.Request) {
	if need(w, r, permRead) == nil {
		return
	}
	limit, offset := 50, 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, 200)
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	rows, err := p.db.QueryContext(r.Context(),
		`SELECT `+cols+` FROM media_items ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		p.internalErr(w, "list media", err)
		return
	}
	defer rows.Close()
	out := []itemView{}
	for rows.Next() {
		it, _, err := scan(rows)
		if err != nil {
			p.internalErr(w, "scan media", err)
			return
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		p.internalErr(w, "list media", err)
		return
	}
	sdk.WriteJSON(w, http.StatusOK, out)
}

func (p *Plugin) handleDelete(w http.ResponseWriter, r *http.Request) {
	pr := need(w, r, permWrite)
	if pr == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		sdk.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	it, name, err := scan(p.db.QueryRowContext(r.Context(), `SELECT `+cols+` FROM media_items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		sdk.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		p.internalErr(w, "load media", err)
		return
	}
	if it.UploaderID != pr.UserID && !pr.Can(permManage) {
		sdk.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	// Row first: a leftover file is harmless, a row pointing at nothing is not.
	if _, err := p.db.ExecContext(r.Context(), `DELETE FROM media_items WHERE id = ?`, id); err != nil {
		p.internalErr(w, "delete media", err)
		return
	}
	if err := p.files.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		p.h.Log().Error("remove media file", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

var servedName = regexp.MustCompile(`^[0-9a-f]{32}\.(jpg|png|gif|webp)$`)

var mimeByExt = map[string]string{".jpg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp"}

// handleServe serves stored files. The name is random and unguessable, files
// are immutable, so they can be cached forever.
func (p *Plugin) handleServe(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !servedName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	f, err := p.files.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", mimeByExt[name[strings.LastIndexByte(name, '.'):]])
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("Content-Disposition", "inline")
	http.ServeContent(w, r, name, st.ModTime(), f)
}
