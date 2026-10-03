package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gotd/td/telegram"
	"golang.org/x/term"

	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

func cmdLogin(ctx context.Context, env Env) int {
	// Login touches only the session, which is separate on the test servers,
	// so it needs no test config of its own.
	st, err := resolveSetup(env, false)
	if err == nil {
		err = st.secrets.RequireTelegram()
	}
	if err == nil {
		err = loginTelegram(ctx, st, env.Stdin, env.Stdout)
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup login:", err)
		return 1
	}
	return 0
}

// loginTelegram signs in interactively; the 2FA password is read from the terminal without echo.
func loginTelegram(ctx context.Context, st setup, in io.Reader, out io.Writer) error {
	fd := int(os.Stdin.Fd())
	readPassword := func() (string, error) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(out)
		return string(b), err
	}
	restore := func() {}
	if saved, err := term.GetState(fd); err == nil {
		restore = func() { _ = term.Restore(fd, saved) }
	}
	opts := st.clientOptions()
	return tgclient.Run(ctx, opts, func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.Login(ctx, c, in, out, readPassword, restore); err != nil {
			return err
		}
		id, err := tgclient.SelfID(ctx, c.API())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "logged in as user %d; session saved to %s\n", id, opts.SessionPath)
		return nil
	})
}

// telegramAuthorized reports whether the saved session is signed in.
func telegramAuthorized(ctx context.Context, st setup) (bool, error) {
	var ok bool
	err := tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		s, err := c.Auth().Status(ctx)
		ok = err == nil && s.Authorized
		return err
	})
	return ok, err
}
