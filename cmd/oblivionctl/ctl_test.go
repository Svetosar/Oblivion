// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const testPassword = "correct horse battery"

type call struct {
	name   string
	args   []string
	stdin  string
	merged bool // standard error goes where standard output goes
}

func (c call) has(args ...string) bool {
	for i := 0; i+len(args) <= len(c.args); i++ {
		if slices.Equal(c.args[i:i+len(args)], args) {
			return true
		}
	}
	return false
}

// fakeEngine records every command instead of running it.
type fakeEngine struct {
	t      *testing.T
	calls  []call
	script func(c call) (stdout string, err error)
}

func (f *fakeEngine) run(_ context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	c := call{name: name, args: args, merged: stdout != nil && stdout == stderr}
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		c.stdin = string(b)
	}
	f.calls = append(f.calls, c)
	for _, a := range args {
		if strings.Contains(a, testPassword) {
			f.t.Errorf("a password reached the command line: %s %q", name, args)
		}
	}
	if f.script != nil {
		out, err := f.script(c)
		if stdout != nil {
			_, _ = io.WriteString(stdout, out)
		}
		return err
	}
	return nil
}

// find returns the calls whose arguments contain all of the given ones.
func (f *fakeEngine) find(args ...string) []call {
	var out []call
	for _, c := range f.calls {
		if c.has(args...) {
			out = append(out, c)
		}
	}
	return out
}

type harness struct {
	app    *app
	eng    *fakeEngine
	out    *bytes.Buffer
	errw   *bytes.Buffer
	stdin  *bytes.Buffer
	health error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{eng: &fakeEngine{t: t}, out: &bytes.Buffer{}, errw: &bytes.Buffer{}, stdin: &bytes.Buffer{}}
	h.eng.script = func(c call) (string, error) {
		if c.has("volume", "inspect") {
			return "", errors.New("no such volume")
		}
		return "", nil
	}
	h.app = &app{
		runner:   h.eng,
		in:       h.stdin,
		out:      h.out,
		errw:     h.errw,
		stateDir: filepath.Join(t.TempDir(), "ctl"),
		lookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		health:   func(context.Context, int) error { return h.health },
		now:      func() time.Time { return time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC) },
		wait:     func(time.Duration) {},
	}
	return h
}

func (h *harness) do(args ...string) error { return h.app.dispatch(context.Background(), args) }

func (h *harness) install(t *testing.T, extra ...string) {
	t.Helper()
	h.stdin.WriteString(testPassword + "\n")
	args := append([]string{"install", "--password-stdin", "--insecure-http"}, extra...)
	if err := h.do(args...); err != nil {
		t.Fatalf("install: %v\n%s", err, h.errw)
	}
	h.eng.calls = nil
}

func TestInstallBuildsAHardenedContainerAndTheFirstAdministrator(t *testing.T) {
	h := newHarness(t)
	h.stdin.WriteString(testPassword + "\n")
	if err := h.do("install", "--password-stdin", "--image", "localhost/oblivion:test", "--port", "9090"); err != nil {
		t.Fatalf("install: %v", err)
	}
	e := h.eng

	if len(e.find("image", "inspect", "localhost/oblivion:test")) != 1 {
		t.Error("the image is not checked first")
	}
	if len(e.find("volume", "create", "oblivion-data")) != 1 {
		t.Error("the data volume is not created")
	}
	creates := e.find("user", "create", "admin", "admin")
	if len(creates) != 1 {
		t.Fatalf("%d administrator creations, want 1; calls: %v", len(creates), e.calls)
	}
	c := creates[0]
	if !c.has("run", "--rm", "-i") || !c.has("-v", "oblivion-data:/data") || c.stdin != testPassword+"\n" {
		t.Errorf("the administrator is not made in a one-shot container with the password on stdin: %+v", c)
	}
	// The administrator exists before the server first starts, so no setup token is needed.
	var first, start = -1, -1
	for i, call := range e.calls {
		if call.has("user", "create") && first < 0 {
			first = i
		}
		if call.has("-d", "--name", "oblivion") {
			start = i
		}
	}
	if first < 0 || start < 0 || first > start {
		t.Errorf("order: administrator at %d, server at %d", first, start)
	}

	srv := e.find("-d", "--name", "oblivion")
	if len(srv) != 1 {
		t.Fatalf("%d server starts: %v", len(srv), e.calls)
	}
	for _, want := range [][]string{
		{"--restart", "unless-stopped"}, {"--read-only"}, {"--cap-drop=ALL"}, {"--security-opt", "no-new-privileges"},
		{"-p", "127.0.0.1:9090:8080"}, {"-v", "oblivion-data:/data"}, {"-e", "OBLIVION_SECURE_COOKIE=true"},
	} {
		if !srv[0].has(want...) {
			t.Errorf("the server container lacks %v: %v", want, srv[0].args)
		}
	}
	if last := srv[0].args[len(srv[0].args)-1]; last != "localhost/oblivion:test" {
		t.Errorf("the image must come last, got %q", last)
	}

	st, err := os.Stat(filepath.Join(h.app.stateDir, "ctl.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("state file: %v %v", st, err)
	}
	if !strings.Contains(h.out.String(), "http://127.0.0.1:9090") {
		t.Errorf("the address is not shown:\n%s", h.out)
	}
}

func TestInstallKeepsExistingAccounts(t *testing.T) {
	h := newHarness(t)
	h.eng.script = func(c call) (string, error) {
		if c.has("user", "list") {
			return "1\troot\tadmin\tactive\t2026-11-25T12:00:00Z\n", nil
		}
		return "", nil
	}
	h.stdin.WriteString(testPassword + "\n")
	if err := h.do("install", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if len(h.eng.find("user", "create")) != 0 {
		t.Error("an existing volume got a second administrator")
	}
	if len(h.eng.find("-d", "--name", "oblivion")) != 1 {
		t.Error("the server was not started")
	}
}

func TestInstallRefusesToRunTwice(t *testing.T) {
	h := newHarness(t)
	h.install(t)
	h.stdin.WriteString(testPassword + "\n")
	err := h.do("install", "--password-stdin")
	if err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("second install: %v", err)
	}
	if len(h.eng.calls) != 0 {
		t.Errorf("a refused install still ran commands: %v", h.eng.calls)
	}
}

// A server that never gets healthy is removed so that the next attempt starts
// clean, and no state is left claiming it is installed.
func TestInstallCleansUpAContainerThatDoesNotStart(t *testing.T) {
	h := newHarness(t)
	h.health = errors.New("connection refused")
	h.stdin.WriteString(testPassword + "\n")
	err := h.do("install", "--password-stdin")
	if err == nil {
		t.Fatal("install succeeded with an unhealthy server")
	}
	if len(h.eng.find("rm", "-f", "oblivion")) != 1 {
		t.Errorf("the failed container was not removed: %v", h.eng.calls)
	}
	if _, serr := os.Stat(filepath.Join(h.app.stateDir, "ctl.json")); !errors.Is(serr, os.ErrNotExist) {
		t.Error("state was saved for a failed install")
	}
}

func TestInstallRejectsDangerousInput(t *testing.T) {
	for name, args := range map[string][]string{
		"image starting with a dash": {"--image", "--privileged"},
		"image with a space":         {"--image", "x y"},
		"port 0":                     {"--port", "0"},
		"port too large":             {"--port", "70000"},
		"unknown engine":             {"--engine", "rm -rf"},
		"proxy list with a quote":    {"--proxy", `10.0.0.0/8" -v /:/host`},
		"login with a space":         {"--admin", "a b"},
	} {
		h := newHarness(t)
		h.stdin.WriteString(testPassword + "\n")
		if err := h.do(append([]string{"install", "--password-stdin"}, args...)...); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(h.eng.calls) != 0 {
			t.Errorf("%s: commands were run: %v", name, h.eng.calls)
		}
	}
}

func TestEngineIsDetectedAndOverridable(t *testing.T) {
	h := newHarness(t)
	h.app.lookPath = func(n string) (string, error) {
		if n == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errors.New("not found")
	}
	h.stdin.WriteString(testPassword + "\n")
	if err := h.do("install", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if h.eng.calls[0].name != "docker" {
		t.Errorf("used %q, want docker when podman is missing", h.eng.calls[0].name)
	}

	h = newHarness(t)
	h.app.lookPath = func(string) (string, error) { return "", errors.New("nothing installed") }
	h.stdin.WriteString(testPassword + "\n")
	if err := h.do("install", "--password-stdin"); err == nil || !strings.Contains(err.Error(), "podman") {
		t.Errorf("no engine: %v", err)
	}
}

func TestUserCommandsRunInAOneShotContainer(t *testing.T) {
	h := newHarness(t)
	h.install(t)

	h.stdin.WriteString(testPassword + "\n")
	if err := h.do("user", "reset-password", "alice", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	c := h.eng.calls[len(h.eng.calls)-1]
	if !c.has("run", "--rm", "-i") || !c.has("-v", "oblivion-data:/data") || c.stdin != testPassword+"\n" {
		t.Errorf("reset-password: %+v", c)
	}
	if n := len(c.args); c.args[n-3] != "user" || c.args[n-2] != "reset-password" || c.args[n-1] != "alice" {
		t.Errorf("arguments: %v", c.args)
	}
	if h.eng.find("exec") != nil {
		t.Error("the helper must not exec into the running container")
	}

	h.eng.calls = nil
	if err := h.do("user", "disable", "alice"); err != nil {
		t.Fatal(err)
	}
	if len(h.eng.calls) != 1 || h.eng.calls[0].stdin != "" {
		t.Errorf("disable: %v", h.eng.calls)
	}
	if err := h.do("user", "delete", "alice"); err == nil {
		t.Error("an unknown subcommand was accepted")
	}
	if err := h.do("user", "reset-password", "-x; rm", "--password-stdin"); err == nil {
		t.Error("a login that looks like an option was accepted")
	}
}

func TestUserListPrintsTheTable(t *testing.T) {
	h := newHarness(t)
	h.install(t)
	h.eng.script = func(c call) (string, error) {
		return "1\troot\tadmin\tactive\t2026-11-25T12:00:00Z\n", nil
	}
	if err := h.do("user", "list"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "root") {
		t.Errorf("output: %q", h.out)
	}
}

func TestBackupWritesAPrivateFileAndNeverOverwrites(t *testing.T) {
	h := newHarness(t)
	h.install(t)
	h.eng.script = func(c call) (string, error) {
		if c.has("backup", "-") {
			return "TAR-BYTES", nil
		}
		return "", nil
	}
	path := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := h.do("backup", path); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", st, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "TAR-BYTES" {
		t.Errorf("content %q", b)
	}
	if err := h.do("backup", path); err == nil {
		t.Error("an existing backup was overwritten")
	}
	if b, _ := os.ReadFile(path); string(b) != "TAR-BYTES" {
		t.Error("the refusal changed the existing file")
	}

	// A failing backup leaves nothing behind.
	h.eng.script = func(c call) (string, error) {
		if c.has("backup", "-") {
			return "PARTIAL", errors.New("exit status 1")
		}
		return "", nil
	}
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err := h.do("backup", bad); err == nil {
		t.Fatal("a failed backup reported success")
	}
	if _, err := os.Stat(bad); !errors.Is(err, os.ErrNotExist) {
		t.Error("a partial backup was left on disk")
	}
}

func TestConfigSetRecreatesTheContainerWithTheNewValue(t *testing.T) {
	h := newHarness(t)
	h.install(t)
	if err := h.do("config", "set", "TRUSTED_PROXIES=10.0.0.0/8,192.168.1.5"); err != nil {
		t.Fatal(err)
	}
	if len(h.eng.find("rm", "-f", "oblivion")) != 1 {
		t.Error("the old container was not removed")
	}
	srv := h.eng.find("-d", "--name", "oblivion")
	if len(srv) != 1 || !srv[0].has("-e", "OBLIVION_TRUSTED_PROXIES=10.0.0.0/8,192.168.1.5") {
		t.Fatalf("the new container: %v", srv)
	}
	if err := h.do("config", "show"); err != nil || !strings.Contains(h.out.String(), "TRUSTED_PROXIES=10.0.0.0/8,192.168.1.5") {
		t.Errorf("show: %v %q", err, h.out)
	}

	for _, bad := range []string{
		"TRUSTED_PROXIES=10.0.0.0/8 -v /:/host", "TRUSTED_PROXIES=$(id)", "SECURE_COOKIE=maybe", "LOG_LEVEL=chatty",
		"LISTEN=0.0.0.0:80", "NOPE=1", "TRUSTED_PROXIES",
	} {
		before := len(h.eng.calls)
		if err := h.do("config", "set", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if len(h.eng.calls) != before {
			t.Errorf("%q: commands were run for a refused value", bad)
		}
	}
}

func TestCommandsNeedAnInstallation(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"user", "list"}, {"backup", "x"}, {"config", "show"}, {"status"}, {"logs"}} {
		if err := h.do(args...); err == nil || !strings.Contains(err.Error(), "install") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if len(h.eng.calls) != 0 {
		t.Errorf("commands were run without an installation: %v", h.eng.calls)
	}
}

func TestStatusAndLogsUseTheEngine(t *testing.T) {
	h := newHarness(t)
	h.install(t)
	if err := h.do("logs", "--tail", "50"); err != nil {
		t.Fatal(err)
	}
	c := h.eng.calls[len(h.eng.calls)-1]
	if !c.has("logs", "--tail", "50") || c.args[len(c.args)-1] != "oblivion" {
		t.Errorf("logs: %v", c.args)
	}
	// The application logs to standard error; `logs | grep` must see it.
	if !c.merged {
		t.Error("the container's standard error is not merged into the output")
	}
	if err := h.do("logs", "--tail", "50; rm"); err == nil {
		t.Error("a non-numeric tail was accepted")
	}
	h.eng.calls = nil
	if err := h.do("status"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "http://127.0.0.1:8080") {
		t.Errorf("status output: %q", h.out)
	}
}
