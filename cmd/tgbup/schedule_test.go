package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeLaunchctl struct {
	calls []string
	// busy makes this many bootstrap calls fail, as when launchd is still tearing down the old job.
	busy int
}

func (f *fakeLaunchctl) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == "bootstrap" && f.busy > 0 {
		f.busy--
		return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
	}
	if args[0] == "print" {
		return []byte("state = not running\nlast exit code = 0\n"), nil
	}
	return nil, nil
}

func testSchedule(t *testing.T) (launchdJob, *fakeLaunchctl) {
	t.Helper()
	f := &fakeLaunchctl{}
	dir := t.TempDir()
	return launchdJob{
		Label: "com.example.tgbup", Exe: "/Users/me/.local/bin/tgbup", AgentsDir: filepath.Join(dir, "LaunchAgents"),
		LogPath: filepath.Join(dir, "state", "logs", "run.log"), UID: 501, Env: map[string]string{"TGBUP_CONFIG": "/c.yaml"},
		launchctl: f.run,
	}, f
}

func TestLaunchdPlist(t *testing.T) {
	j, _ := testSchedule(t)
	got := string(j.plist())
	for _, want := range []string{
		"<string>com.example.tgbup</string>", "<string>/Users/me/.local/bin/tgbup</string>", "<string>run</string>",
		"<key>Hour</key><integer>0</integer>", "<key>Hour</key><integer>6</integer>", "<key>Hour</key><integer>12</integer>",
		"<key>Hour</key><integer>18</integer>", "<key>Minute</key><integer>30</integer>",
		"<key>TGBUP_CONFIG</key><string>/c.yaml</string>", "run.log",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist lacks %s:\n%s", want, got)
		}
	}
}

func TestScheduleInstallAndUninstall(t *testing.T) {
	j, f := testSchedule(t)
	if err := j.install(); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(j.AgentsDir, "com.example.tgbup.plist")
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(j.LogPath)); err != nil {
		t.Errorf("log folder not created: %v", err)
	}
	want := []string{"bootout gui/501/com.example.tgbup", "bootstrap gui/501 " + plist}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("launchctl calls = %q", f.calls)
	}
	f.calls = nil
	if err := j.uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("plist left behind")
	}
	if len(f.calls) != 1 || f.calls[0] != "bootout gui/501/com.example.tgbup" {
		t.Fatalf("launchctl calls = %q", f.calls)
	}
}

func TestScheduleInstallRetriesBusyBootstrap(t *testing.T) {
	j, f := testSchedule(t)
	j.retryDelay = 0
	f.busy = 2
	if err := j.install(); err != nil {
		t.Fatalf("install gave up while launchd was busy: %v", err)
	}
	f.busy = 10
	if err := j.install(); err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduleKeepsPathOverrides(t *testing.T) {
	env := map[string]string{"XDG_CONFIG_HOME": "/x/config", "XDG_STATE_HOME": "/x/state", "TGBUP_SECRETS": "/s.env", "OTHER": "no"}
	got := jobEnv(func(k string) string { return env[k] })
	if len(got) != 3 || got["XDG_CONFIG_HOME"] != "/x/config" || got["XDG_STATE_HOME"] != "/x/state" || got["TGBUP_SECRETS"] != "/s.env" {
		t.Fatalf("job env = %v", got)
	}
}
