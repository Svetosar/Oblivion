// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"oblivion/sdk"
)

// Account management outside the HTTP server: the "user" subcommands of the
// binary. They run against the same database as the server (SQLite in WAL mode
// shares it between processes) and use the same rules: password policy, argon2id,
// at least one active administrator, sessions ended on a password reset.
//
// This is operator access: whoever can run it already controls the server.

var (
	ErrNoSuchUser    = errors.New("no such user")
	ErrLastAdmin     = errors.New(errLastAdmin)
	ErrUsernameTaken = errors.New("username already taken")
)

// UserInfo is one account as shown to the operator.
type UserInfo struct {
	ID        int64
	Username  string
	Role      string
	Disabled  bool
	CreatedAt int64
}

// Admin manages accounts. The schema must exist: run the plugin's migrations
// (plugins.MigratePlugin) first.
type Admin struct {
	db  *sql.DB
	now func() time.Time
}

func NewAdmin(d *sql.DB) *Admin { return &Admin{db: d, now: time.Now} }

func (a *Admin) clock() time.Time {
	if a.now == nil {
		return time.Now()
	}
	return a.now()
}

func (a *Admin) List(ctx context.Context) ([]UserInfo, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, username, role, disabled, created_at FROM auth_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserInfo
	for rows.Next() {
		var u UserInfo
		var dis int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &dis, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Disabled = dis == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// Create adds an account. The same rules as the HTTP API apply.
func (a *Admin) Create(ctx context.Context, username, password, role string) error {
	if !usernameRe.MatchString(username) {
		return errors.New("username must be 3-32 characters: Latin letters, digits, . _ -")
	}
	if !validRole(role) {
		return errors.New("unknown role (admin, editor or author)")
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(ctx, password)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO auth_users(username, password_hash, role, created_at) VALUES (?,?,?,?)`,
		username, hash, role, a.clock().Unix())
	if sdk.IsUniqueViolation(err) {
		return ErrUsernameTaken
	}
	return err
}

func (a *Admin) id(ctx context.Context, username string) (int64, error) {
	var id int64
	err := a.db.QueryRowContext(ctx, `SELECT id FROM auth_users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoSuchUser
	}
	return id, err
}

// ResetPassword sets a new password and ends every session of the account.
func (a *Admin) ResetPassword(ctx context.Context, username, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	id, err := a.id(ctx, username)
	if err != nil {
		return err
	}
	hash, err := hashPassword(ctx, password)
	if err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE auth_users SET password_hash = ? WHERE id = ?`, hash, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetDisabled blocks or unblocks an account. The last active administrator
// cannot be disabled. Blocking ends the account's sessions.
func (a *Admin) SetDisabled(ctx context.Context, username string, disabled bool) error {
	id, err := a.id(ctx, username)
	if err != nil {
		return err
	}
	dis := 0
	if disabled {
		dis = 1
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, guardedUpdateSQL, nil, dis, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLastAdmin
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
