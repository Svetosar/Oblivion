// SPDX-License-Identifier: AGPL-3.0-or-later

package plugins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"oblivion/sdk"
)

// host is the capability-gated sdk.Host handed to one plugin.
type host struct {
	m        *Manager
	id       string
	caps     map[sdk.Capability]bool
	hooks    map[string]bool
	provides map[string]bool
	requires map[string]bool
	log      *slog.Logger
}

var _ sdk.Host = (*host)(nil)

func (h *host) Log() *slog.Logger { return h.log }

func (h *host) Runtime() sdk.Runtime { return h.m.runtime }

func (h *host) need(c sdk.Capability) error {
	if !h.caps[c] {
		return fmt.Errorf("%w: %s (plugin %s)", sdk.ErrCapabilityDenied, c, h.id)
	}
	return nil
}

func (h *host) DB() (*sql.DB, error) {
	if err := h.need(sdk.CapDB); err != nil {
		return nil, err
	}
	return h.m.db, nil
}

func (h *host) KV() (sdk.KV, error) {
	if err := h.need(sdk.CapKV); err != nil {
		return nil, err
	}
	return &kvStore{db: h.m.db, plugin: h.id}, nil
}

func (h *host) Files() (sdk.Files, error) {
	if err := h.need(sdk.CapFiles); err != nil {
		return nil, err
	}
	return openFiles(h.m.dataDir, h.id)
}

// splitPattern splits "[METHOD ]/path" and validates it. Host-based patterns,
// relative paths and path traversal are rejected.
func splitPattern(p string) (method, path string, err error) {
	if i := strings.IndexByte(p, ' '); i >= 0 {
		method, path = p[:i], strings.TrimSpace(p[i+1:])
		switch method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			return "", "", fmt.Errorf("%w: method %q", sdk.ErrReservedRoute, method)
		}
	} else {
		path = p
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.ContainsAny(path, " \t\r\n") {
		return "", "", fmt.Errorf("%w: %q", sdk.ErrReservedRoute, p)
	}
	return method, path, nil
}

func (h *host) HandleAPI(pattern string, handler http.Handler) error {
	if err := h.need(sdk.CapHTTPAPI); err != nil {
		return err
	}
	method, path, err := splitPattern(pattern)
	if err != nil {
		return err
	}
	return register(h.m.apiMux, method, "/api/v1/"+h.id+path, handler)
}

// Reserved public prefixes plugins can never claim.
var reservedPublic = []string{"/api/", "/healthz", "/admin"}

func (h *host) HandlePublic(pattern string, handler http.Handler) error {
	if err := h.need(sdk.CapHTTPPublic); err != nil {
		return err
	}
	method, path, err := splitPattern(pattern)
	if err != nil {
		return err
	}
	for _, r := range reservedPublic {
		if path == strings.TrimSuffix(r, "/") || strings.HasPrefix(path, r) {
			return fmt.Errorf("%w: %q", sdk.ErrReservedRoute, pattern)
		}
	}
	return register(h.m.publicMux, method, path, handler)
}

// register adds a route; ServeMux panics on conflicts, which we turn into errors.
func register(mux *http.ServeMux, method, path string, handler http.Handler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("route %s %s: %v", method, path, r)
		}
	}()
	p := path
	if method != "" {
		p = method + " " + path
	}
	mux.Handle(p, handler)
	return nil
}

func (h *host) OnAction(hook string, fn sdk.ActionFunc) error {
	if err := h.need(sdk.CapHooks); err != nil {
		return err
	}
	if !h.hooks[hook] {
		return fmt.Errorf("%w: hook %q", sdk.ErrNotDeclared, hook)
	}
	h.m.bus.onAction(h.id, hook, fn)
	return nil
}

func (h *host) OnFilter(hook string, fn sdk.FilterFunc) error {
	if err := h.need(sdk.CapHooks); err != nil {
		return err
	}
	if !h.hooks[hook] {
		return fmt.Errorf("%w: hook %q", sdk.ErrNotDeclared, hook)
	}
	h.m.bus.onFilter(h.id, hook, fn)
	return nil
}

func (h *host) ownHook(hook string) error {
	if !strings.HasPrefix(hook, h.id+".") {
		return fmt.Errorf("%w: plugins may only emit hooks named %q", sdk.ErrNotDeclared, h.id+".*")
	}
	return nil
}

func (h *host) DoAction(ctx context.Context, hook string, payload any) error {
	if err := h.ownHook(hook); err != nil {
		return err
	}
	h.m.bus.doAction(ctx, hook, payload)
	return nil
}

func (h *host) ApplyFilters(ctx context.Context, hook string, value any) (any, error) {
	if err := h.ownHook(hook); err != nil {
		return value, err
	}
	return h.m.bus.applyFilters(ctx, hook, value), nil
}

func (h *host) Provide(service string, impl any) error {
	if !h.provides[service] {
		return fmt.Errorf("%w: provides %q", sdk.ErrNotDeclared, service)
	}
	if service == sdk.ServiceIdentity {
		if err := h.need(sdk.CapIdentity); err != nil {
			return err
		}
		if _, ok := impl.(sdk.Identity); !ok {
			return errors.New("identity service must implement sdk.Identity")
		}
	}
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if _, dup := h.m.services[service]; dup {
		return fmt.Errorf("service %q already provided", service)
	}
	h.m.services[service] = impl
	return nil
}

func (h *host) Service(service string) (any, error) {
	if !h.requires[service] {
		return nil, fmt.Errorf("%w: requires %q", sdk.ErrNotDeclared, service)
	}
	h.m.mu.RLock()
	defer h.m.mu.RUnlock()
	s, ok := h.m.services[service]
	if !ok {
		return nil, fmt.Errorf("%w: %s", sdk.ErrServiceMissing, service)
	}
	return s, nil
}
