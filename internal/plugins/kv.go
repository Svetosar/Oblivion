// SPDX-License-Identifier: AGPL-3.0-or-later

package plugins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	maxKVKey   = 256
	maxKVValue = 64 << 10
)

// kvStore is the namespaced key/value store of one plugin.
type kvStore struct {
	db     *sql.DB
	plugin string
}

func (s *kvStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM plugin_kv WHERE plugin_id = ? AND key = ?`, s.plugin, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return v, err == nil, err
}

func (s *kvStore) Set(ctx context.Context, key string, value []byte) error {
	if key == "" || len(key) > maxKVKey {
		return fmt.Errorf("kv: key must be 1..%d bytes", maxKVKey)
	}
	if len(value) > maxKVValue {
		return fmt.Errorf("kv: value exceeds %d bytes", maxKVValue)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO plugin_kv(plugin_id, key, value) VALUES (?,?,?)
		ON CONFLICT(plugin_id, key) DO UPDATE SET value = excluded.value`, s.plugin, key, value)
	return err
}

func (s *kvStore) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM plugin_kv WHERE plugin_id = ? AND key = ?`, s.plugin, key)
	return err
}
