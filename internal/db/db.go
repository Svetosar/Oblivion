// SPDX-License-Identifier: AGPL-3.0-or-later

// Package db opens SQLite (pure Go, no CGO) and applies embedded migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (creating if needed) the database at path with safe pragmas.
// The data directory is created with mode 0700 and the file is private.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
		"trusted_schema(0)",
	} {
		q.Add("_pragma", p)
	}
	dsn := "file:" + path + "?" + q.Encode()
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 1 CPU / 1 GB target: a handful of connections is plenty, and SQLite
	// allows a single writer anyway.
	d.SetMaxOpenConns(4)
	d.SetMaxIdleConns(4)
	if err := d.PingContext(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	if err := Migrate(ctx, d, migrationsFS, "schema_migrations"); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Snapshot writes a consistent, compacted copy of the database at path into dest,
// which must not exist yet. It is safe while a server is using the database: the
// source is opened read-only and is neither created nor migrated.
func Snapshot(ctx context.Context, path, dest string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	q := url.Values{"mode": {"ro"}}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "trusted_schema(0)")
	d, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return err
	}
	defer d.Close()
	if _, err := d.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	return nil
}

// IntegrityCheck opens the database at path read-only and runs SQLite's integrity
// check. A file that is not a database, or a damaged one, is an error.
func IntegrityCheck(ctx context.Context, path string) error {
	q := url.Values{"mode": {"ro"}}
	q.Add("_pragma", "trusted_schema(0)")
	d, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return err
	}
	defer d.Close()
	rows, err := d.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("the database is damaged: %s", strings.Join(problems, "; "))
	}
	return nil
}

var tableRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// Migrate applies every not-yet-applied migrations/NNNN_name.sql found in fsys,
// in name order, each in its own transaction, recording progress in table
// (a trusted identifier: validated, never user input).
func Migrate(ctx context.Context, d *sql.DB, fsys fs.FS, table string) error {
	if !tableRe.MatchString(table) {
		return fmt.Errorf("invalid migrations table name %q", table)
	}
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
	) STRICT`); err != nil {
		return fmt.Errorf("create %s: %w", table, err)
	}
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE version = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return err
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+`(version) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
