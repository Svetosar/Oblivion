// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "t.db")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("migrations not recorded: n=%d err=%v", n, err)
	}
	var fk int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys=%d err=%v", fk, err)
	}
	d.Close()

	// Reopen: must not re-apply (kernel_meta would fail with "already exists").
	d2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d2.Close()
	var n2 int
	_ = d2.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n2)
	if n2 != n {
		t.Fatalf("migration count changed %d -> %d", n, n2)
	}
}
