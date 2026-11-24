// SPDX-License-Identifier: AGPL-3.0-or-later

// Package plugins lists the official plugins compiled into the binary.
package plugins

import (
	"oblivion/plugins/auth"
	"oblivion/plugins/content"

	"oblivion/sdk"
)

// EssentialIDs are plugins without which the server refuses to start.
var EssentialIDs = []string{"auth"}

// All returns a fresh instance of every official plugin.
func All() []sdk.Plugin {
	return []sdk.Plugin{auth.New(), content.New()}
}
