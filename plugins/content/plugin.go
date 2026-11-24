// SPDX-License-Identifier: AGPL-3.0-or-later

// Package content is the official content plugin: posts and pages written in
// Markdown, draft/published workflow, a JSON API for the admin, and public
// rendering with the built-in theme.
package content

import (
	"context"
	"database/sql"
	"embed"
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
		Permissions:  []string{permRead, permWrite, permWriteOwn, permPublish, permSettings},
		Migrations:   migrationsFS,
	}
}

func (p *Plugin) Init(_ context.Context, h sdk.Host) error {
	var err error
	if p.db, err = h.DB(); err != nil {
		return err
	}
	if p.kv, err = h.KV(); err != nil {
		return err
	}
	p.h = h
	if err := p.registerAPI(); err != nil {
		return err
	}
	return p.registerPublic()
}
