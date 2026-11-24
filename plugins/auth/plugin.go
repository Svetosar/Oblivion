// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth is the official authentication plugin: users, argon2id
// passwords, server-side sessions, CSRF protection, roles and brute-force
// limiting. It provides the "identity" service the kernel uses to resolve the
// principal of every request.
package auth

import (
	"context"
	"database/sql"
	"embed"
	"net/http"
	"sync"
	"time"

	"oblivion/sdk"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Plugin is the auth plugin. Create with New.
type Plugin struct {
	db  *sql.DB
	rt  sdk.Runtime
	h   sdk.Host
	now func() time.Time

	ipLimit   *limiter // per client network (IPv6 grouped by /64)
	userLimit *limiter // per username, from any address: slows distributed guessing

	stop context.CancelFunc // stops the session cleanup loop

	mu           sync.Mutex
	setupToken   string // non-empty while no user exists
	setupExpires time.Time
	dummyHash    string // verified for unknown users to equalize timing
}

func New() *Plugin {
	p := &Plugin{
		now:     time.Now,
		ipLimit: newLimiter(10, 30*time.Second, 15*time.Minute, 15*time.Minute),
		// Deliberately mild: a hard lock here would let anyone lock the admin out.
		// After 20 failures: 1s, 2s, 4s ... capped at 2 minutes.
		userLimit: newLimiter(20, time.Second, 2*time.Minute, 15*time.Minute),
	}
	// One clock for everything, so tests can move time.
	clock := func() time.Time { return p.now() }
	p.ipLimit.now, p.userLimit.now = clock, clock
	return p
}

func (p *Plugin) Manifest() sdk.Manifest {
	return sdk.Manifest{
		ID:           "auth",
		Name:         "Authentication",
		Version:      "0.1.0",
		APIVersion:   sdk.APIVersion,
		Description:  "Users, sessions, roles, CSRF protection and login rate limiting",
		Capabilities: []sdk.Capability{sdk.CapDB, sdk.CapHTTPAPI, sdk.CapHooks, sdk.CapIdentity},
		Provides:     []string{sdk.ServiceIdentity},
		Permissions:  []string{permManageUsers},
		Migrations:   migrationsFS,
	}
}

func (p *Plugin) Init(ctx context.Context, h sdk.Host) error {
	var err error
	if p.db, err = h.DB(); err != nil {
		return err
	}
	p.h, p.rt = h, h.Runtime()
	if p.dummyHash, err = hashPassword(ctx, "dummy-password-for-timing-equalization"); err != nil {
		return err
	}
	if err := p.registerRoutes(); err != nil {
		return err
	}
	if err := h.Provide(sdk.ServiceIdentity, p); err != nil {
		return err
	}
	p.startCleanup()
	return p.maybeIssueSetupToken(ctx)
}

// maybeIssueSetupToken creates a one-time token when no user exists yet.
func (p *Plugin) maybeIssueSetupToken(ctx context.Context) error {
	var n int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return p.issueSetupToken()
}

func (p *Plugin) emit(ctx context.Context, hook string, payload map[string]any) {
	_ = p.h.DoAction(ctx, hook, payload)
}

func principalOf(r *http.Request) *sdk.Principal { return sdk.PrincipalFrom(r.Context()) }

// startCleanup removes dead sessions now and then hourly, until Close.
func (p *Plugin) startCleanup() {
	ctx, cancel := context.WithCancel(context.Background())
	p.stop = cancel
	run := func() {
		if n, err := p.cleanupSessions(ctx); err != nil && ctx.Err() == nil {
			p.h.Log().Error("session cleanup", "err", err)
		} else if n > 0 {
			p.h.Log().Debug("expired sessions removed", "count", n)
		}
	}
	run()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}

// Close stops background work (sdk.Closer).
func (p *Plugin) Close(context.Context) error {
	if p.stop != nil {
		p.stop()
	}
	return nil
}
