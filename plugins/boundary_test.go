// SPDX-License-Identifier: AGPL-3.0-or-later

package plugins_test

import (
	"os/exec"
	"strings"
	"testing"
)

// Official plugins may depend on the public SDK only, never on kernel
// internals: they must prove that the plugin API is sufficient.
func TestOfficialPluginsUseOnlySDK(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./...").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "oblivion/internal/") || p == "oblivion/cmd/oblivion" {
			t.Errorf("official plugin depends on kernel package %s", p)
		}
	}
}
