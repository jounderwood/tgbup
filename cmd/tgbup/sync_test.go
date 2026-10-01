package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/syncer"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

func TestUnresolvedIncludeFailsTheRun(t *testing.T) {
	sum := syncSummary{
		sync:       syncer.Report{Chats: 3},
		unresolved: []string{"include @mama_ivanova: chat not found among your dialogs"},
	}
	if sum.ok() {
		t.Fatal("a configured chat that is no longer archived must make the run fail")
	}
	var out bytes.Buffer
	sum.print(&out)
	if !strings.Contains(out.String(), "@mama_ivanova") {
		t.Fatalf("summary does not name the unresolved include: %q", out.String())
	}
}

func TestDryRunWithTakeoutIsAFlagError(t *testing.T) {
	env, _, errOut := testEnv()
	if code := run(context.Background(), []string{"sync", "--dry-run", "--takeout"}, env); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "--dry-run") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestTopicNotesDoNotFailTheRun(t *testing.T) {
	sum := syncSummary{sync: syncer.Report{Chats: 1}, notes: []string{"exclude_topics -1001: chat is not selected"}}
	if !sum.ok() {
		t.Fatal("a stale exclude_topics key failed the run")
	}
	var out bytes.Buffer
	sum.print(&out)
	if !strings.Contains(out.String(), "warning: exclude_topics -1001") {
		t.Fatalf("summary = %q", out.String())
	}
}

func TestUnconfirmedTakeoutPrintsOneLine(t *testing.T) {
	var out, errOut bytes.Buffer
	log := slog.New(slog.NewTextHandler(&errOut, nil))
	err := fmt.Errorf("callback: %w", &tgclient.TakeoutNotConfirmed{Wait: 24 * time.Hour})
	if code := finishSync(&out, log, syncSummary{}, err); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if out.Len() != 0 {
		t.Errorf("empty summary printed for a run that did not start:\n%s", out.String())
	}
	if got := errOut.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "confirm the data export") || strings.Contains(got, "callback") {
		t.Errorf("stderr = %q", got)
	}
}

// Errors are logged by their message; gotd's verbose format adds stack frames.
func TestSyncErrorsAreLoggedWithoutFrames(t *testing.T) {
	var out, errOut bytes.Buffer
	log := slog.New(slog.NewTextHandler(&errOut, nil))
	finishSync(&out, log, syncSummary{}, framed{})
	if strings.Contains(errOut.String(), "connect.go") {
		t.Errorf("stack frames in log: %q", errOut.String())
	}
}

type framed struct{}

func (framed) Error() string { return "callback: boom" }

func (framed) Format(s fmt.State, verb rune) { fmt.Fprint(s, "callback:\n    telegram/connect.go:224\n  - boom") }
