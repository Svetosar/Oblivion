// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"

	"oblivion/internal/brand"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"LISTEN", "DATA_DIR", "LOG_LEVEL", "LOG_JSON", "SECURE_COOKIE", "TRUSTED_PROXIES"} {
		t.Setenv(brand.EnvPrefix+k, "")
	}
	for k, v := range kv {
		t.Setenv(brand.EnvPrefix+k, v)
	}
}

func TestDefaultsAreSafe(t *testing.T) {
	setEnv(t, nil)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.SecureCookie {
		t.Error("cookies must be Secure unless explicitly turned off")
	}
	if !strings.HasPrefix(c.Listen, "127.0.0.1:") {
		t.Errorf("default listen %q must be loopback", c.Listen)
	}
	if len(c.TrustedProxies) != 0 {
		t.Error("no proxy is trusted by default")
	}
}

func TestSecureCookieOptOut(t *testing.T) {
	setEnv(t, map[string]string{"SECURE_COOKIE": "false"})
	c, err := Load()
	if err != nil || c.SecureCookie {
		t.Fatalf("got %v, %v", c.SecureCookie, err)
	}
}

func TestBadValuesAreRefused(t *testing.T) {
	for name, kv := range map[string]map[string]string{
		"bool":       {"SECURE_COOKIE": "maybe"},
		"json":       {"LOG_JSON": "x"},
		"level":      {"LOG_LEVEL": "loud"},
		"proxy":      {"TRUSTED_PROXIES": "10.0.0.1, not-an-ip"},
		"proxy cidr": {"TRUSTED_PROXIES": "10.0.0.0/33"},
	} {
		setEnv(t, kv)
		if _, err := Load(); err == nil {
			t.Errorf("%s: accepted %v", name, kv)
		}
	}
}

func TestTrustedProxies(t *testing.T) {
	setEnv(t, map[string]string{"TRUSTED_PROXIES": " 10.1.2.3 , 192.168.0.7/24,, ::1 "})
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, p := range c.TrustedProxies {
		got = append(got, p.String())
	}
	want := "10.1.2.3/32 192.168.0.0/24 ::1/128"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v, want %s", got, want)
	}
}

func TestDBPath(t *testing.T) {
	setEnv(t, map[string]string{"DATA_DIR": "/srv/d"})
	c, _ := Load()
	if c.DBPath() != "/srv/d/"+brand.Slug+".db" {
		t.Errorf("DBPath = %s", c.DBPath())
	}
}
