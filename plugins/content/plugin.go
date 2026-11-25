// SPDX-License-Identifier: AGPL-3.0-or-later

// Package content is the official content plugin: posts and pages written in
// Markdown, draft/published workflow, a JSON API for the admin, and public
// rendering with the built-in theme.
package content

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"sync/atomic"
	"time"

	"oblivion/sdk"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Permissions checked by this plugin; the auth plugin maps roles to them.
const (
	permRead     = "content.read"
	permWrite    = "content.write"
	permWriteOwn = "content.write.own"
	permPublish  = "content.publish"
)

type Plugin struct {
	db  *sql.DB
	kv  sdk.KV
	h   sdk.Host
	now func() time.Time
	st  settingsStore

	acc     sdk.Accounts // answers whether a nickname is a login; required
	version atomic.Int64 // bumped by every write that can change a public document
	docs    docCache
}

func New() *Plugin { return &Plugin{now: time.Now} }

func (p *Plugin) Manifest() sdk.Manifest {
	return sdk.Manifest{
		ID:           "content",
		Name:         "Content",
		Version:      "0.1.0",
		APIVersion:   sdk.APIVersion,
		Description:  "Posts and pages in Markdown with a built-in public theme",
		Capabilities: []sdk.Capability{sdk.CapDB, sdk.CapKV, sdk.CapHTTPAPI, sdk.CapHTTPPublic},
		Requires:     []string{sdk.ServiceAccounts},
		Permissions:  []string{permRead, permWrite, permWriteOwn, permPublish, permSettings},
		Migrations:   migrationsFS,
	}
}

func (p *Plugin) Init(ctx context.Context, h sdk.Host) error {
	var err error
	if p.db, err = h.DB(); err != nil {
		return err
	}
	if p.kv, err = h.KV(); err != nil {
		return err
	}
	p.h = h
	svc, err := h.Service(sdk.ServiceAccounts)
	if err != nil {
		return err
	}
	acc, ok := svc.(sdk.Accounts)
	if !ok {
		return errors.New("content: the accounts service does not implement sdk.Accounts")
	}
	p.acc = acc
	if err := p.backfillDerived(ctx); err != nil {
		return err
	}
	if err := p.registerAPI(); err != nil {
		return err
	}
	return p.registerPublic()
}

// backfillDerived fills lead/og_image for rows saved before they existed
// (derived = 0). New and edited rows are always derived on save.
func (p *Plugin) backfillDerived(ctx context.Context) error {
	for {
		rows, err := p.db.QueryContext(ctx, `SELECT id, body_html FROM content_items WHERE derived = 0 LIMIT 100`)
		if err != nil {
			return err
		}
		type row struct {
			id   int64
			html string
		}
		var batch []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.html); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, r := range batch {
			lead, og := derive(r.html)
			if _, err := p.db.ExecContext(ctx, `UPDATE content_items SET lead=?, og_image=?, derived=1 WHERE id=? AND derived=0`, lead, og, r.id); err != nil {
				return err
			}
		}
	}
}
