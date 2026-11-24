// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads configuration from environment variables only.
// No config files and no remote sources: fewer places for secrets to leak.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"oblivion/internal/brand"
)

// Config is the immutable runtime configuration.
type Config struct {
	Listen         string         // HTTP listen address
	DataDir        string         // directory for sqlite, media, plugins
	LogLevel       string         // debug|info|warn|error
	LogJSON        bool           // JSON logs instead of text
	SecureCookie   bool           // set Secure flag on cookies (must be true behind TLS)
	MaxBodyBytes   int64          // default request body limit
	ShutdownWait   time.Duration  // graceful shutdown timeout
	TrustedProxies []netip.Prefix // peers whose X-Forwarded-For is honored
}

// DBPath returns the SQLite file path.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, brand.Slug+".db") }

func env(key string) string { return os.Getenv(brand.EnvPrefix + key) }

func envDefault(key, def string) string {
	if v := env(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) (bool, error) {
	v := env(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s%s: %w", brand.EnvPrefix, key, err)
	}
	return b, nil
}

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	var (
		c   Config
		err error
	)
	c.Listen = envDefault("LISTEN", "127.0.0.1:8080")
	c.DataDir = envDefault("DATA_DIR", "./data")
	c.LogLevel = envDefault("LOG_LEVEL", "info")
	if c.LogJSON, err = envBool("LOG_JSON", false); err != nil {
		return c, err
	}
	// Secure by default; opt out explicitly for local http development.
	if c.SecureCookie, err = envBool("SECURE_COOKIE", true); err != nil {
		return c, err
	}
	c.MaxBodyBytes = 1 << 20 // 1 MiB; media upload routes set their own limit
	c.ShutdownWait = 15 * time.Second
	if c.TrustedProxies, err = parseProxies(env("TRUSTED_PROXIES")); err != nil {
		return c, err
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, fmt.Errorf("%sLOG_LEVEL: unknown level %q", brand.EnvPrefix, c.LogLevel)
	}
	return c, nil
}

// parseProxies parses a comma-separated list of IPs or CIDRs.
func parseProxies(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("%sTRUSTED_PROXIES: %q is not an IP or CIDR", brand.EnvPrefix, part)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
