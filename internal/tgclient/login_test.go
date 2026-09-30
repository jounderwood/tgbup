package tgclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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

// Ctrl+C cancels ctx; a prompt blocked on the terminal must give up at once.
func TestTerminalAuthStopsOnInterrupt(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	restored := false
	a := terminalAuth{
		in:           bufio.NewReader(pr),
		out:          io.Discard,
		readPassword: func() (string, error) { select {} },
		restore:      func() { restored = true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 2)
	go func() { _, err := a.Phone(ctx); done <- err }()
	go func() { _, err := a.Password(ctx); done <- err }()
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("prompt ignores interrupt")
		}
	}
	if !restored {
		t.Error("terminal state not restored after an interrupted password prompt")
	}
}
