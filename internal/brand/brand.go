// SPDX-License-Identifier: AGPL-3.0-or-later

// Package brand holds the product name in one place. "oblivion" is only a
// codename; keep every user-visible or externally-visible name derived from here
// so a rename is a one-line change.
package brand

const (
	// Name is the human-readable product name.
	Name = "Oblivion"
	// Slug is the machine-readable name (binary, env prefix in lower case, cookie prefix).
	Slug = "oblivion"
	// EnvPrefix is the prefix of every configuration environment variable.
	EnvPrefix = "OBLIVION_"
)
