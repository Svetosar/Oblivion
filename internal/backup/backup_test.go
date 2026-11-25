// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"oblivion/internal/db"
)

// liveData builds a data directory the way a running server has it: the database
// stays open (recent rows live in the -wal file) and one plugin owns a file.
func liveData(t *testing.T) (dataDir, dbPath string) {
	t.Helper()
	dataDir = t.TempDir()
	dbPath = filepath.Join(dataDir, "site.db")
	d, err := db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE note (body TEXT); INSERT INTO note VALUES ('fresh row')`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(dataDir, "plugins", "media")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abc.jpg"), []byte("image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dataDir, dbPath
}

// extract unpacks an archive into a new directory and returns it with the entry names.
func extract(t *testing.T, archive []byte) (string, []string) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var names []string
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if !filepath.IsLocal(h.Name) {
			t.Fatalf("unsafe entry name %q", h.Name)
		}
		path := filepath.Join(out, h.Name)
		if h.Typeflag == tar.TypeDir {
			if h.Mode != 0o700 {
				t.Errorf("%s: directory mode %o, want 700", h.Name, h.Mode)
			}
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if h.Mode != 0o600 {
			t.Errorf("%s: file mode %o, want 600", h.Name, h.Mode)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Reading to the end makes gzip verify its checksum: a truncated archive fails here.
	if _, err := io.Copy(io.Discard, zr); err != nil {
		t.Fatal(err)
	}
	return out, names
}

func TestWriteRestoresDatabaseAndFiles(t *testing.T) {
	dataDir, dbPath := liveData(t)
	var buf bytes.Buffer
	if err := Write(context.Background(), &buf, dataDir, dbPath); err != nil {
		t.Fatal(err)
	}

	restored, names := extract(t, buf.Bytes())
	want := []string{"site.db", "plugins/", "plugins/media/", "plugins/media/abc.jpg"}
	if !slices.Equal(names, want) {
		t.Fatalf("entries %q, want %q", names, want)
	}
	d, err := db.Open(context.Background(), filepath.Join(restored, "site.db"))
	if err != nil {
		t.Fatalf("restored database does not open: %v", err)
	}
	defer d.Close()
	var body string
	if err := d.QueryRow(`SELECT body FROM note`).Scan(&body); err != nil || body != "fresh row" {
		t.Fatalf("restored row %q, err %v", body, err)
	}
	img, err := os.ReadFile(filepath.Join(restored, "plugins", "media", "abc.jpg"))
	if err != nil || string(img) != "image bytes" {
		t.Fatalf("restored file %q, err %v", img, err)
	}
	assertNoLeftovers(t, dataDir)
}

// The temporary snapshot holds password hashes: it must never stay behind.
func assertNoLeftovers(t *testing.T, dataDir string) {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "plugins" && !slices.Contains([]string{"site.db", "site.db-wal", "site.db-shm"}, e.Name()) {
			t.Errorf("left behind in the data directory: %s", e.Name())
		}
	}
}

func TestWriteWithoutDatabaseFailsAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "site.db")
	if err := Write(context.Background(), io.Discard, dir, dbPath); err == nil {
		t.Fatal("backup of a missing database succeeded")
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a database appeared: %v", err)
	}
}

// A symlink could point anywhere; silently skipping it would give an incomplete backup.
func TestWriteRefusesSymlinks(t *testing.T) {
	dataDir, dbPath := liveData(t)
	if err := os.Symlink("/etc/hostname", filepath.Join(dataDir, "plugins", "media", "link")); err != nil {
		t.Fatal(err)
	}
	if err := Write(context.Background(), io.Discard, dataDir, dbPath); err == nil {
		t.Fatal("symlink accepted")
	}
	assertNoLeftovers(t, dataDir)
}

func TestToFile(t *testing.T) {
	dataDir, dbPath := liveData(t)
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := ToFile(context.Background(), out, dataDir, dbPath); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, err %v", st.Mode(), err)
	}
	first, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	extract(t, first)

	// An existing file is never overwritten, and a refusal must not delete it.
	if err := ToFile(context.Background(), out, dataDir, dbPath); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("overwrite: err %v, want ErrExist", err)
	}
	if now, err := os.ReadFile(out); err != nil || !bytes.Equal(now, first) {
		t.Fatalf("existing backup changed: %v", err)
	}
}

func TestToFileRemovesPartialOutput(t *testing.T) {
	dataDir, dbPath := liveData(t)
	if err := os.Symlink("/etc/hostname", filepath.Join(dataDir, "plugins", "media", "link")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := ToFile(context.Background(), out, dataDir, dbPath); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial backup left on disk: %v", err)
	}
}
