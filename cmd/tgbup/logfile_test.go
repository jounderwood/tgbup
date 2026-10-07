package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeunderwood/tgbup/internal/config"
)

func TestLogTeeWritesLinesToTheFile(t *testing.T) {
	home := t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
	tee := openLogTee([]string{"sync", "--dry-run"}, getenv)
	if tee == nil {
		t.Fatal("no log file")
	}
	devnull, _ := os.Open(os.DevNull)
	tee.console = devnull
	fmt.Fprint(tee, "media 1/3\rmedia 2/3\rmedia 3/3\nfirst line\n")
	fmt.Fprint(tee, "\r\033[Kcounting 12/50")
	fmt.Fprint(tee, "time=x level=WARN msg=hm\n")
	fmt.Fprint(tee, "unfinished")
	tee.Close()
	b, err := os.ReadFile(filepath.Join(home, "Tgbup", "logs", "tgbup.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 || !strings.HasSuffix(lines[0], " sync: media 3/3") ||
		!strings.HasSuffix(lines[1], " sync: first line") || !strings.HasSuffix(lines[2], " sync: time=x level=WARN msg=hm") ||
		!strings.HasSuffix(lines[3], " sync: unfinished") {
		t.Fatalf("log =\n%s", b)
	}
}

func TestLogFileCanBeTurnedOff(t *testing.T) {
	home := t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
	p, _ := config.ResolvePaths(getenv)
	if err := config.SetValue(p.Config, []string{"log", "file"}, false); err != nil {
		t.Fatal(err)
	}
	if tee := openLogTee([]string{"run"}, getenv); tee != nil {
		tee.Close()
		t.Fatal("log.file: false still writes the file")
	}
}

func TestRotateLogKeepsFiveOld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tgbup.log")
	for i := range 7 {
		if err := os.WriteFile(path, []byte(fmt.Sprint(i)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, maxLogSize); err != nil {
			t.Fatal(err)
		}
		rotateLog(path)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a full log was not moved")
	}
	for i := 1; i <= keptLogs; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", path, i)); err != nil {
			t.Errorf("%s.%d missing", path, i)
		}
	}
	if _, err := os.Stat(fmt.Sprintf("%s.%d", path, keptLogs+1)); err == nil {
		t.Error("more than five old logs kept")
	}
	small := filepath.Join(t.TempDir(), "run.log")
	_ = os.WriteFile(small, []byte("x"), 0o600)
	if rotateLog(small); !isFile(small) {
		t.Error("a small log was moved")
	}
}

func TestWarnLinesKeepTheRunsProblems(t *testing.T) {
	w := &warnLines{}
	log := slog.New(w.wrap(slog.NewTextHandler(discard{}, nil))).With("chat", 5)
	log.Info("archiving 3 chats")
	log.Warn("include not found", "name", "@gone")
	log.Error("sync failed", "err", "boom")
	got := w.lines()
	if len(got) != 2 || got[0] != "include not found chat=5 name=@gone" || got[1] != "sync failed chat=5 err=boom" {
		t.Fatalf("lines = %q", got)
	}
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }
