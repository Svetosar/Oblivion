// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server wires the kernel HTTP server.
package server

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"oblivion/internal/admin"
	"oblivion/internal/config"
	"oblivion/internal/httpx"
	"oblivion/internal/plugins"
)

// New builds the HTTP server. Routes are registered on a ServeMux (Go 1.22+ patterns).
func New(cfg config.Config, log *slog.Logger, d *sql.DB, mgr *plugins.Manager) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.PingContext(ctx); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("ok\n"))
	})

	// Everything else belongs to plugins: /api/ to their API routes, the rest
	// to their public routes. The identity middleware resolves the principal
	// for every request and fails closed.
	adminUI := admin.Handler()
	mux.Handle("GET /admin", http.RedirectHandler("/admin/", http.StatusMovedPermanently))
	mux.Handle("GET /admin/", adminUI)
	mux.Handle("/api/", mgr.APIHandler())
	mux.Handle("/", mgr.PublicHandler())

	// First is outermost. Security headers wrap everything, so even a panic
	// (500) or a rejected identity (403) carries the CSP and friends.
	handler := httpx.Chain(mux,
		httpx.SecurityHeaders(cfg.SecureCookie),
		httpx.Recover(log),
		httpx.AccessLog(log),
		httpx.ForwardedHint(log, cfg.TrustedProxies),
		httpx.Gzip,
		bodyLimit(cfg.MaxBodyBytes, mgr),
		mgr.IdentityMiddleware,
	)
	return &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// Serve runs the server until ctx is cancelled, then shuts down gracefully.
func Serve(ctx context.Context, srv *http.Server, wait time.Duration, log *slog.Logger) error {
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// bodyLimit applies the default request body limit, raised for the API routes
// of plugins that declare MaxBodyBytes in their manifest.
func bodyLimit(def int64, mgr *plugins.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, mgr.BodyLimit(r.URL.Path, def))
			next.ServeHTTP(w, r)
		})
	}
}
