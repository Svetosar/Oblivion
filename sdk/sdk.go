// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sdk is the public, versioned API every plugin is written against.
// Official plugins import ONLY this package from the kernel side (enforced by
// a test), so they exercise exactly the API third parties get.
package sdk

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
)

// APIVersion is the plugin API version. A plugin whose Manifest.APIVersion
// differs is refused. Breaking changes bump this number.
const APIVersion = 1

// Capability is a permission a plugin must declare in its manifest.
// Host methods return ErrCapabilityDenied for undeclared/ungranted capabilities.
type Capability string

const (
	CapDB         Capability = "db"          // Host.DB: SQL access to the instance database
	CapKV         Capability = "storage.kv"  // Host.KV: namespaced key/value store
	CapHTTPAPI    Capability = "http.api"    // Host.HandleAPI: routes under /api/v1/<id>/
	CapHTTPPublic Capability = "http.public" // Host.HandlePublic: routes on the public site
	CapHooks      Capability = "hooks"       // Host.OnAction/OnFilter on manifest-declared hooks
	CapIdentity   Capability = "identity"    // provide the identity service (authentication)
)

// KnownCapabilities lists every capability the kernel understands.
var KnownCapabilities = map[Capability]bool{
	CapDB: true, CapKV: true, CapHTTPAPI: true, CapHTTPPublic: true, CapHooks: true, CapIdentity: true,
}

// ServiceIdentity is the well-known service name of the authentication provider.
// The service value must implement Identity.
const ServiceIdentity = "identity"

var (
	ErrCapabilityDenied = errors.New("sdk: capability not declared or not granted")
	ErrNotDeclared      = errors.New("sdk: name not declared in manifest")
	ErrReservedRoute    = errors.New("sdk: route pattern is reserved or invalid")
	ErrServiceMissing   = errors.New("sdk: service not available")
	// ErrForbidden returned from Identity.Authenticate makes the kernel answer 403.
	ErrForbidden = errors.New("sdk: forbidden")
)

// Manifest describes a plugin. It is data: what a plugin may do is exactly what
// it declares here, and an admin sees this before enabling it.
type Manifest struct {
	ID           string // [a-z][a-z0-9_]{1,31}; also the route and storage namespace
	Name         string // human-readable
	Version      string // semver of the plugin
	APIVersion   int    // must equal sdk.APIVersion
	Description  string
	Capabilities []Capability
	Hooks        []string // hook names this plugin listens to (needs CapHooks)
	Provides     []string // service names this plugin provides
	Requires     []string // service names that must be provided by other enabled plugins
	Permissions  []string // permission strings this plugin introduces (e.g. "content.write")
	Migrations   fs.FS    // optional; directory "migrations/NNNN_name.sql" (needs CapDB)
}

// Plugin is implemented by every plugin.
type Plugin interface {
	Manifest() Manifest
	// Init is called once at startup, after plugins it Requires were initialized.
	Init(ctx context.Context, h Host) error
}

// Closer may be implemented by a plugin that needs cleanup at shutdown.
type Closer interface {
	Close(ctx context.Context) error
}

type (
	// ActionFunc observes an event; an error is logged and does not stop other listeners.
	ActionFunc func(ctx context.Context, payload any) error
	// FilterFunc transforms a value; on error the value passes through unchanged.
	FilterFunc func(ctx context.Context, value any) (any, error)
)

// Host is the per-plugin handle to the kernel. Every method is capability-gated.
type Host interface {
	Log() *slog.Logger // always available; already tagged with the plugin id

	DB() (*sql.DB, error)                              // CapDB
	KV() (KV, error)                                   // CapKV
	HandleAPI(pattern string, h http.Handler) error    // CapHTTPAPI; pattern "[METHOD ]/path" relative to /api/v1/<id>
	HandlePublic(pattern string, h http.Handler) error // CapHTTPPublic; pattern "[METHOD ]/path" on the site root

	OnAction(hook string, fn ActionFunc) error // CapHooks + hook in Manifest.Hooks
	OnFilter(hook string, fn FilterFunc) error // CapHooks + hook in Manifest.Hooks
	// Emit hooks: name must start with "<id>." (namespacing).
	DoAction(ctx context.Context, hook string, payload any) error
	ApplyFilters(ctx context.Context, hook string, value any) (any, error)

	Provide(service string, impl any) error // service must be in Manifest.Provides
	Service(service string) (any, error)    // service must be in Manifest.Requires
}

// KV is a small namespaced key/value store. Keys and values are bounded.
type KV interface {
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)
	Set(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
}

// Principal is the authenticated actor of a request.
type Principal struct {
	UserID   int64
	Username string
	Roles    []string
	Perms    map[string]bool // resolved permissions; "*" means everything
}

// Can reports whether the principal holds permission perm. Nil principal can nothing.
func (p *Principal) Can(perm string) bool {
	if p == nil {
		return false
	}
	return p.Perms["*"] || p.Perms[perm]
}

// Identity is implemented by the authentication plugin and published as ServiceIdentity.
// Authenticate returns (nil, nil) for an anonymous request, (*Principal, nil) for an
// authenticated one, ErrForbidden (or wrapping it) to reject (e.g. CSRF failure),
// or any other error for an internal failure (kernel answers 500, fail-closed).
type Identity interface {
	Authenticate(r *http.Request) (*Principal, error)
}

type ctxKey struct{}

// WithPrincipal stores p in ctx (kernel use).
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom returns the request principal, or nil if anonymous.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}
