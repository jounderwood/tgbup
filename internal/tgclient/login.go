package tgclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

type terminalAuth struct {
	in           *bufio.Reader
	out          io.Writer
	readPassword func() (string, error)
	// restore puts the terminal back to normal when an interrupt abandons a
	// password prompt, which otherwise leaves echo switched off.
	restore func()
}

// interruptible runs a blocking terminal read and gives up when ctx ends
// (Ctrl+C). Each read gets its own goroutine so two reads never compete for stdin.
func interruptible(ctx context.Context, read func() (string, error)) (string, error) {
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := read()
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a terminalAuth) prompt(ctx context.Context, q string) (string, error) {
	fmt.Fprint(a.out, q)
	return interruptible(ctx, func() (string, error) {
		s, err := a.in.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || s == "") {
			return "", err
		}
		return strings.TrimSpace(s), nil
	})
}

func (a terminalAuth) Phone(ctx context.Context) (string, error) {
	return a.prompt(ctx, "Phone number (international format, e.g. +79990001122): ")
}

func (a terminalAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return a.prompt(ctx, "Login code from Telegram: ")
}

func (a terminalAuth) Password(ctx context.Context) (string, error) {
	fmt.Fprint(a.out, "2FA password: ")
	pass, err := interruptible(ctx, a.readPassword)
	if ctx.Err() != nil && a.restore != nil {
		a.restore()
	}
	return pass, err
}

func (a terminalAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("no Telegram account for this number: sign up in an official app first")
}

func (a terminalAuth) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	return &auth.SignUpRequired{TermsOfService: tos}
}

// Login runs the interactive sign-in; restore, if set, resets the terminal
// when Ctrl+C interrupts the hidden password prompt.
func Login(ctx context.Context, c *telegram.Client, in io.Reader, out io.Writer, readPassword func() (string, error), restore func()) error {
	flow := auth.NewFlow(terminalAuth{in: bufio.NewReader(in), out: out, readPassword: readPassword, restore: restore}, auth.SendCodeOptions{})
	return c.Auth().IfNecessary(ctx, flow)
}
