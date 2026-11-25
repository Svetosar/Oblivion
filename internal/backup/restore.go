// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"oblivion/internal/db"
)

// maxRestoreBytes bounds what an archive may unpack to: a gzip bomb must not fill
// the volume.
const maxRestoreBytes = 8 << 30

// Restore unpacks a backup made by Write into an EMPTY data directory and checks
// the database. It refuses anything an honest backup never contains: paths that
// leave the directory, links, devices, other top-level names, a second database,
// a truncated stream. If anything is wrong, everything it created is removed, so
// the directory is empty again.
func Restore(ctx context.Context, r io.Reader, dataDir, dbName string) (err error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("restore: %s is not empty; restoring over existing data would destroy it", dataDir)
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	defer func() {
		if err != nil {
			left, _ := os.ReadDir(dataDir)
			for _, e := range left {
				_ = os.RemoveAll(filepath.Join(dataDir, e.Name()))
			}
		}
	}()

	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("restore: not a gzip archive: %w", err)
	}
	tr := tar.NewReader(zr)
	var total int64
	seenDB := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("restore: damaged archive: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || !filepath.IsLocal(name) || path.Clean(name) != name {
			return fmt.Errorf("restore: unsafe path %q in the archive", h.Name)
		}
		top, _, nested := strings.Cut(name, "/")
		switch {
		case name == dbName && h.Typeflag == tar.TypeReg:
			if seenDB {
				return errors.New("restore: the archive holds two databases")
			}
			seenDB = true
		case top == "plugins":
			// a directory or a file inside the plugins' directory
		default:
			return fmt.Errorf("restore: %q does not belong in a backup", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if top != "plugins" {
				return fmt.Errorf("restore: %q does not belong in a backup", h.Name)
			}
			if err := root.MkdirAll(name, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if top == "plugins" && !nested {
				return fmt.Errorf("restore: %q is a file where the plugins' directory belongs", h.Name)
			}
			if h.Size < 0 || total+h.Size > maxRestoreBytes {
				return errors.New("restore: the archive unpacks to more than the allowed size")
			}
			total += h.Size
			if dir := path.Dir(name); dir != "." {
				if err := root.MkdirAll(dir, 0o700); err != nil {
					return err
				}
			}
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, cerr := io.CopyN(f, tr, h.Size)
			if err := f.Close(); cerr == nil {
				cerr = err
			}
			if cerr != nil {
				return fmt.Errorf("restore: write %s: %w", name, cerr)
			}
		default:
			return fmt.Errorf("restore: %q is not a plain file or directory", h.Name)
		}
	}
	// Reading the stream to its end makes gzip verify its checksum: a truncated
	// archive fails here even if the tar structure looked complete.
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return fmt.Errorf("restore: damaged archive: %w", err)
	}
	if !seenDB {
		return errors.New("restore: the archive has no database")
	}
	return db.IntegrityCheck(ctx, filepath.Join(dataDir, dbName))
}
