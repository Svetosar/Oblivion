// SPDX-License-Identifier: AGPL-3.0-or-later

package plugins

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"oblivion/sdk"
)

func TestFilesConfinement(t *testing.T) {
	m := newMgr(t)
	dir := t.TempDir()
	m.SetDataDir(dir)
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	var files sdk.Files
	mfst := mf("fs", sdk.CapFiles)
	m.Register(&fake{mf: mfst, init: func(_ context.Context, h sdk.Host) (err error) {
		files, err = h.Files()
		return err
	}})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	w, err := files.Create("a.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("hello"))
	_ = w.Close()
	if _, err := files.Create("a.bin"); err == nil {
		t.Fatal("Create must not overwrite an existing file")
	}
	f, err := files.Open("a.bin")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(f); string(b) != "hello" {
		t.Fatalf("read %q", b)
	}
	f.Close()
	if fi, _ := os.Stat(filepath.Join(dir, "plugins", "fs", "a.bin")); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file missing or not private: %v", fi)
	}

	for _, name := range []string{"../secret.txt", "..", "/etc/passwd", "a/b", `a\b`, ".hidden", "", "x\x00y"} {
		if _, err := files.Open(name); err == nil {
			t.Errorf("Open(%q) succeeded", name)
		}
		if w, err := files.Create(name); err == nil {
			w.Close()
			t.Errorf("Create(%q) succeeded", name)
		}
		if err := files.Remove(name); err == nil {
			t.Errorf("Remove(%q) succeeded", name)
		}
	}
	// A symlink planted inside the plugin directory must not lead out of it.
	if err := os.Symlink(secret, filepath.Join(dir, "plugins", "fs", "link")); err != nil {
		t.Fatal(err)
	}
	if f, err := files.Open("link"); err == nil {
		f.Close()
		t.Fatal("followed a symlink out of the plugin directory")
	}
	if b, _ := os.ReadFile(secret); string(b) != "top secret" {
		t.Fatal("secret modified")
	}
	if err := files.Remove("a.bin"); err != nil {
		t.Fatal(err)
	}
}

func TestFilesRequireCapabilityAndDataDir(t *testing.T) {
	m := newMgr(t)
	m.SetDataDir(t.TempDir())
	var capErr error
	m.Register(&fake{mf: mf("nofs"), init: func(_ context.Context, h sdk.Host) error {
		_, capErr = h.Files()
		return nil
	}})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(capErr, sdk.ErrCapabilityDenied) {
		t.Fatalf("got %v", capErr)
	}
	if _, err := openFiles("", "x"); err == nil {
		t.Fatal("empty data dir accepted")
	}
}

func TestBodyLimit(t *testing.T) {
	m := newMgr(t)
	big := mf("big")
	big.MaxBodyBytes = 5 << 20
	m.Register(&fake{mf: big})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	const def = 1 << 20
	for path, want := range map[string]int64{
		"/api/v1/big/items":    5 << 20,
		"/api/v1/big":          5 << 20,
		"/api/v1/other/items":  def,
		"/api/v1/bigger/items": def, // id must match exactly, not by prefix
		"/posts/x":             def,
		"/api/v1/":             def,
	} {
		if got := m.BodyLimit(path, def); got != want {
			t.Errorf("BodyLimit(%q) = %d, want %d", path, got, want)
		}
	}
	// A manifest may not ask for an unreasonable limit.
	bad := mf("greedy")
	bad.MaxBodyBytes = MaxPluginBody + 1
	if err := validate(bad); err == nil {
		t.Fatal("excessive MaxBodyBytes accepted")
	}
	bad.MaxBodyBytes = -1
	if err := validate(bad); err == nil {
		t.Fatal("negative MaxBodyBytes accepted")
	}
}
