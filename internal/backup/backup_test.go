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
	"strings"
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

// ---- Restore ----

type entry struct {
	name string
	typ  byte
	body string
	mode int64
	link string
}

// archive builds a tar.gz by hand: the archives Restore must refuse are exactly the
// ones Write never produces.
func archive(t *testing.T, entries ...entry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: e.mode, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	return bytes.NewReader(buf.Bytes())
}

func TestRestoreRebuildsADataDirectoryFromABackup(t *testing.T) {
	dataDir, dbPath := liveData(t)
	var buf bytes.Buffer
	if err := Write(context.Background(), &buf, dataDir, dbPath); err != nil {
		t.Fatal(err)
	}

	fresh := t.TempDir()
	if err := Restore(context.Background(), bytes.NewReader(buf.Bytes()), fresh, "site.db"); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(context.Background(), filepath.Join(fresh, "site.db"))
	if err != nil {
		t.Fatalf("the restored database does not open: %v", err)
	}
	defer d.Close()
	var body string
	if err := d.QueryRow(`SELECT body FROM note`).Scan(&body); err != nil || body != "fresh row" {
		t.Fatalf("restored row %q, err %v", body, err)
	}
	img, err := os.ReadFile(filepath.Join(fresh, "plugins", "media", "abc.jpg"))
	if err != nil || string(img) != "image bytes" {
		t.Fatalf("restored file %q, err %v", img, err)
	}
	st, _ := os.Stat(filepath.Join(fresh, "site.db"))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("database mode %v, want 600", st.Mode().Perm())
	}
}

// Restoring over existing data would silently destroy it.
func TestRestoreRefusesANonEmptyDirectory(t *testing.T) {
	dataDir, dbPath := liveData(t)
	var buf bytes.Buffer
	if err := Write(context.Background(), &buf, dataDir, dbPath); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "keep.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), bytes.NewReader(buf.Bytes()), other, "site.db"); err == nil {
		t.Fatal("restored into a directory that has data")
	}
	if b, _ := os.ReadFile(filepath.Join(other, "keep.txt")); string(b) != "mine" {
		t.Error("existing data was touched")
	}
}

// validDB returns the bytes of a real, healthy SQLite database.
func validDB(t *testing.T) string {
	t.Helper()
	dataDir, dbPath := liveData(t)
	var buf bytes.Buffer
	if err := Write(context.Background(), &buf, dataDir, dbPath); err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	h, err := tr.Next()
	if err != nil || h.Name != "site.db" {
		t.Fatalf("first entry %v, err %v", h, err)
	}
	b, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRestoreRefusesHostileOrBrokenArchives(t *testing.T) {
	good := validDB(t)
	dbEntry := entry{name: "site.db", body: good, mode: 0o600}
	plugins := entry{name: "plugins/", typ: tar.TypeDir, mode: 0o700}
	// Every case below has a healthy database, so only the one suspicious entry
	// can be the reason for the refusal.
	cases := map[string][]entry{
		"path traversal":                                     {dbEntry, {name: "../evil", body: "x", mode: 0o600}},
		"traversal out of plugins":                           {dbEntry, plugins, {name: "plugins/../../evil", body: "x", mode: 0o600}},
		"dot-dot inside plugins ends up beside the database": {dbEntry, plugins, {name: "plugins/../extra.txt", body: "x", mode: 0o600}},
		"absolute path":                                      {dbEntry, {name: "/etc/evil", body: "x", mode: 0o600}},
		"unknown top-level file":                             {dbEntry, {name: "other.txt", body: "x", mode: 0o600}},
		"a file where plugins belong":                        {dbEntry, {name: "plugins", body: "x", mode: 0o600}},
		"a symlink":                                          {dbEntry, plugins, {name: "plugins/l", typ: tar.TypeSymlink, link: "/etc/passwd"}},
		"a hard link":                                        {dbEntry, plugins, {name: "plugins/l", typ: tar.TypeLink, link: "site.db"}},
		"a device":                                           {dbEntry, plugins, {name: "plugins/c", typ: tar.TypeChar}},
		"duplicate database":                                 {dbEntry, dbEntry},
		"a database that is not one":                         {{name: "site.db", body: "this is not sqlite", mode: 0o600}},
		"no database at all":                                 {plugins},
	}
	for name, entries := range cases {
		parent := t.TempDir()
		dir := filepath.Join(parent, "data")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := Restore(context.Background(), archive(t, entries...), dir, "site.db"); err == nil {
			t.Errorf("%s: accepted", name)
		}
		// Nothing may be left behind, inside or outside the directory.
		if left, _ := os.ReadDir(dir); len(left) != 0 {
			t.Errorf("%s: left %d entries in the data directory", name, len(left))
		}
		if left, _ := os.ReadDir(parent); len(left) != 1 {
			t.Errorf("%s: something was written next to the data directory", name)
		}
	}
}

// The tar structure of this archive is complete; only the checksum at the very end
// of the gzip stream is wrong.
func TestRestoreChecksTheGzipChecksum(t *testing.T) {
	dataDir, dbPath := liveData(t)
	var buf bytes.Buffer
	if err := Write(context.Background(), &buf, dataDir, dbPath); err != nil {
		t.Fatal(err)
	}
	b := append([]byte(nil), buf.Bytes()...)
	b[len(b)-8] ^= 0xff // the CRC-32 of the uncompressed data
	dir := t.TempDir()
	if err := Restore(context.Background(), bytes.NewReader(b), dir, "site.db"); err == nil {
		t.Fatal("an archive with a wrong checksum was accepted")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left %d entries", len(left))
	}
}

func TestRestoreRefusesATruncatedArchive(t *testing.T) {
	dataDir, dbPath := liveData(t)
	var buf bytes.Buffer
	if err := Write(context.Background(), &buf, dataDir, dbPath); err != nil {
		t.Fatal(err)
	}
	cut := buf.Bytes()[:buf.Len()-20]
	dir := t.TempDir()
	if err := Restore(context.Background(), bytes.NewReader(cut), dir, "site.db"); err == nil {
		t.Fatal("a truncated archive was accepted")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left %d entries", len(left))
	}
	if err := Restore(context.Background(), strings.NewReader("not a gzip stream"), t.TempDir(), "site.db"); err == nil {
		t.Error("garbage was accepted")
	}
}
