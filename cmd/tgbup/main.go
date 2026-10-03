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

Start with tgbup init: it sets everything up step by step.

commands:
  init      set everything up step by step (run again any time to continue or change)
  login     interactive Telegram login, stores the session
  setup     choose which chats to archive (writes the config)
  sync      download new messages and media
  run       sync, back up and notify: what the schedule starts
  backup    snapshot the archive into the restic repositories (--init: create them)
  deps      find or download restic and rclone
  notify    notify test: send a test message through the alerts bot
  schedule  install|uninstall|status: start tgbup run 4 times a day (launchd)
  render    redraw html/ and md/ from the archive (--full: every page)
  export    pack a chat, topic or period with its media into a zip
  serve     web page on this computer: setup, status, update now, chat list, the archive (--open)
  restore   bring the archive back from a backup (--from, --snapshot, --list)
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
	var stderr io.Writer = os.Stderr
	tee := openLogTee(os.Args[1:], os.Getenv)
	if tee != nil {
		stderr = tee
	}
	code := run(ctx, os.Args[1:], Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: stderr, Getenv: os.Getenv})
	if tee != nil {
		tee.Close()
	}
	os.Exit(code)
}

func run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usage)
		return 0
	case "version":
		fmt.Fprintln(env.Stdout, version)
		return 0
	case "init":
		return cmdInit(ctx, env)
	case "login":
		return cmdLogin(ctx, env)
	case "setup":
		return cmdSetup(ctx, env)
	case "sync":
		return cmdSync(ctx, args[1:], env)
	case "run":
		return cmdRun(ctx, env)
	case "backup":
		return cmdBackup(ctx, args[1:], env)
	case "deps":
		return cmdDeps(ctx, env)
	case "notify":
		return cmdNotify(ctx, args[1:], env)
	case "schedule":
		return cmdSchedule(args[1:], env)
	case "render":
		return cmdRender(ctx, args[1:], env)
	case "export":
		return cmdExport(ctx, args[1:], env)
	case "serve":
		return cmdServe(ctx, args[1:], env)
	case "restore":
		return cmdRestore(ctx, args[1:], env)
	default:
		fmt.Fprintf(env.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
