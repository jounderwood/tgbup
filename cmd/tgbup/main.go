package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

var version = "dev"

const usage = `usage: tgbup <command> [flags]

commands:
  login     interactive Telegram login, stores the session
  setup     choose which chats to archive (writes the config)
  sync      download new messages and media
  version   print version
`

type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv}))
}

func run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(env.Stdout, version)
		return 0
	case "login":
		return cmdLogin(ctx, env)
	case "setup":
		return cmdSetup(ctx, env)
	case "sync":
		return cmdSync(ctx, args[1:], env)
	default:
		fmt.Fprintf(env.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
