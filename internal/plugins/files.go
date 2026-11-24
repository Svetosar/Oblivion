// SPDX-License-Identifier: AGPL-3.0-or-later

package plugins

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"oblivion/sdk"
)

// MaxPluginBody is the largest request body a plugin may ask for.
const MaxPluginBody = 64 << 20

// fileStore confines a plugin to <dataDir>/plugins/<id>/ using os.Root, which
// refuses symlink and ".." escapes at the OS level.
type fileStore struct{ root *os.Root }

func openFiles(dataDir, id string) (sdk.Files, error) {
	if dataDir == "" {
		return nil, errors.New("plugins: data directory not configured")
	}
	dir := filepath.Join(dataDir, "plugins", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &fileStore{root: root}, nil
}

func checkName(name string) error {
	if name == "" || len(name) > 128 || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("plugins: invalid file name %q", name)
	}
	return nil
}

func (s *fileStore) Create(name string) (io.WriteCloser, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	return s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func (s *fileStore) Open(name string) (sdk.File, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	return s.root.Open(name)
}

func (s *fileStore) Remove(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	return s.root.Remove(name)
}
