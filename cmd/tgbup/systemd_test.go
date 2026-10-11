package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jounderwood/tgbup/internal/config"
)

type fakeSystemctl struct {
	calls []string
	show  string // what systemctl show prints
	fail  string // the command that fails
}

func (f *fakeSystemctl) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == f.fail {
		return []byte("Failed to connect to bus"), errors.New("exit status 1")
	}
	if args[0] == "show" {
		return []byte(f.show), nil
	}
	return nil, nil
}

func testSystemd(t *testing.T) (schedJob, *fakeSystemctl) {
	t.Helper()
	f := &fakeSystemctl{}
	dir := t.TempDir()
	times, _ := config.ParseTimes([]string{"0:30", "6:30", "18:45"})
	return schedJob{
		Kind: kindSystemd, Label: "tgbup", Exe: "/home/me/My Apps/tgbup", UnitDir: filepath.Join(dir, "systemd", "user"),
		LogPath: filepath.Join(dir, "logs", "run.log"), User: "me", Env: map[string]string{"TGBUP_CONFIG": "/c 1.yaml"},
		Times: times, systemctl: f.run,
		loginctl: func(...string) ([]byte, error) { return []byte("no\n"), nil },
	}, f
}

func TestSystemdUnits(t *testing.T) {
	j, _ := testSystemd(t)
	svc, timer := string(j.serviceUnit()), string(j.timerUnit())
	for _, want := range []string{"Type=oneshot", `ExecStart="/home/me/My Apps/tgbup" run`, `Environment="TGBUP_CONFIG=/c 1.yaml"`,
		"StandardOutput=append:" + j.LogPath} {
		if !strings.Contains(svc, want) {
			t.Errorf("service lacks %q:\n%s", want, svc)
		}
	}
	for _, want := range []string{"OnCalendar=*-*-* 00:30:00", "OnCalendar=*-*-* 06:30:00", "OnCalendar=*-*-* 18:45:00",
		"Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(timer, want) {
			t.Errorf("timer lacks %q:\n%s", want, timer)
		}
	}
	if got := unitQuote("50%"); got != "50%%" {
		t.Errorf("unitQuote(50%%) = %q", got)
	}
}

func TestSystemdInstallAndUninstall(t *testing.T) {
	j, f := testSystemd(t)
	if j.installedHere() {
		t.Fatal("installed before installing")
	}
	if err := j.install(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, "|"); got != "daemon-reload|enable tgbup.timer|restart tgbup.timer" {
		t.Errorf("systemctl calls = %q", got)
	}
	if !j.installedHere() {
		t.Error("not installed here after installing")
	}
	f.calls = nil
	if err := j.uninstall(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{j.timerPath(), j.servicePath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind", p)
		}
	}
	if !strings.HasPrefix(strings.Join(f.calls, "|"), "disable --now tgbup.timer") {
		t.Errorf("not stopped: %q", f.calls)
	}
	f.fail = "enable"
	if err := j.install(); err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Errorf("a failing systemctl: %v", err)
	}
}

func TestSystemdState(t *testing.T) {
	j, f := testSystemd(t)
	f.show = "ActiveState=active\n\nActiveState=inactive\nExecMainStatus=0\nExecMainExitTimestampMonotonic=123\n"
	if s := j.state(); !s.Loaded || s.Running || s.LastExit != "0" {
		t.Errorf("after a run: %+v", s)
	}
	f.show = "ActiveState=active\n\nActiveState=activating\nExecMainStatus=0\nExecMainExitTimestampMonotonic=0\n"
	if s := j.state(); !s.Loaded || !s.Running || s.LastExit != "" {
		t.Errorf("running, never ended: %+v", s)
	}
	f.show = "ActiveState=inactive\n\nActiveState=inactive\n"
	if s := j.state(); s.Loaded {
		t.Errorf("a stopped timer is loaded: %+v", s)
	}
	if err := j.kickstart(); err != nil || f.calls[len(f.calls)-1] != "start --no-block tgbup.service" {
		t.Errorf("kickstart: %v %q", err, f.calls)
	}
	if !strings.Contains(j.systemdStatus(), "not installed") {
		t.Error("status of a job never installed")
	}
	_ = j.install()
	if !strings.Contains(j.systemdStatus(), "loginctl enable-linger me") {
		t.Errorf("status does not say how to run it logged out:\n%s", j.systemdStatus())
	}
}

func TestCronLine(t *testing.T) {
	j, _ := testSystemd(t)
	j.Kind = kindCron
	got := j.cronLine()
	want := "30 0,6 * * * TGBUP_CONFIG='/c 1.yaml' '/home/me/My Apps/tgbup' run >> " + j.LogPath + " 2>&1\n" +
		"45 18 * * * TGBUP_CONFIG='/c 1.yaml' '/home/me/My Apps/tgbup' run >> " + j.LogPath + " 2>&1"
	if got != want {
		t.Errorf("cron line:\n%s\nwant:\n%s", got, want)
	}
	if !j.manual() || j.install() == nil || j.installedHere() {
		t.Error("a cron job installs itself")
	}
}
