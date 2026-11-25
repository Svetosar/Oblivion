// SPDX-License-Identifier: AGPL-3.0-or-later

// Command oblivion is the CMS server binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"oblivion/internal/backup"
	"oblivion/internal/brand"
	"oblivion/internal/config"
	"oblivion/internal/db"
	"oblivion/internal/httpx"
	"oblivion/internal/plugins"
	"oblivion/internal/server"
	officialplugins "oblivion/plugins"
	"oblivion/sdk"
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			return healthcheck(cfg)
		case "backup":
			return runBackup(cfg, os.Args[2:])
		case "user":
			return runUser(cfg, os.Args[2:])
		case "restore":
			return runRestore(cfg, os.Args[2:])
		default:
			return fmt.Errorf("unknown command %q", os.Args[1])
		}
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

	mgr := plugins.New(log, d, officialplugins.EssentialIDs...)
	mgr.SetDataDir(cfg.DataDir)
	mgr.SetRuntime(sdk.Runtime{SecureCookies: cfg.SecureCookie, ClientIP: httpx.ClientIPFunc(cfg.TrustedProxies)})
	for _, p := range officialplugins.All() {
		mgr.Register(p)
	}
	// Essential plugins failing to start aborts here: fail closed.
	if err := mgr.Start(ctx); err != nil {
		return err
	}
	defer mgr.Close(context.Background())

	srv := server.New(cfg, log, d, mgr)
	return server.Serve(ctx, srv, cfg.ShutdownWait, log)
}

// runBackup writes a backup of the data directory to a new file, or to standard
// output when the destination is "-". The server may keep running.
func runBackup(cfg config.Config, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: %s backup <new-file.tar.gz | ->", brand.Slug)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if args[0] != "-" {
		return backup.ToFile(ctx, args[0], cfg.DataDir, cfg.DBPath())
	}
	if st, err := os.Stdout.Stat(); err != nil || st.Mode()&os.ModeCharDevice != 0 {
		return errors.New("backup: refusing to write binary data to a terminal; redirect the output")
	}
	return backup.Write(ctx, os.Stdout, cfg.DataDir, cfg.DBPath())
}

// runRestore unpacks a backup read from standard input ("-") into an empty data
// directory. It never overwrites: an existing database is a reason to stop.
func runRestore(cfg config.Config, args []string) error {
	if len(args) != 1 || args[0] != "-" {
		return fmt.Errorf("usage: %s restore -   (the backup is read from standard input)", brand.Slug)
	}
	if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice != 0 {
		return errors.New("restore: refusing to read a backup from a terminal; pipe the file in")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	if err := backup.Restore(ctx, os.Stdin, cfg.DataDir, filepath.Base(cfg.DBPath())); err != nil {
		return err
	}
	fmt.Println("backup restored")
	return nil
}

// healthcheck probes the local /healthz; the scratch image has no curl.
func healthcheck(cfg config.Config) error {
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}
