// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"oblivion/sdk"
)

const (
	setupTokenTTL = time.Hour
	maxBodyBytes  = 16 << 10
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{3,32}$`)

type userView struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	CreatedAt int64  `json:"created_at"`
}

func (p *Plugin) registerRoutes() error {
	routes := []struct {
		pattern string
		fn      http.HandlerFunc
	}{
		{"GET /setup", p.handleSetupStatus},
		{"POST /setup", p.handleSetup},
		{"POST /login", p.handleLogin},
		{"POST /logout", p.handleLogout},
		{"GET /me", p.handleMe},
		{"POST /password", p.handlePassword},
		{"GET /users", p.handleListUsers},
		{"POST /users", p.handleCreateUser},
		{"PATCH /users/{id}", p.handleUpdateUser},
		{"DELETE /users/{id}", p.handleDeleteUser},
	}
	for _, rt := range routes {
		if err := p.h.HandleAPI(rt.pattern, rt.fn); err != nil {
			return err
		}
	}
	return nil
}

// ---- helpers ---------------------------------------------------------------

var (
	writeJSON = sdk.WriteJSON
	writeErr  = sdk.WriteError
)

func (p *Plugin) internalErr(w http.ResponseWriter, what string, err error) {
	p.h.Log().Error(what, "err", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return sdk.DecodeJSON(w, r, v, maxBodyBytes)
}

func tooMany(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(d/time.Second)+1))
	writeErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
}

func (p *Plugin) clientIP(r *http.Request) string {
	if p.rt.ClientIP != nil {
		return p.rt.ClientIP(r)
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func ipKey(ip string) string { return "ip:" + limitKey(ip) }

func userKey(name string) string {
	name = strings.ToLower(name)
	if len(name) > 64 {
		name = name[:64]
	}
	return "u:" + name
}

// startSession creates a session for userID, sets the cookie and returns the CSRF token.
func (p *Plugin) startSession(w http.ResponseWriter, r *http.Request, userID int64) (string, error) {
	tok, csrf, exp, err := p.newSession(r, userID)
	if err != nil {
		return "", err
	}
	p.setCookie(w, tok, exp)
	return csrf, nil
}

type dbUser struct {
	id       int64
	username string
	hash     string
	role     string
	disabled bool
}

func (p *Plugin) findUser(r *http.Request, username string) (*dbUser, error) {
	u := &dbUser{}
	var dis int
	err := p.db.QueryRowContext(r.Context(),
		`SELECT id, username, password_hash, role, disabled FROM auth_users WHERE username = ?`, username).
		Scan(&u.id, &u.username, &u.hash, &u.role, &dis)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	u.disabled = dis == 1
	return u, err
}

func isUnique(err error) bool { return err != nil && strings.Contains(err.Error(), "UNIQUE") }

// ---- setup -----------------------------------------------------------------

// issueSetupToken creates a fresh one-time token and logs it for the operator.
func (p *Plugin) issueSetupToken() error {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	p.mu.Lock()
	p.setupToken, p.setupExpires = tok, p.now().Add(setupTokenTTL)
	p.mu.Unlock()
	p.h.Log().Warn("no users exist: create the first administrator with this one-time setup token",
		"setup_token", tok, "valid_for", setupTokenTTL.String())
	return nil
}

// handleSetupStatus tells the admin UI whether to show the first-run screen.
func (p *Plugin) handleSetupStatus(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	have := p.setupToken != ""
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"available": have})
}

func (p *Plugin) handleSetup(w http.ResponseWriter, r *http.Request) {
	if err := checkOrigin(r); err != nil {
		writeErr(w, http.StatusForbidden, "cross-site request")
		return
	}
	p.mu.Lock()
	have := p.setupToken != ""
	p.mu.Unlock()
	if !have {
		writeErr(w, http.StatusNotFound, "setup is not available")
		return
	}
	var in struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	ik := ipKey(p.clientIP(r))
	if d := p.ipLimit.check(ik); d > 0 {
		tooMany(w, d)
		return
	}

	p.mu.Lock()
	expired := p.now().After(p.setupExpires)
	match := !expired && subtle.ConstantTimeCompare([]byte(in.Token), []byte(p.setupToken)) == 1
	p.mu.Unlock()
	if expired {
		if err := p.issueSetupToken(); err != nil {
			p.internalErr(w, "issue setup token", err)
			return
		}
		writeErr(w, http.StatusUnauthorized, "setup token expired; a new one was written to the server log")
		return
	}
	if !match {
		p.ipLimit.fail(ik)
		writeErr(w, http.StatusUnauthorized, "invalid setup token")
		return
	}
	if !usernameRe.MatchString(in.Username) {
		writeErr(w, http.StatusBadRequest, "username must be 3-32 characters: letters, digits, . _ -")
		return
	}
	if err := validatePassword(in.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		p.internalErr(w, "hash password", err)
		return
	}
	// Atomic: succeeds only while the table is empty, so two racing setups
	// cannot both create an administrator.
	res, err := p.db.ExecContext(r.Context(),
		`INSERT INTO auth_users(username, password_hash, role, created_at)
		 SELECT ?, ?, 'admin', ? WHERE NOT EXISTS (SELECT 1 FROM auth_users)`,
		in.Username, hash, p.now().Unix())
	if err != nil {
		p.internalErr(w, "create first admin", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusConflict, "setup already completed")
		return
	}
	p.mu.Lock()
	p.setupToken = ""
	p.mu.Unlock()
	id, _ := res.LastInsertId()
	p.emit(r.Context(), "auth.user_created", map[string]any{"user_id": id, "username": in.Username, "role": "admin"})
	csrf, err := p.startSession(w, r, id)
	if err != nil {
		p.internalErr(w, "start session", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user": userView{ID: id, Username: in.Username, Role: "admin", CreatedAt: p.now().Unix()},
		"csrf": csrf,
	})
}

// ---- login / logout / me ---------------------------------------------------

func (p *Plugin) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := checkOrigin(r); err != nil {
		writeErr(w, http.StatusForbidden, "cross-site request")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	ik, uk := ipKey(p.clientIP(r)), userKey(in.Username)
	if d := max(p.ipLimit.check(ik), p.userLimit.check(uk)); d > 0 {
		tooMany(w, d)
		return
	}
	u, err := p.findUser(r, in.Username)
	if err != nil {
		p.internalErr(w, "find user", err)
		return
	}
	// Always burn one verification so unknown users cost the same as known ones.
	hash := p.dummyHash
	if u != nil {
		hash = u.hash
	}
	ok := false
	if len(in.Password) <= maxPasswordLen {
		if ok, err = verifyPassword(in.Password, hash); err != nil {
			p.internalErr(w, "verify password", err)
			return
		}
	}
	if u == nil || !ok || u.disabled {
		p.ipLimit.fail(ik)
		p.userLimit.fail(uk)
		p.emit(r.Context(), "auth.login_failed", map[string]any{"username": in.Username})
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	// The per-IP counter is deliberately NOT reset on success: otherwise an
	// attacker with their own account could reset it between guesses.
	p.userLimit.reset(uk)
	csrf, err := p.startSession(w, r, u.id)
	if err != nil {
		p.internalErr(w, "start session", err)
		return
	}
	p.emit(r.Context(), "auth.login_success", map[string]any{"user_id": u.id, "username": u.username})
	writeJSON(w, http.StatusOK, map[string]any{
		"user": userView{ID: u.id, Username: u.username, Role: u.role},
		"csrf": csrf,
	})
}

func (p *Plugin) handleLogout(w http.ResponseWriter, r *http.Request) {
	s, err := p.lookup(r)
	if err != nil {
		p.internalErr(w, "lookup session", err)
		return
	}
	if s != nil {
		if _, err := p.db.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE token_hash = ?`, s.tokenHash); err != nil {
			p.internalErr(w, "delete session", err)
			return
		}
	}
	p.setCookie(w, "", time.Time{})
	w.WriteHeader(http.StatusNoContent)
}

func (p *Plugin) handleMe(w http.ResponseWriter, r *http.Request) {
	s, err := p.lookup(r)
	if err != nil {
		p.internalErr(w, "lookup session", err)
		return
	}
	if s == nil {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	pr := s.principal
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{"id": pr.UserID, "username": pr.Username, "role": pr.Roles[0], "perms": pr.Perms},
		"csrf": s.csrf,
	})
}

func (p *Plugin) handlePassword(w http.ResponseWriter, r *http.Request) {
	s, err := p.lookup(r)
	if err != nil {
		p.internalErr(w, "lookup session", err)
		return
	}
	if s == nil {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decode(w, r, &in) {
		return
	}
	ik, uk := ipKey(p.clientIP(r)), userKey(s.principal.Username)
	if d := max(p.ipLimit.check(ik), p.userLimit.check(uk)); d > 0 {
		tooMany(w, d)
		return
	}
	var hash string
	if err := p.db.QueryRowContext(r.Context(), `SELECT password_hash FROM auth_users WHERE id = ?`, s.principal.UserID).Scan(&hash); err != nil {
		p.internalErr(w, "load hash", err)
		return
	}
	ok := false
	if len(in.Current) <= maxPasswordLen {
		if ok, err = verifyPassword(in.Current, hash); err != nil {
			p.internalErr(w, "verify password", err)
			return
		}
	}
	if !ok {
		p.ipLimit.fail(ik)
		p.userLimit.fail(uk)
		writeErr(w, http.StatusForbidden, "current password is incorrect")
		return
	}
	if err := validatePassword(in.New); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	nh, err := hashPassword(in.New)
	if err != nil {
		p.internalErr(w, "hash password", err)
		return
	}
	if _, err := p.db.ExecContext(r.Context(), `UPDATE auth_users SET password_hash = ? WHERE id = ?`, nh, s.principal.UserID); err != nil {
		p.internalErr(w, "update password", err)
		return
	}
	// Every other device is logged out; this one stays.
	if err := p.revokeSessions(r.Context(), s.principal.UserID, s.tokenHash); err != nil {
		p.internalErr(w, "revoke sessions", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- user management -------------------------------------------------------

// requireManage answers 401/403 and returns false unless the caller may manage users.
func requireManage(w http.ResponseWriter, r *http.Request) bool {
	pr := principalOf(r)
	if pr == nil {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return false
	}
	if !pr.Can(permManageUsers) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}

func (p *Plugin) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !requireManage(w, r) {
		return
	}
	rows, err := p.db.QueryContext(r.Context(),
		`SELECT id, username, role, disabled, created_at FROM auth_users ORDER BY id LIMIT 1000`)
	if err != nil {
		p.internalErr(w, "list users", err)
		return
	}
	defer rows.Close()
	out := []userView{}
	for rows.Next() {
		var u userView
		var dis int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &dis, &u.CreatedAt); err != nil {
			p.internalErr(w, "scan user", err)
			return
		}
		u.Disabled = dis == 1
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		p.internalErr(w, "list users", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Plugin) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !requireManage(w, r) {
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !usernameRe.MatchString(in.Username) {
		writeErr(w, http.StatusBadRequest, "username must be 3-32 characters: letters, digits, . _ -")
		return
	}
	if !validRole(in.Role) {
		writeErr(w, http.StatusBadRequest, "unknown role")
		return
	}
	if err := validatePassword(in.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		p.internalErr(w, "hash password", err)
		return
	}
	now := p.now().Unix()
	res, err := p.db.ExecContext(r.Context(),
		`INSERT INTO auth_users(username, password_hash, role, created_at) VALUES (?,?,?,?)`,
		in.Username, hash, in.Role, now)
	if isUnique(err) {
		writeErr(w, http.StatusConflict, "username already taken")
		return
	}
	if err != nil {
		p.internalErr(w, "create user", err)
		return
	}
	id, _ := res.LastInsertId()
	p.emit(r.Context(), "auth.user_created", map[string]any{"user_id": id, "username": in.Username, "role": in.Role})
	writeJSON(w, http.StatusCreated, userView{ID: id, Username: in.Username, Role: in.Role, CreatedAt: now})
}

func userID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid user id")
		return 0, false
	}
	return id, true
}

const errLastAdmin = "cannot remove, disable or demote the last active administrator"

func (p *Plugin) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !requireManage(w, r) {
		return
	}
	id, ok := userID(w, r)
	if !ok {
		return
	}
	var in struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Role != nil && !validRole(*in.Role) {
		writeErr(w, http.StatusBadRequest, "unknown role")
		return
	}
	var newHash *string
	if in.Password != nil {
		if err := validatePassword(*in.Password); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		h, err := hashPassword(*in.Password)
		if err != nil {
			p.internalErr(w, "hash password", err)
			return
		}
		newHash = &h
	}
	var exists int
	if err := p.db.QueryRowContext(r.Context(), `SELECT 1 FROM auth_users WHERE id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		p.internalErr(w, "find user", err)
		return
	}
	var dis *int
	if in.Disabled != nil {
		v := 0
		if *in.Disabled {
			v = 1
		}
		dis = &v
	}

	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		p.internalErr(w, "begin", err)
		return
	}
	defer tx.Rollback()
	// The last-admin guard lives inside the UPDATE itself, so it is atomic even
	// when two administrators demote each other concurrently.
	res, err := tx.ExecContext(r.Context(), `
		UPDATE auth_users SET role = COALESCE(?1, role), disabled = COALESCE(?2, disabled)
		WHERE id = ?3 AND (
		      (COALESCE(?1, role) = 'admin' AND COALESCE(?2, disabled) = 0)
		   OR role <> 'admin' OR disabled = 1
		   OR EXISTS (SELECT 1 FROM auth_users o WHERE o.id <> ?3 AND o.role = 'admin' AND o.disabled = 0))`,
		in.Role, dis, id)
	if err != nil {
		p.internalErr(w, "update user", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusConflict, errLastAdmin)
		return
	}
	if newHash != nil {
		if _, err := tx.ExecContext(r.Context(), `UPDATE auth_users SET password_hash = ? WHERE id = ?`, *newHash, id); err != nil {
			p.internalErr(w, "update password", err)
			return
		}
	}
	// Disabling, demoting or resetting a password ends the user's sessions.
	if newHash != nil || in.Role != nil || (dis != nil && *dis == 1) {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE user_id = ?`, id); err != nil {
			p.internalErr(w, "revoke sessions", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		p.internalErr(w, "commit", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Plugin) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !requireManage(w, r) {
		return
	}
	id, ok := userID(w, r)
	if !ok {
		return
	}
	res, err := p.db.ExecContext(r.Context(), `
		DELETE FROM auth_users WHERE id = ?1 AND (
		     role <> 'admin' OR disabled = 1
		  OR EXISTS (SELECT 1 FROM auth_users o WHERE o.id <> ?1 AND o.role = 'admin' AND o.disabled = 0))`, id)
	if err != nil {
		p.internalErr(w, "delete user", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := p.db.QueryRowContext(r.Context(), `SELECT 1 FROM auth_users WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		writeErr(w, http.StatusConflict, errLastAdmin)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
