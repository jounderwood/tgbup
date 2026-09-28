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
}

func (a terminalAuth) prompt(q string) (string, error) {
	fmt.Fprint(a.out, q)
	s, err := a.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || s == "") {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (a terminalAuth) Phone(context.Context) (string, error) {
	return a.prompt("Phone number (international format, e.g. +79990001122): ")
}

func (a terminalAuth) Code(context.Context, *tg.AuthSentCode) (string, error) {
	return a.prompt("Login code from Telegram: ")
}

func (a terminalAuth) Password(context.Context) (string, error) {
	fmt.Fprint(a.out, "2FA password: ")
	return a.readPassword()
}

func (a terminalAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("no Telegram account for this number: sign up in an official app first")
}

func (a terminalAuth) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	return &auth.SignUpRequired{TermsOfService: tos}
}

func Login(ctx context.Context, c *telegram.Client, in io.Reader, out io.Writer, readPassword func() (string, error)) error {
	flow := auth.NewFlow(terminalAuth{in: bufio.NewReader(in), out: out, readPassword: readPassword}, auth.SendCodeOptions{})
	return c.Auth().IfNecessary(ctx, flow)
}
