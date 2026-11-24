// SPDX-License-Identifier: AGPL-3.0-or-later

// Command oblivion is the CMS server binary.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"oblivion/internal/brand"
	"oblivion/internal/config"
	"oblivion/internal/db"
	"oblivion/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", brand.Slug, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if cfg.LogJSON {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	log := slog.New(h)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer d.Close()

	srv := server.New(cfg, log, d)
	return server.Serve(ctx, srv, cfg.ShutdownWait, log)
}
