// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"oblivion/internal/brand"
	"oblivion/internal/config"
	"oblivion/internal/db"
	"oblivion/internal/plugins"
	"oblivion/plugins/auth"
)

const userUsage = `usage:
  %[1]s user list
  %[1]s user create <login> <admin|editor|author>   (password on standard input)
  %[1]s user reset-password <login>                 (new password on standard input)
  %[1]s user disable <login>
  %[1]s user enable <login>`

// runUser manages accounts in the data directory. It is meant to be run by the
// operator's tooling, in the same image as the server and against the same
// volume: the password is read from standard input (never from an argument,
// which any local user can see in the process list).
func runUser(cfg config.Config, args []string) error {
	usage := fmt.Errorf(userUsage, brand.Slug)
	if len(args) == 0 {
		return usage
	}
	cmd, rest := args[0], args[1:]
	want := map[string]int{"list": 0, "create": 2, "reset-password": 1, "disable": 1, "enable": 1}
	n, known := want[cmd]
	if !known || len(rest) != n {
		return usage
	}
	var password string
	if cmd == "create" || cmd == "reset-password" {
		var err error
		if password, err = readPassword(os.Stdin); err != nil {
			return err
		}
	}

	ctx := context.Background()
	if cmd != "create" {
		if _, err := os.Stat(cfg.DBPath()); err != nil {
			if cmd == "list" {
				return nil // a volume that was never used has no accounts; do not create a database for that
			}
			return fmt.Errorf("no database in %s: nothing to manage yet", cfg.DataDir)
		}
	}
	d, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer d.Close()
	// On a fresh volume the server has not created the accounts schema yet.
	if err := plugins.MigratePlugin(ctx, d, auth.New()); err != nil {
		return err
	}
	a := auth.NewAdmin(d)

	switch cmd {
	case "list":
		users, err := a.List(ctx)
		if err != nil {
			return err
		}
		for _, u := range users {
			state := "active"
			if u.Disabled {
				state = "disabled"
			}
			fmt.Printf("%d\t%s\t%s\t%s\t%s\n", u.ID, u.Username, u.Role, state, time.Unix(u.CreatedAt, 0).UTC().Format(time.RFC3339))
		}
		return nil
	case "create":
		err = a.Create(ctx, rest[0], password, rest[1])
	case "reset-password":
		err = a.ResetPassword(ctx, rest[0], password)
	case "disable":
		err = a.SetDisabled(ctx, rest[0], true)
	case "enable":
		err = a.SetDisabled(ctx, rest[0], false)
	}
	if err != nil {
		return err
	}
	switch cmd {
	case "create":
		fmt.Printf("user %s created\n", rest[0])
	case "reset-password":
		fmt.Printf("password of %s changed; its sessions were ended\n", rest[0])
	case "disable":
		fmt.Printf("%s disabled; its sessions were ended\n", rest[0])
	case "enable":
		fmt.Printf("%s enabled\n", rest[0])
	}
	return nil
}

// readPassword reads one line from r. A terminal is refused: the password would
// be echoed on screen and end up in the scrollback; the host helper pipes it in.
func readPassword(f *os.File) (string, error) {
	if st, err := f.Stat(); err != nil || st.Mode()&os.ModeCharDevice != 0 {
		return "", errors.New("the password is read from standard input and must be piped in, not typed here (use the helper, or: printf '%s\\n' \"$PASSWORD\" | ...)")
	}
	line, err := bufio.NewReader(io.LimitReader(f, 1024)).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", errors.New("no password on standard input")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
