package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/serve"
)

func TestTailFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	if got, err := tailFile(path, 1024, 3); err != nil || got != "" {
		t.Fatalf("missing log: %q %v", got, err)
	}
	var b strings.Builder
	for i := range 100 {
		b.WriteString(strings.Repeat("x", 10) + string(rune('a'+i%26)) + "\n")
	}
	_ = os.WriteFile(path, []byte(b.String()), 0o600)
	got, _ := tailFile(path, 1024, 3)
	if lines := strings.Split(got, "\n"); len(lines) != 3 || lines[2] != "xxxxxxxxxxv" {
		t.Errorf("last 3 lines = %q", got)
	}
	got, _ = tailFile(path, 30, 100)
	for _, l := range strings.Split(got, "\n") {
		if len(l) != 11 {
			t.Errorf("a line cut in half came through: %q", got)
		}
	}
}

func TestNextRun(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	for now, want := range map[string]string{
		"2026-10-03 05:00": "2026-10-03 06:30",
		"2026-10-03 06:30": "2026-10-03 12:30",
		"2026-10-03 19:00": "2026-10-04 00:30",
	} {
		n, _ := time.ParseInLocation("2006-01-02 15:04", now, loc)
		if got := nextRun(n).Format("2006-01-02 15:04"); got != want {
			t.Errorf("%s: %s, want %s", now, got, want)
		}
	}
}

func TestSetupProblems(t *testing.T) {
	st := setup{cfg: config.Default()}
	got := setupProblems(st, serve.Status{})
	if len(got) != 3 || !strings.Contains(got[0], "tgbup init") {
		t.Errorf("empty setup: %q", got)
	}
	// Cloud backup is optional: not having it is not a problem.
	st.secrets = config.Secrets{APIID: 1, APIHash: "h"}
	st.cfg.Targets.Include = []config.PeerRef{"1"}
	if got := setupProblems(st, serve.Status{Schedule: serve.Schedule{Installed: true}}); len(got) != 0 {
		t.Errorf("complete setup: %q", got)
	}
}

// A run must not skip because serve was checking the lock at that instant.
func TestRunWaitsOutAStatusProbe(t *testing.T) {
	lockPatience = time.Second
	defer func() { lockPatience = 0 }()
	path := filepath.Join(t.TempDir(), "run.lock")
	probe, err := tryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		probe()
	}()
	release, err := acquireLock(path)
	if err != nil {
		t.Fatalf("run skipped over a probe: %v", err)
	}
	if !lockHeld(path) {
		t.Error("a held lock is not reported as a run in progress")
	}
	release()
	if lockHeld(path) {
		t.Error("a free lock is reported as busy")
	}
}
