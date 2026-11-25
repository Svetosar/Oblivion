// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/term"

	"oblivion/internal/brand"
)

// execRunner starts real processes, without a shell.
type execRunner struct{}

func (execRunner) run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", ctlName, err)
		os.Exit(1)
	}
	a := &app{
		runner:   execRunner{},
		in:       os.Stdin,
		out:      os.Stdout,
		errw:     os.Stderr,
		stateDir: filepath.Join(cfg, brand.Slug),
		lookPath: exec.LookPath,
		health:   httpHealth,
		now:      time.Now,
		wait:     time.Sleep,
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		a.askPassword = func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
	}
	if err := a.dispatch(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", ctlName, err)
		os.Exit(1)
	}
}
