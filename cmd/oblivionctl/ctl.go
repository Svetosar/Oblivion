// SPDX-License-Identifier: AGPL-3.0-or-later

// Command oblivionctl is the operator's helper on the host. It installs and
// configures the container, manages accounts (including a lost password),
// takes backups and shows status and logs.
//
// It never touches the application's database itself: schema and rules belong to
// the application, and a rootless container's volume is owned by another uid.
// Every action on data runs as a one-shot container of the same image against the
// same volume. Passwords go through standard input only, never through arguments
// (any local user can read those in the process list). Commands are run without a
// shell, and every value that reaches a command line is validated first.
//
// Whoever can run this tool already controls the server: run it as the user that
// owns the containers (rootless podman), not as root.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"oblivion/internal/brand"
)

const (
	ctlName       = brand.Slug + "ctl"
	containerName = brand.Slug
	volumeName    = brand.Slug + "-data"
	defaultImage  = "localhost/" + brand.Slug + ":dev"
	defaultPort   = 8080
)

// runner executes a command. The real one starts a process; tests record calls.
type runner interface {
	run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

type app struct {
	runner   runner
	in       io.Reader
	out      io.Writer
	errw     io.Writer
	stateDir string

	lookPath     func(string) (string, error)
	health       func(ctx context.Context, port int) error
	now          func() time.Time
	wait         func(time.Duration)
	askPassword  func(prompt string) (string, error) // nil when no terminal is available
	healthTries  int
	healthPeriod time.Duration
}

// state is what the helper remembers between runs. Everything the container
// needs is derived from it, so a changed setting means "recreate".
type state struct {
	Engine string            `json:"engine"`
	Image  string            `json:"image"`
	Port   int               `json:"port"`
	Env    map[string]string `json:"env"`
}

func (a *app) statePath() string { return filepath.Join(a.stateDir, "ctl.json") }

var errNotInstalled = fmt.Errorf("not installed yet: run `%s install` first", ctlName)

func (a *app) loadState() (*state, error) {
	b, err := os.ReadFile(a.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotInstalled
	}
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", a.statePath(), err)
	}
	if !validEngine(st.Engine) || !validImage(st.Image) || !validPort(st.Port) {
		return nil, fmt.Errorf("%s holds values that are not valid; remove it only if you know the container is gone", a.statePath())
	}
	return &st, nil
}

func (a *app) saveState(st *state) error {
	if err := os.MkdirAll(a.stateDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := a.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.statePath())
}

// ---- validation: everything that reaches a command line passes through here ----

var (
	imageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,200}$`)
	loginRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,31}$`)
	proxyRe = regexp.MustCompile(`^[0-9A-Fa-f:./,]{0,200}$`)
)

func validEngine(e string) bool { return e == "podman" || e == "docker" }
func validImage(i string) bool  { return imageRe.MatchString(i) }
func validPort(p int) bool      { return p >= 1 && p <= 65535 }

// settable are the environment settings the helper manages. Values are checked
// here, so nothing else can be smuggled into the container's command line.
var settable = map[string]func(string) error{
	"SECURE_COOKIE": func(v string) error {
		if v != "true" && v != "false" {
			return errors.New("must be true or false")
		}
		return nil
	},
	"TRUSTED_PROXIES": func(v string) error {
		if !proxyRe.MatchString(v) {
			return errors.New("must be a comma-separated list of IP addresses or CIDR ranges")
		}
		return nil
	},
	"LOG_LEVEL": func(v string) error {
		switch v {
		case "debug", "info", "warn", "error":
			return nil
		}
		return errors.New("must be debug, info, warn or error")
	},
}

// ---- running things ----

func (a *app) engineRun(ctx context.Context, st *state, stdin io.Reader, stdout io.Writer, args ...string) error {
	return a.runner.run(ctx, stdin, stdout, a.errw, st.Engine, args...)
}

// probe is engineRun for questions whose "no" is not worth printing.
func (a *app) probe(ctx context.Context, st *state, args ...string) error {
	return a.runner.run(ctx, nil, nil, nil, st.Engine, args...)
}

// hardening is applied to every container the helper starts.
var hardening = []string{"--read-only", "--cap-drop=ALL", "--security-opt", "no-new-privileges"}

// oneShot runs a command of the application image against the data volume, in a
// container that disappears afterwards. -i keeps standard input open for passwords.
func (a *app) oneShot(ctx context.Context, st *state, stdin io.Reader, stdout io.Writer, appArgs ...string) error {
	return a.oneShotOn(ctx, st, volumeName, stdin, stdout, appArgs...)
}

func (a *app) oneShotOn(ctx context.Context, st *state, volume string, stdin io.Reader, stdout io.Writer, appArgs ...string) error {
	args := append([]string{"run", "--rm", "-i"}, hardening...)
	args = append(args, "-v", volume+":/data", st.Image)
	return a.engineRun(ctx, st, stdin, stdout, append(args, appArgs...)...)
}

func (st *state) serverArgs() []string {
	args := []string{"run", "-d", "--name", containerName, "--restart", "unless-stopped"}
	args = append(args, hardening...)
	args = append(args, "--memory", "512m", "--cpus", "1",
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", st.Port), "-v", volumeName+":/data")
	keys := make([]string, 0, len(st.Env))
	for k := range st.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", brand.EnvPrefix+k+"="+st.Env[k])
	}
	return append(args, st.Image)
}

// startServer starts the container and waits until it answers. On failure it
// removes the container, so that the next attempt starts clean.
func (a *app) startServer(ctx context.Context, st *state) error {
	if err := a.engineRun(ctx, st, nil, nil, st.serverArgs()...); err != nil {
		return fmt.Errorf("start the container: %w", err)
	}
	tries, period := a.healthTries, a.healthPeriod
	if tries == 0 {
		tries, period = 30, time.Second
	}
	var last error
	for i := 0; i < tries; i++ {
		if last = a.health(ctx, st.Port); last == nil {
			return nil
		}
		a.wait(period)
	}
	_ = a.engineRun(ctx, st, nil, a.errw, "logs", "--tail", "30", containerName)
	_ = a.engineRun(ctx, st, nil, nil, "rm", "-f", containerName)
	return fmt.Errorf("the server did not become healthy (%v); its last log lines are above", last)
}

// recreate replaces the running container (stop, remove, start) with one built
// from st. If the new one does not come up, the previous one is restored.
func (a *app) recreate(ctx context.Context, old, st *state) error {
	_ = a.engineRun(ctx, st, nil, nil, "stop", "-t", "20", containerName)
	_ = a.engineRun(ctx, st, nil, nil, "rm", "-f", containerName)
	if err := a.startServer(ctx, st); err != nil {
		fmt.Fprintf(a.errw, "restoring the previous settings...\n")
		if rerr := a.startServer(ctx, old); rerr != nil {
			return fmt.Errorf("%w; and the previous container did not start either: %v", err, rerr)
		}
		return err
	}
	return nil
}

// ---- passwords ----

// readPassword returns the password for login: one line of standard input with
// --password-stdin, otherwise typed twice without echo.
func (a *app) readPassword(login string, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(io.LimitReader(a.in, 1024)).ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			return "", errors.New("no password on standard input")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	if a.askPassword == nil {
		return "", errors.New("no terminal: pipe the password in and add --password-stdin")
	}
	first, err := a.askPassword("Password for " + login + ": ")
	if err != nil {
		return "", err
	}
	second, err := a.askPassword("Repeat the password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("the passwords differ")
	}
	return first, nil
}

// ---- commands ----

func (a *app) dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintf(a.out, usage, ctlName, ctlName, ctlName, ctlName, ctlName, ctlName, ctlName, ctlName, ctlName, ctlName)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "install":
		return a.install(ctx, rest)
	case "user":
		return a.user(ctx, rest)
	case "backup":
		return a.backup(ctx, rest)
	case "restore":
		return a.restore(ctx, rest)
	case "update":
		return a.update(ctx, rest)
	case "config":
		return a.config(ctx, rest)
	case "status":
		return a.status(ctx)
	case "logs":
		return a.logs(ctx, rest)
	}
	return fmt.Errorf("unknown command %q; run `%s help`", cmd, ctlName)
}

const usage = `usage:
  %s install [--image REF] [--port N] [--engine podman|docker] [--admin LOGIN] [--proxy IPS] [--insecure-http] [--password-stdin]
  %s user list
  %s user create LOGIN admin|editor|author [--password-stdin]
  %s user reset-password LOGIN [--password-stdin]
  %s user disable LOGIN | enable LOGIN
  %s backup [FILE]
  %s restore FILE [--replace]            (--replace: overwrite existing data; it is saved first)
  %s update [--image REF]                (backs up first; restores the old container if the new one does not start)
  %s config show | set KEY=VALUE...      keys: SECURE_COOKIE, TRUSTED_PROXIES, LOG_LEVEL
  %s status | logs [--tail N] [-f]
`

func (a *app) detectEngine(want string) (string, error) {
	if want != "" {
		if !validEngine(want) {
			return "", fmt.Errorf("unknown engine %q (podman or docker)", want)
		}
		if _, err := a.lookPath(want); err != nil {
			return "", fmt.Errorf("%s is not installed", want)
		}
		return want, nil
	}
	for _, e := range []string{"podman", "docker"} {
		if _, err := a.lookPath(e); err == nil {
			return e, nil
		}
	}
	return "", errors.New("neither podman nor docker was found in PATH; install podman first")
}

func (a *app) install(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	image := fs.String("image", defaultImage, "image of the application")
	port := fs.Int("port", defaultPort, "port on 127.0.0.1")
	engine := fs.String("engine", "", "podman or docker (default: whichever is installed, podman first)")
	admin := fs.String("admin", "admin", "login of the first administrator")
	proxy := fs.String("proxy", "", "reverse proxies whose X-Forwarded-For is trusted (IPs or CIDRs, comma-separated)")
	insecure := fs.Bool("insecure-http", false, "serve plain HTTP: cookies are not marked Secure (local testing only)")
	pwStdin := fs.Bool("password-stdin", false, "read the administrator's password from standard input")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if !validImage(*image) {
		return fmt.Errorf("image %q is not a valid image reference", *image)
	}
	if !validPort(*port) {
		return fmt.Errorf("port %d is out of range", *port)
	}
	if !loginRe.MatchString(*admin) {
		return errors.New("login: 3-32 characters, letters, digits, . _ -, starting with a letter or digit")
	}
	if err := settable["TRUSTED_PROXIES"](*proxy); err != nil {
		return fmt.Errorf("--proxy: %w", err)
	}
	if _, err := a.loadState(); err == nil {
		return fmt.Errorf("already installed (settings in %s); use `%s config`, `%s status`", a.statePath(), ctlName, ctlName)
	} else if !errors.Is(err, errNotInstalled) {
		return err
	}
	eng, err := a.detectEngine(*engine)
	if err != nil {
		return err
	}
	st := &state{Engine: eng, Image: *image, Port: *port, Env: map[string]string{"SECURE_COOKIE": "true"}}
	if *insecure {
		st.Env["SECURE_COOKIE"] = "false"
	}
	if *proxy != "" {
		st.Env["TRUSTED_PROXIES"] = *proxy
	}

	step := func(format string, v ...any) { fmt.Fprintf(a.out, "• "+format+"\n", v...) }
	step("checking the image %s", st.Image)
	if err := a.probe(ctx, st, "image", "inspect", st.Image); err != nil {
		return fmt.Errorf("image %s not found (build it with `make image`, or pass --image): %w", st.Image, err)
	}
	step("preparing the data volume %s", volumeName)
	if a.probe(ctx, st, "volume", "inspect", volumeName) != nil {
		if err := a.engineRun(ctx, st, nil, nil, "volume", "create", volumeName); err != nil {
			return fmt.Errorf("create the volume: %w", err)
		}
	}

	var list strings.Builder
	if err := a.oneShot(ctx, st, nil, &list, "user", "list"); err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}
	if strings.TrimSpace(list.String()) != "" {
		step("the volume already has accounts: keeping them")
	} else {
		pw, err := a.readPassword(*admin, *pwStdin)
		if err != nil {
			return err
		}
		step("creating the administrator %s", *admin)
		if err := a.oneShot(ctx, st, strings.NewReader(pw+"\n"), a.out, "user", "create", *admin, "admin"); err != nil {
			return fmt.Errorf("create the administrator: %w", err)
		}
	}

	step("starting the server")
	if err := a.startServer(ctx, st); err != nil {
		return err
	}
	if err := a.saveState(st); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "\nThe server answers on http://127.0.0.1:%d (admin panel: /admin/).\n", st.Port)
	if *insecure {
		fmt.Fprintln(a.out, "Plain HTTP: cookies are not marked Secure. Do not expose this to the internet.")
	} else {
		fmt.Fprintln(a.out, "Put a reverse proxy with HTTPS in front of it: the session cookie is marked Secure and\n"+
			"does not work over plain HTTP. Tell the application which proxy to trust:\n  "+ctlName+" config set TRUSTED_PROXIES=<proxy address>")
	}
	if eng == "podman" {
		fmt.Fprintln(a.out, "To start the container after a reboot: systemctl --user enable --now podman-restart.service\n"+
			"(and `loginctl enable-linger $USER` so it runs without a login session).")
	}
	return nil
}

// user passes account commands to a one-shot container.
func (a *app) user(ctx context.Context, args []string) error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	var pos []string
	pwStdin := false
	for _, x := range args {
		if x == "--password-stdin" {
			pwStdin = true
			continue
		}
		pos = append(pos, x)
	}
	if len(pos) == 0 {
		return errors.New("usage: user list | create LOGIN ROLE | reset-password LOGIN | disable LOGIN | enable LOGIN")
	}
	sub, rest := pos[0], pos[1:]
	need := map[string]int{"list": 0, "create": 2, "reset-password": 1, "disable": 1, "enable": 1}
	n, ok := need[sub]
	if !ok || len(rest) != n {
		return errors.New("usage: user list | create LOGIN ROLE | reset-password LOGIN | disable LOGIN | enable LOGIN")
	}
	if n > 0 && !loginRe.MatchString(rest[0]) {
		return fmt.Errorf("%q is not a valid login", rest[0])
	}
	if sub == "create" && rest[1] != "admin" && rest[1] != "editor" && rest[1] != "author" {
		return errors.New("role must be admin, editor or author")
	}
	var stdin io.Reader
	if sub == "create" || sub == "reset-password" {
		pw, err := a.readPassword(rest[0], pwStdin)
		if err != nil {
			return err
		}
		stdin = strings.NewReader(pw + "\n")
	}
	if err := a.oneShot(ctx, st, stdin, a.out, append([]string{"user", sub}, rest...)...); err != nil {
		return err
	}
	if sub == "reset-password" || sub == "disable" {
		fmt.Fprintln(a.errw, "Note: repeated failed sign-ins slow a login down for a few minutes; that clears by itself.")
	}
	return nil
}

func (a *app) backup(ctx context.Context, args []string) error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return errors.New("usage: backup [FILE]")
	}
	path := fmt.Sprintf("%s-backup-%s.tar.gz", brand.Slug, a.now().UTC().Format("20060102-150405"))
	if len(args) == 1 {
		path = args[0]
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	err = a.oneShot(ctx, st, nil, f, "backup", "-")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("backup failed: %w", err)
	}
	fmt.Fprintf(a.out, "backup written to %s (it contains password hashes: keep it private)\n", path)
	return nil
}

// restore puts a backup back. Nothing is destroyed until the archive has been
// proven good in a throw-away volume, and the data that is replaced is saved
// first.
func (a *app) restore(ctx context.Context, args []string) error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	var path string
	replace := false
	for _, x := range args {
		switch {
		case x == "--replace":
			replace = true
		case path == "" && !strings.HasPrefix(x, "-"):
			path = x
		default:
			return errors.New("usage: restore FILE [--replace]")
		}
	}
	if path == "" {
		return errors.New("usage: restore FILE [--replace]")
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a readable file", path)
	}
	exists := a.probe(ctx, st, "volume", "inspect", volumeName) == nil
	if exists && !replace {
		return fmt.Errorf("a data volume already exists: restoring replaces everything in it. Add --replace to do that; " +
			"the current data is saved to a backup file first")
	}

	feed := func(volume string, out io.Writer) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return a.oneShotOn(ctx, st, volume, f, out, "restore", "-")
	}

	// 1. Prove the archive in a volume nobody uses.
	check := volumeName + "-check"
	if a.probe(ctx, st, "volume", "inspect", check) == nil {
		_ = a.engineRun(ctx, st, nil, nil, "volume", "rm", "-f", check)
	}
	if err := a.engineRun(ctx, st, nil, nil, "volume", "create", check); err != nil {
		return fmt.Errorf("create a test volume: %w", err)
	}
	trial := feed(check, nil) // the trial run is silent: it restores nothing that matters
	_ = a.engineRun(ctx, st, nil, nil, "volume", "rm", "-f", check)
	if trial != nil {
		return fmt.Errorf("this archive cannot be restored; nothing was changed: %w", trial)
	}

	// 2. Stop the server, save what is there, start over with an empty volume.
	fmt.Fprintln(a.out, "• the archive is good; stopping the server")
	_ = a.engineRun(ctx, st, nil, nil, "stop", "-t", "20", containerName)
	_ = a.engineRun(ctx, st, nil, nil, "rm", "-f", containerName)
	saved := ""
	if exists {
		saved = fmt.Sprintf("%s-pre-restore-%s.tar.gz", brand.Slug, a.now().UTC().Format("20060102-150405"))
		if err := a.backup(ctx, []string{saved}); err != nil {
			_ = a.startServer(ctx, st) // put the old server back
			return fmt.Errorf("the current data could not be saved first, so nothing was replaced (if the volume is really empty, remove it with `%s volume rm %s`): %w", st.Engine, volumeName, err)
		}
		if err := a.engineRun(ctx, st, nil, nil, "volume", "rm", "-f", volumeName); err != nil {
			return fmt.Errorf("remove the old volume (your data is in %s): %w", saved, err)
		}
	}
	if err := a.engineRun(ctx, st, nil, nil, "volume", "create", volumeName); err != nil {
		return fmt.Errorf("create the volume: %w", err)
	}
	where := ""
	if saved != "" {
		where = fmt.Sprintf(" The data that was replaced is in %s.", saved)
	}
	if err := feed(volumeName, a.out); err != nil {
		return fmt.Errorf("restore failed.%s: %w", where, err)
	}
	if err := a.startServer(ctx, st); err != nil {
		return fmt.Errorf("the data was restored but the server does not start.%s %w", where, err)
	}
	fmt.Fprintf(a.out, "backup restored; the server is running again.%s\n", where)
	return nil
}

// update switches to another image of the application: a backup first, then the
// container is recreated, and the old container comes back if the new one does not
// start. The database is migrated by the new version when it starts and is not
// migrated back, so after a failed update the backup is what returns the data.
func (a *app) update(ctx context.Context, args []string) error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	image := fs.String("image", st.Image, "image to switch to (default: the same reference, rebuilt or pulled)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !validImage(*image) {
		return errors.New("usage: update [--image REF]")
	}
	if err := a.probe(ctx, st, "image", "inspect", *image); err != nil {
		return fmt.Errorf("image %s not found (build it with `make image`): %w", *image, err)
	}
	saved := fmt.Sprintf("%s-pre-update-%s.tar.gz", brand.Slug, a.now().UTC().Format("20060102-150405"))
	if err := a.backup(ctx, []string{saved}); err != nil {
		return fmt.Errorf("update stopped: the backup before it failed: %w", err)
	}
	next := *st
	next.Image = *image
	if err := a.recreate(ctx, st, &next); err != nil {
		return fmt.Errorf("the new image did not start and the previous container was restored. "+
			"The new version may already have changed the database; if the old one misbehaves, `%s restore %s --replace`: %w", ctlName, saved, err)
	}
	if err := a.saveState(&next); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "updated to %s; the backup from before is %s\n", *image, saved)
	return nil
}

func (a *app) config(ctx context.Context, args []string) error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: config show | config set KEY=VALUE...")
	}
	switch args[0] {
	case "show":
		fmt.Fprintf(a.out, "engine=%s\nimage=%s\nport=%d\n", st.Engine, st.Image, st.Port)
		keys := make([]string, 0, len(st.Env))
		for k := range st.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(a.out, "%s=%s\n", k, st.Env[k])
		}
		return nil
	case "set":
		if len(args) < 2 {
			return errors.New("usage: config set KEY=VALUE...")
		}
		next := *st
		next.Env = map[string]string{}
		for k, v := range st.Env {
			next.Env[k] = v
		}
		for _, kv := range args[1:] {
			k, v, ok := strings.Cut(kv, "=")
			check, known := settable[k]
			if !ok || !known {
				return fmt.Errorf("%q: use KEY=VALUE with a key out of SECURE_COOKIE, TRUSTED_PROXIES, LOG_LEVEL", kv)
			}
			if err := check(v); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			if v == "" {
				delete(next.Env, k)
			} else {
				next.Env[k] = v
			}
		}
		if err := a.recreate(ctx, st, &next); err != nil {
			return err
		}
		if err := a.saveState(&next); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "settings applied; the server was restarted")
		return nil
	}
	return errors.New("usage: config show | config set KEY=VALUE...")
}

func (a *app) status(ctx context.Context) error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	var state strings.Builder
	if err := a.engineRun(ctx, st, nil, &state, "inspect", "--format", "{{.State.Status}}", containerName); err != nil {
		state.Reset()
		state.WriteString("not found")
	}
	health := "healthy"
	if err := a.health(ctx, st.Port); err != nil {
		health = "not answering (" + err.Error() + ")"
	}
	fmt.Fprintf(a.out, "container: %s\nhealth:    %s\naddress:   http://127.0.0.1:%d\nimage:     %s\nengine:    %s\n",
		strings.TrimSpace(state.String()), health, st.Port, st.Image, st.Engine)
	return nil
}

func (a *app) logs(ctx context.Context, args []string) error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	tail := fs.String("tail", "100", "number of lines")
	follow := fs.Bool("f", false, "follow")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if n, err := strconv.Atoi(*tail); err != nil || n < 0 || n > 100000 {
		return errors.New("--tail must be a number")
	}
	cmd := []string{"logs", "--tail", *tail}
	if *follow {
		cmd = append(cmd, "-f")
	}
	// The application writes its log to standard error; merge it so that
	// `logs | grep ...` works.
	return a.runner.run(ctx, nil, a.out, a.out, st.Engine, append(cmd, containerName)...)
}

// httpHealth asks the server's own health endpoint.
func httpHealth(ctx context.Context, port int) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz answered %d", resp.StatusCode)
	}
	return nil
}
