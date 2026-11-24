// SPDX-License-Identifier: AGPL-3.0-or-later

// Package media is the official media plugin: image upload with magic-byte
// type detection, metadata (EXIF/XMP/comment) stripping and size limits.
package media

import (
	"context"
	"database/sql"
	"embed"
	"net/http"
	"time"

	"oblivion/sdk"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	permRead   = "media.read"
	permWrite  = "media.write"
	permManage = "media.manage" // delete other people's files

	maxUpload = 10 << 20 // bytes of image data
	// The request body limit leaves room for multipart framing.
	maxRequest = maxUpload + 1<<20
	maxPixels  = 50_000_000

	// Total stored bytes allowed per uploader: a low-privilege account must not
	// be able to fill the disk.
	defaultQuota = 200 << 20
	// A large upload over a slow link legitimately takes longer than the
	// server-wide read timeout; the upload route extends its own deadline.
	uploadDeadline = 3 * time.Minute
)

type Plugin struct {
	db    *sql.DB
	files sdk.Files
	h     sdk.Host
	now   func() time.Time
	sem   chan struct{} // bounds in-memory uploads: 1 GB host
	quota int64         // per-uploader byte quota
	dc    dimCache
}

func New() *Plugin {
	return &Plugin{now: time.Now, sem: make(chan struct{}, 2), quota: defaultQuota}
}

func (p *Plugin) Manifest() sdk.Manifest {
	return sdk.Manifest{
		ID:           "media",
		Name:         "Media",
		Version:      "0.1.0",
		APIVersion:   sdk.APIVersion,
		Description:  "Image uploads with metadata stripping",
		Capabilities: []sdk.Capability{sdk.CapDB, sdk.CapFiles, sdk.CapHTTPAPI, sdk.CapHTTPPublic, sdk.CapHooks},
		Hooks:        []string{htmlHook},
		Permissions:  []string{permRead, permWrite, permManage},
		Migrations:   migrationsFS,
		MaxBodyBytes: maxRequest,
	}
}

func (p *Plugin) Init(_ context.Context, h sdk.Host) error {
	var err error
	if p.db, err = h.DB(); err != nil {
		return err
	}
	if p.files, err = h.Files(); err != nil {
		return err
	}
	p.h = h
	if err := h.OnFilter(htmlHook, p.filterHTML); err != nil {
		return err
	}
	for _, r := range []struct {
		api     bool
		pattern string
		fn      func(http.ResponseWriter, *http.Request)
	}{
		{true, "POST /items", p.handleUpload},
		{true, "GET /items", p.handleList},
		{true, "DELETE /items/{id}", p.handleDelete},
		{false, "GET /media/{name}", p.handleServe},
	} {
		reg := h.HandlePublic
		if r.api {
			reg = h.HandleAPI
		}
		if err := reg(r.pattern, http.HandlerFunc(r.fn)); err != nil {
			return err
		}
	}
	return nil
}
