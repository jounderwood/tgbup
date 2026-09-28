package tgclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

func TestTerminalAuthPrompts(t *testing.T) {
	var out bytes.Buffer
	a := terminalAuth{
		in:           bufio.NewReader(strings.NewReader(" +79990001122 \n 12345\n")),
		out:          &out,
		readPassword: func() (string, error) { return "secret", nil },
	}
	ctx := context.Background()

	phone, err := a.Phone(ctx)
	if err != nil || phone != "+79990001122" {
		t.Fatalf("phone = %q %v", phone, err)
	}
	code, err := a.Code(ctx, &tg.AuthSentCode{})
	if err != nil || code != "12345" {
		t.Fatalf("code = %q %v", code, err)
	}
	pass, err := a.Password(ctx)
	if err != nil || pass != "secret" {
		t.Fatalf("password = %q %v", pass, err)
	}
	for _, want := range []string{"Phone number", "Login code", "2FA password"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt %q missing in %q", want, out.String())
		}
	}
	if _, err := a.SignUp(ctx); err == nil {
		t.Error("SignUp must refuse")
	}
	var su *auth.SignUpRequired
	if err := a.AcceptTermsOfService(ctx, tg.HelpTermsOfService{}); !errors.As(err, &su) {
		t.Errorf("AcceptTermsOfService = %v", err)
	}
}
