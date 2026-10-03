package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/config"
)

const (
	maxLogSize = 10 << 20
	keptLogs   = 5
)

// logTee is a command's stderr: the console as it is, and logs/tgbup.log
// line by line with the time and the command. A progress line redrawn with
// \r reaches the file once, as last drawn.
type logTee struct {
	console *os.File
	file    *os.File
	command string
	mu      sync.Mutex
	line    []byte
}

// openLogTee starts logs/tgbup.log for the command unless the config turns
// it off; nil when the file cannot be written: the console still has it all.
func openLogTee(args []string, getenv func(string) string) *logTee {
	p, err := config.ResolvePaths(getenv)
	if err != nil {
		return nil
	}
	if cfg, err := config.LoadWithoutTargets(p.Config, p.Home); err == nil && !cfg.Log.File {
		return nil
	}
	if err := os.MkdirAll(p.Logs, 0o700); err != nil {
		return nil
	}
	path := filepath.Join(p.Logs, "tgbup.log")
	rotateLog(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	command := "tgbup"
	if len(args) > 0 {
		command = args[0]
	}
	return &logTee{console: os.Stderr, file: f, command: command}
}

func (t *logTee) Write(b []byte) (int, error) {
	n, err := t.console.Write(b)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range b {
		switch c {
		case '\n':
			t.flushLine()
		case '\r':
			t.line = t.line[:0]
		default:
			t.line = append(t.line, c)
		}
	}
	return n, err
}

func (t *logTee) flushLine() {
	if line := bytes.TrimSpace(t.line); len(line) > 0 {
		fmt.Fprintf(t.file, "%s %s: %s\n", time.Now().Format("2006-01-02 15:04:05"), t.command, line)
	}
	t.line = t.line[:0]
}

// Close writes a last unfinished line.
func (t *logTee) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.flushLine()
	t.file.Close()
}

// rotateLog moves a log of maxLogSize or more to .1, .1 to .2 and so on,
// keeping keptLogs old files.
func rotateLog(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < maxLogSize {
		return
	}
	for i := keptLogs - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}

// maxRunLog bounds the warnings and errors a run record keeps.
const maxRunLog = 200

// warnLines collects the warnings and errors a run logs, for its record.
type warnLines struct {
	mu  sync.Mutex
	out []string
}

func (w *warnLines) wrap(h slog.Handler) slog.Handler { return warnHandler{Handler: h, w: w} }

func (w *warnLines) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.out...)
}

type warnHandler struct {
	slog.Handler
	w     *warnLines
	attrs []slog.Attr
}

func (h warnHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		var b strings.Builder
		b.WriteString(r.Message)
		add := func(a slog.Attr) bool { fmt.Fprintf(&b, " %s=%v", a.Key, a.Value); return true }
		for _, a := range h.attrs {
			add(a)
		}
		r.Attrs(add)
		h.w.mu.Lock()
		if len(h.w.out) < maxRunLog {
			h.w.out = append(h.w.out, b.String())
		}
		h.w.mu.Unlock()
	}
	return h.Handler.Handle(ctx, r)
}

func (h warnHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return warnHandler{Handler: h.Handler.WithAttrs(as), w: h.w, attrs: append(append([]slog.Attr(nil), h.attrs...), as...)}
}

func (h warnHandler) WithGroup(name string) slog.Handler {
	return warnHandler{Handler: h.Handler.WithGroup(name), w: h.w, attrs: h.attrs}
}
