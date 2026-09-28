package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gotd/td/telegram"
	"golang.org/x/term"

	"gitlab.com/joeunderwood/tgbp/internal/tgclient"
)

func cmdLogin(ctx context.Context, env Env) int {
	st, err := loadSetup(env, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbp login:", err)
		return 1
	}
	readPassword := func() (string, error) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(env.Stdout)
		return string(b), err
	}
	err = tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.Login(ctx, c, env.Stdin, env.Stdout, readPassword); err != nil {
			return err
		}
		id, err := tgclient.SelfID(ctx, c.API())
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "logged in as user %d; session saved to %s\n", id, st.paths.Session())
		return nil
	})
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbp login:", err)
		return 1
	}
	return 0
}
