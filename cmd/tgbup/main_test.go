package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testEnv() (Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return Env{
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
		Getenv: func(string) string { return "" },
	}, &out, &errOut
}

func TestRunWithoutArgsPrintsUsage(t *testing.T) {
	env, _, errOut := testEnv()
	if code := run(context.Background(), nil, env); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage: tgbup") {
		t.Fatalf("usage not printed: %q", errOut.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	env, _, errOut := testEnv()
	if code := run(context.Background(), []string{"nope"}, env); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), `unknown command "nope"`) {
		t.Fatalf("got %q", errOut.String())
	}
}

func TestRunVersion(t *testing.T) {
	env, out, _ := testEnv()
	if code := run(context.Background(), []string{"version"}, env); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.TrimSpace(out.String()) != "dev" {
		t.Fatalf("got %q", out.String())
	}
}
