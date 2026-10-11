package main

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/jounderwood/tgbup/internal/config"
)

// launchdLabel names the job in launchctl, Login Items and LaunchAgents.
const launchdLabel = "tgbup"

// legacyLabel is the name earlier versions gave the job (after the project's
// first address); installing or removing the job removes that one too, so
// the two never both run.
const legacyLabel = "com.gitlab.joeunderwood.tgbup"

// The kinds of schedulers a job can be set up with.
const (
	kindLaunchd = "launchd" // macOS: a launchd agent
	kindSystemd = "systemd" // Linux with systemd: a user timer
	kindCron    = "cron"    // anything else: a crontab line the person adds
)

// schedJob is the job that starts tgbup run at schedule.times: a launchd
// agent on macOS, a systemd user timer on Linux, or, without either, a line
// for cron. A start missed while the computer slept or was off runs once
// when it is back (launchd, systemd; not cron).
type schedJob struct {
	Kind      string // "" is launchd
	Label     string
	Exe       string
	AgentsDir string // launchd
	UnitDir   string // systemd: ~/.config/systemd/user
	LogPath   string
	UID       int
	User      string // systemd: whose lingering keeps the timer running logged out
	Env       map[string]string
	Times     []config.Clock
	launchctl func(args ...string) ([]byte, error)
	systemctl func(args ...string) ([]byte, error) // run with --user
	loginctl  func(args ...string) ([]byte, error)
	// retryDelay is the pause between bootstrap attempts while launchd is still
	// tearing down the job that was just booted out.
	retryDelay time.Duration
}

// manual: nothing can be installed here; the person adds cronLine.
func (j schedJob) manual() bool { return j.Kind == kindCron }

func (j schedJob) path() string {
	switch j.Kind {
	case kindSystemd:
		return j.timerPath()
	case kindCron:
		return ""
	}
	return filepath.Join(j.AgentsDir, j.Label+".plist")
}
func (j schedJob) target() string { return fmt.Sprintf("gui/%d/%s", j.UID, j.Label) }

func (j schedJob) plist() []byte {
	var b bytes.Buffer
	esc := html.EscapeString
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	fmt.Fprintf(&b, "<key>Label</key><string>%s</string>\n", esc(j.Label))
	fmt.Fprintf(&b, "<key>ProgramArguments</key><array><string>%s</string><string>run</string></array>\n", esc(j.Exe))
	b.WriteString("<key>StartCalendarInterval</key><array>\n")
	for _, t := range j.Times {
		fmt.Fprintf(&b, "<dict><key>Hour</key><integer>%d</integer><key>Minute</key><integer>%d</integer></dict>\n", t.Hour, t.Minute)
	}
	b.WriteString("</array>\n")
	if len(j.Env) > 0 {
		b.WriteString("<key>EnvironmentVariables</key><dict>\n")
		for _, k := range slices.Sorted(maps.Keys(j.Env)) {
			fmt.Fprintf(&b, "<key>%s</key><string>%s</string>\n", esc(k), esc(j.Env[k]))
		}
		b.WriteString("</dict>\n")
	}
	fmt.Fprintf(&b, "<key>StandardOutPath</key><string>%s</string>\n", esc(j.LogPath))
	fmt.Fprintf(&b, "<key>StandardErrorPath</key><string>%s</string>\n", esc(j.LogPath))
	b.WriteString("<key>ProcessType</key><string>Background</string>\n</dict>\n</plist>\n")
	return b.Bytes()
}

func (j schedJob) install() error {
	if strings.Contains(j.Exe, string(filepath.Separator)+"go-build") {
		return errors.New("run an installed tgbup (make install), not go run")
	}
	switch j.Kind {
	case kindSystemd:
		return j.systemdInstall()
	case kindCron:
		return errors.New("no launchd or systemd here: add this line with crontab -e: " + j.cronLine())
	}
	if err := os.MkdirAll(filepath.Dir(j.LogPath), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(j.AgentsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(j.path(), j.plist(), 0o644); err != nil {
		return err
	}
	if err := j.removeLegacy(); err != nil {
		return err
	}
	// Replace a job installed earlier (another tgbup path or schedule); none is fine.
	_, _ = j.launchctl("bootout", j.target())
	var err error
	for range 5 {
		var out []byte
		if out, err = j.launchctl("bootstrap", fmt.Sprintf("gui/%d", j.UID), j.path()); err == nil {
			return nil
		}
		err = fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
		time.Sleep(j.retryDelay)
	}
	return err
}

func (j schedJob) uninstall() error {
	switch j.Kind {
	case kindSystemd:
		return j.systemdUninstall()
	case kindCron:
		return errors.New("remove the tgbup line with crontab -e")
	}
	_, _ = j.launchctl("bootout", j.target())
	if err := os.Remove(j.path()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return j.removeLegacy()
}

// legacy is the job as earlier versions named it.
func (j schedJob) legacy() schedJob {
	j.Label = legacyLabel
	return j
}

// legacyInstalled: a job under the old name is still there.
func (j schedJob) legacyInstalled() bool {
	if j.Kind != "" && j.Kind != kindLaunchd {
		return false
	}
	_, err := os.Stat(j.legacy().path())
	return err == nil
}

func (j schedJob) removeLegacy() error {
	old := j.legacy()
	if !j.legacyInstalled() {
		return nil
	}
	_, _ = j.launchctl("bootout", old.target())
	if err := os.Remove(old.path()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (j schedJob) status() string {
	switch j.Kind {
	case kindSystemd:
		return j.systemdStatus()
	case kindCron:
		return "no launchd or systemd here: add this line with crontab -e:\n" + j.cronLine()
	}
	if _, err := os.Stat(j.path()); err != nil {
		return "not installed"
	}
	s := fmt.Sprintf("installed: %s runs at %s; log %s", j.Exe, clockList(j.Times), j.LogPath)
	out, err := j.launchctl("print", j.target())
	if err != nil {
		return s + "\nlaunchd: not loaded (run tgbup schedule install again)"
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state =") || strings.HasPrefix(line, "last exit code =") {
			s += "\n" + line
		}
	}
	return s
}

// jobEnv carries the variables that move tgbup's files, so the scheduled run
// finds the same config, secrets and state as the shell that installed it.
func jobEnv(getenv func(string) string) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"TGBUP_CONFIG", "TGBUP_SECRETS", "TGBUP_STATE_DIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		if v := getenv(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// newSchedJob describes the job for this tgbup binary.
// clockList is "00:30, 06:30".
func clockList(times []config.Clock) string {
	out := make([]string, len(times))
	for i, t := range times {
		out[i] = t.String()
	}
	return strings.Join(out, ", ")
}

func newSchedJob(st setup, env Env) (schedJob, error) {
	p := st.paths
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return schedJob{}, err
	}
	j := schedJob{
		Kind: kindLaunchd, Label: launchdLabel, Exe: exe, AgentsDir: filepath.Join(p.Home, "Library", "LaunchAgents"),
		LogPath: p.RunLog(), UID: os.Getuid(), Env: jobEnv(env.Getenv), Times: st.cfg.Schedule.Clocks(),
		retryDelay: time.Second,
		launchctl:  func(args ...string) ([]byte, error) { return exec.Command("launchctl", args...).CombinedOutput() },
		systemctl: func(args ...string) ([]byte, error) {
			return exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
		},
		loginctl: func(args ...string) ([]byte, error) { return exec.Command("loginctl", args...).CombinedOutput() },
	}
	if runtime.GOOS != "darwin" {
		j.Kind, j.UnitDir, j.User = kindCron, systemdUnitDir(p.Home, env.Getenv), env.Getenv("USER")
		// A user manager answers when systemd runs this login (most Linux, NAS
		// systems with systemd); without it the person adds a cron line.
		if _, err := j.systemctl("show-environment"); err == nil {
			j.Kind = kindSystemd
		}
	}
	return j, nil
}

// jobState is what launchd reports about the job.
type jobState struct {
	Loaded  bool
	Running bool
	// LastExit is "" before the first run ends.
	LastExit string
}

func (j schedJob) state() jobState {
	switch j.Kind {
	case kindSystemd:
		return j.systemdState()
	case kindCron:
		return jobState{}
	}
	out, err := j.launchctl("print", j.target())
	if err != nil {
		return jobState{}
	}
	s := jobState{Loaded: true}
	seenState := false
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
		switch {
		case !ok:
		case k == "state" && !seenState:
			// Later "state = active" lines belong to nested sections (sockets, endpoints).
			s.Running, seenState = v == "running", true
		case k == "last exit code" && !strings.HasPrefix(v, "("):
			s.LastExit = v
		}
	}
	return s
}

// installedHere reports whether the job file starts this same binary.
func (j schedJob) installedHere() bool {
	switch j.Kind {
	case kindSystemd:
		b, err := os.ReadFile(j.servicePath())
		return err == nil && bytes.Contains(b, []byte("ExecStart="+unitQuote(j.Exe)+" run\n"))
	case kindCron:
		return false
	}
	b, err := os.ReadFile(j.path())
	return err == nil && bytes.Contains(b, []byte("<string>"+html.EscapeString(j.Exe)+"</string>"))
}

func (j schedJob) kickstart() error {
	switch j.Kind {
	case kindSystemd:
		// --no-block: the service is oneshot, and the page waits for nobody.
		if out, err := j.systemctl("start", "--no-block", j.Label+".service"); err != nil {
			return fmt.Errorf("systemctl --user start: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	case kindCron:
		return errors.New("cron starts tgbup run by itself; run it now with: tgbup run")
	}
	if out, err := j.launchctl("kickstart", j.target()); err != nil {
		return fmt.Errorf("launchctl kickstart: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func cmdSchedule(args []string, env Env) int {
	if len(args) != 1 || !slices.Contains([]string{"install", "uninstall", "status"}, args[0]) {
		fmt.Fprintln(env.Stderr, "usage: tgbup schedule install|uninstall|status")
		return 2
	}
	p, err := loadLocalSetup(env, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup schedule:", err)
		return 1
	}
	if p.testDC && args[0] == "install" {
		fmt.Fprintln(env.Stderr, "tgbup schedule: not with TGBUP_TEST_DC=1: the scheduled job runs the real archive")
		return 1
	}
	j, err := newSchedJob(p, env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup schedule:", err)
		return 1
	}
	if j.manual() {
		fmt.Fprintln(env.Stdout, j.status())
		if args[0] == "status" {
			return 0
		}
		return 1
	}
	switch args[0] {
	case "install":
		err = j.install()
	case "uninstall":
		err = j.uninstall()
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup schedule:", err)
		return 1
	}
	fmt.Fprintln(env.Stdout, j.status())
	return 0
}
