package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gotd/td/telegram"
	"golang.org/x/term"

	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

func cmdLogin(ctx context.Context, env Env) int {
	st, err := loadSetup(env, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup login:", err)
		return 1
	}
	fd := int(os.Stdin.Fd())
	readPassword := func() (string, error) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(env.Stdout)
		return string(b), err
	}
	restore := func() {}
	if saved, err := term.GetState(fd); err == nil {
		restore = func() { _ = term.Restore(fd, saved) }
	}
	opts := st.clientOptions()
	err = tgclient.Run(ctx, opts, func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.Login(ctx, c, env.Stdin, env.Stdout, readPassword, restore); err != nil {
			return err
		}
		id, err := tgclient.SelfID(ctx, c.API())
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "logged in as user %d; session saved to %s\n", id, opts.SessionPath)
		return nil
	})
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup login:", err)
		return 1
	}
	return 0
}
