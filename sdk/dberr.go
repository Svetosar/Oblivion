// SPDX-License-Identifier: AGPL-3.0-or-later

package sdk

import (
	"errors"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsUniqueViolation reports whether err is a UNIQUE (or PRIMARY KEY) constraint
// failure, so plugins can answer 409 without matching on error text.
func IsUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return true
	case sqlite3.SQLITE_CONSTRAINT: // driver reported the primary code only
		return strings.Contains(se.Error(), "UNIQUE")
	}
	return false
}
