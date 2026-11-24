// SPDX-License-Identifier: AGPL-3.0-or-later

// Package plugins is the kernel's plugin manager: validation, dependency
// ordering, migrations, lifecycle, routing and the hook bus.
//
// Honest security note: capabilities are a HARD boundary only for sandboxed
// (WASM) plugins. A natively compiled plugin is trusted code running in the
// kernel process; for it the capability system is an enforced API contract
// plus a review aid, not isolation. Only plugins compiled into the binary can
// be native.
package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"sync"

	"oblivion/internal/db"
	"oblivion/sdk"
)

var (
	idRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9_.]{1,63}$`) // service and hook names
)

// Manager owns all plugins of one kernel instance.
type Manager struct {
	log *slog.Logger
	db  *sql.DB
	bus *hookBus

	apiMux    *http.ServeMux
	publicMux *http.ServeMux

	essential  map[string]bool
	registered []sdk.Plugin

	mu       sync.RWMutex
	services map[string]any
	identity sdk.Identity
	active   []string // ids, init order
}

// New creates a manager. essential lists plugin ids without which the kernel
// must refuse to start (e.g. the auth plugin: fail closed).
func New(log *slog.Logger, d *sql.DB, essential ...string) *Manager {
	m := &Manager{
		log: log, db: d, bus: newHookBus(log),
		apiMux: http.NewServeMux(), publicMux: http.NewServeMux(),
		essential: map[string]bool{}, services: map[string]any{},
	}
	for _, e := range essential {
		m.essential[e] = true
	}
	return m
}

// Register adds a compiled-in plugin. Call before Start.
func (m *Manager) Register(p sdk.Plugin) { m.registered = append(m.registered, p) }

// APIHandler serves /api/v1/<plugin>/... routes.
func (m *Manager) APIHandler() http.Handler { return m.apiMux }

// PublicHandler serves plugin-registered public site routes.
func (m *Manager) PublicHandler() http.Handler { return m.publicMux }

// Emit lets the kernel itself fire an action (hook names start with "kernel.").
func (m *Manager) Emit(ctx context.Context, hook string, payload any) {
	m.bus.doAction(ctx, hook, payload)
}

// Active returns the ids of started plugins in init order.
func (m *Manager) Active() []string { return append([]string(nil), m.active...) }

func validate(mf sdk.Manifest) error {
	if !idRe.MatchString(mf.ID) {
		return fmt.Errorf("invalid id %q", mf.ID)
	}
	if mf.APIVersion != sdk.APIVersion {
		return fmt.Errorf("api version %d, kernel speaks %d", mf.APIVersion, sdk.APIVersion)
	}
	if mf.Version == "" || mf.Name == "" {
		return errors.New("name and version are required")
	}
	caps := map[sdk.Capability]bool{}
	for _, c := range mf.Capabilities {
		if !sdk.KnownCapabilities[c] {
			return fmt.Errorf("unknown capability %q", c)
		}
		caps[c] = true
	}
	for _, n := range append(append(append([]string{}, mf.Hooks...), mf.Provides...), mf.Requires...) {
		if !nameRe.MatchString(n) {
			return fmt.Errorf("invalid hook/service name %q", n)
		}
	}
	if len(mf.Hooks) > 0 && !caps[sdk.CapHooks] {
		return errors.New("declares hooks without capability hooks")
	}
	if mf.Migrations != nil && !caps[sdk.CapDB] {
		return errors.New("declares migrations without capability db")
	}
	for _, s := range mf.Provides {
		if s == sdk.ServiceIdentity && !caps[sdk.CapIdentity] {
			return errors.New("provides identity without capability identity")
		}
	}
	return nil
}

// Start validates, orders and initializes all enabled plugins.
func (m *Manager) Start(ctx context.Context) error {
	byID := map[string]sdk.Plugin{}
	for _, p := range m.registered {
		mf := p.Manifest()
		if err := validate(mf); err != nil {
			return fmt.Errorf("plugin %q: %w", mf.ID, err)
		}
		if _, dup := byID[mf.ID]; dup {
			return fmt.Errorf("duplicate plugin id %q", mf.ID)
		}
		byID[mf.ID] = p
	}
	for id := range m.essential {
		if _, ok := byID[id]; !ok {
			return fmt.Errorf("essential plugin %q is not compiled in", id)
		}
	}

	enabled, err := m.loadState(ctx, byID)
	if err != nil {
		return err
	}
	for id := range m.essential {
		if !enabled[id] {
			return fmt.Errorf("essential plugin %q is disabled", id)
		}
	}

	order, err := m.order(byID, enabled)
	if err != nil {
		return err
	}
	for _, id := range order {
		if err := m.startOne(ctx, byID[id]); err != nil {
			if m.essential[id] {
				return fmt.Errorf("essential plugin %q failed: %w", id, err)
			}
			m.log.Error("plugin failed to start; continuing without it", "plugin", id, "err", err)
			continue
		}
		m.active = append(m.active, id)
	}

	m.mu.Lock()
	if s, ok := m.services[sdk.ServiceIdentity].(sdk.Identity); ok {
		m.identity = s
	}
	m.mu.Unlock()
	m.log.Info("plugins started", "active", m.active)
	return nil
}

// loadState ensures a row per registered plugin (new ones are enabled: they are
// compiled in, i.e. trusted) and returns the enabled set.
func (m *Manager) loadState(ctx context.Context, byID map[string]sdk.Plugin) (map[string]bool, error) {
	enabled := map[string]bool{}
	for id, p := range byID {
		caps, _ := json.Marshal(p.Manifest().Capabilities)
		if _, err := m.db.ExecContext(ctx,
			`INSERT INTO plugins(id, enabled, granted_caps) VALUES (?, 1, ?) ON CONFLICT(id) DO NOTHING`, id, string(caps)); err != nil {
			return nil, err
		}
	}
	rows, err := m.db.QueryContext(ctx, `SELECT id, enabled FROM plugins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var en int
		if err := rows.Scan(&id, &en); err != nil {
			return nil, err
		}
		if _, ok := byID[id]; ok {
			enabled[id] = en == 1
		}
	}
	return enabled, rows.Err()
}

// order drops plugins whose required services cannot be provided and returns the
// rest topologically sorted (providers first). Deterministic: ties by id.
func (m *Manager) order(byID map[string]sdk.Plugin, enabled map[string]bool) ([]string, error) {
	live := map[string]bool{}
	for id, en := range enabled {
		if en {
			live[id] = true
		}
	}
	// Fixpoint: remove plugins with unsatisfiable requirements.
	for changed := true; changed; {
		changed = false
		provided := map[string]string{}
		for id := range live {
			for _, s := range byID[id].Manifest().Provides {
				if other, dup := provided[s]; dup {
					return nil, fmt.Errorf("service %q provided by both %q and %q", s, other, id)
				}
				provided[s] = id
			}
		}
		for id := range live {
			for _, r := range byID[id].Manifest().Requires {
				if _, ok := provided[r]; !ok {
					if m.essential[id] {
						return nil, fmt.Errorf("essential plugin %q requires service %q which nothing provides", id, r)
					}
					m.log.Error("plugin disabled: unmet requirement", "plugin", id, "service", r)
					delete(live, id)
					changed = true
					break
				}
			}
		}
	}
	provider := map[string]string{}
	for id := range live {
		for _, s := range byID[id].Manifest().Provides {
			provider[s] = id
		}
	}
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []string
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("dependency cycle involving %q", id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, r := range byID[id].Manifest().Requires {
			if dep := provider[r]; dep != id {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		state[id] = 2
		out = append(out, id)
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m *Manager) startOne(ctx context.Context, p sdk.Plugin) (err error) {
	mf := p.Manifest()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in Init: %v", r)
		}
	}()
	h := &host{
		m: m, id: mf.ID, log: m.log.With("plugin", mf.ID),
		caps: map[sdk.Capability]bool{}, hooks: map[string]bool{},
		provides: map[string]bool{}, requires: map[string]bool{},
	}
	for _, c := range mf.Capabilities {
		h.caps[c] = true
	}
	for _, s := range mf.Hooks {
		h.hooks[s] = true
	}
	for _, s := range mf.Provides {
		h.provides[s] = true
	}
	for _, s := range mf.Requires {
		h.requires[s] = true
	}
	if mf.Migrations != nil {
		if err := db.Migrate(ctx, m.db, mf.Migrations, "plugin_"+mf.ID+"_migrations"); err != nil {
			return fmt.Errorf("migrations: %w", err)
		}
	}
	return p.Init(ctx, h)
}

// Close shuts plugins down in reverse init order.
func (m *Manager) Close(ctx context.Context) {
	byID := map[string]sdk.Plugin{}
	for _, p := range m.registered {
		byID[p.Manifest().ID] = p
	}
	for i := len(m.active) - 1; i >= 0; i-- {
		if c, ok := byID[m.active[i]].(sdk.Closer); ok {
			if err := c.Close(ctx); err != nil {
				m.log.Warn("plugin close failed", "plugin", m.active[i], "err", err)
			}
		}
	}
}

// IdentityMiddleware resolves the request principal through the identity
// service (if any) and stores it in the request context. A forbidden result
// answers 403; any other error answers 500 (fail closed, never anonymous).
func (m *Manager) IdentityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.RLock()
		id := m.identity
		m.mu.RUnlock()
		if id == nil || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		p, err := id.Authenticate(r)
		switch {
		case errors.Is(err, sdk.ErrForbidden):
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		case err != nil:
			m.log.Error("identity failure", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r.WithContext(sdk.WithPrincipal(r.Context(), p)))
	})
}
