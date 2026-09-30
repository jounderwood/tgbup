package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/syncer"
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
