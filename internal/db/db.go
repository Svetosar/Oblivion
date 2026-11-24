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
	if err := migrate(ctx, d, migrationsFS); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// migrate applies every not-yet-applied migrations/NNNN_name.sql in order,
// each in its own transaction, and records it in schema_migrations.
func migrate(ctx context.Context, d *sql.DB, fsys fs.FS) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
	) STRICT`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
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
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, name).Scan(&n); err != nil {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
