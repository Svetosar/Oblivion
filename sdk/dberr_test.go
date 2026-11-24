// SPDX-License-Identifier: AGPL-3.0-or-later

package sdk

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

func TestIsUniqueViolation(t *testing.T) {
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT UNIQUE, n INTEGER NOT NULL CHECK (n > 0))`,
		`INSERT INTO t(name, n) VALUES ('a', 1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_, uniq := d.Exec(`INSERT INTO t(name, n) VALUES ('a', 1)`)
	_, pk := d.Exec(`INSERT INTO t(id, name, n) VALUES (1, 'b', 1)`)
	_, notnull := d.Exec(`INSERT INTO t(name, n) VALUES ('c', NULL)`)
	_, check := d.Exec(`INSERT INTO t(name, n) VALUES ('d', 0)`)
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"unique":      {uniq, true},
		"primary key": {pk, true},
		"wrapped":     {fmt.Errorf("insert: %w", uniq), true},
		"not null":    {notnull, false},
		"check":       {check, false},
		"nil":         {nil, false},
		"other":       {errors.New("a UNIQUE word in an unrelated error"), false},
	} {
		if got := IsUniqueViolation(tc.err); got != tc.want {
			t.Errorf("%s: got %v want %v (err: %v)", name, got, tc.want, tc.err)
		}
	}
}
