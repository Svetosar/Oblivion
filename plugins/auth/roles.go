// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import "oblivion/sdk"

// Fixed roles for the beta. Permission strings are owned by the plugins that
// check them (e.g. the content plugin checks "content.write").
var rolePerms = map[string][]string{
	"admin":  {"*"},
	"editor": {"content.read", "content.write", "content.publish", "media.read", "media.write", "media.manage"},
	"author": {"content.read", "content.write.own", "media.read", "media.write"},
}

func validRole(r string) bool { _, ok := rolePerms[r]; return ok }

func principalFor(id int64, username, role string) *sdk.Principal {
	perms := map[string]bool{}
	for _, p := range rolePerms[role] {
		perms[p] = true
	}
	return &sdk.Principal{UserID: id, Username: username, Roles: []string{role}, Perms: perms}
}

const permManageUsers = "users.manage"
