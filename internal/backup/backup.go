// SPDX-License-Identifier: AGPL-3.0-or-later

// Package backup packs the persistent state of a data directory, the database
// and the plugin files, into one tar.gz. Unpacking it into an empty data
// directory restores the site.
//
// The archive contains password hashes: treat it like the database itself.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"oblivion/internal/db"
)

// ToFile writes a backup into a new private file. It never overwrites an
// existing file, and removes its own output if anything fails.
func ToFile(ctx context.Context, path, dataDir, dbPath string) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err := Write(ctx, f, dataDir, dbPath); err != nil {
		return err
	}
	return f.Sync()
}

// Write streams a backup to w. The server may keep running meanwhile.
//
// The database is captured first and the files after it, so an upload that
// happens during the backup leaves at most an unused file in the archive. Only a
// file deleted during the backup can leave a database row without its file.
//
// On error the output is left unterminated, so unpacking it fails too.
func Write(ctx context.Context, w io.Writer, dataDir, dbPath string) error {
	// Inside the data directory: the container image has no /tmp.
	tmp, err := os.MkdirTemp(dataDir, ".backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snapshot := filepath.Join(tmp, "snapshot.db")
	if err := db.Snapshot(ctx, dbPath, snapshot); err != nil {
		return err
	}

	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	if err := addFile(tw, snapshot, filepath.Base(dbPath)); err != nil {
		return err
	}
	root := filepath.Join(dataDir, "plugins")
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil // no plugin files at all, or deleted while we were walking
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		switch {
		case e.IsDir():
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o700})
		case e.Type().IsRegular():
			if err := addFile(tw, path, name); !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		default:
			return fmt.Errorf("backup: %s is not a regular file or directory", path)
		}
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// addFile appends the file at path to the archive under name.
func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	h := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime()}
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	if _, err := io.CopyN(tw, f, st.Size()); err != nil {
		return fmt.Errorf("backup: copy %s: %w", path, err)
	}
	return nil
}
