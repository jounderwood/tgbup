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

	"gitlab.com/joeunderwood/tgbup/internal/config"
)

const launchdLabel = "com.gitlab.joeunderwood.tgbup"

type launchdJob struct {
	Label     string
	Exe       string
	AgentsDir string
	LogPath   string
	UID       int
	Env       map[string]string
	// Times are when launchd starts tgbup run (schedule.times); a start
	// missed while the Mac sleeps runs once on wake-up.
	Times     []config.Clock
	launchctl func(args ...string) ([]byte, error)
	// retryDelay is the pause between bootstrap attempts while launchd is still
	// tearing down the job that was just booted out.
	retryDelay time.Duration
}

func (j launchdJob) path() string   { return filepath.Join(j.AgentsDir, j.Label+".plist") }
func (j launchdJob) target() string { return fmt.Sprintf("gui/%d/%s", j.UID, j.Label) }

func (j launchdJob) plist() []byte {
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

func (j launchdJob) install() error {
	if strings.Contains(j.Exe, string(filepath.Separator)+"go-build") {
		return errors.New("run an installed tgbup (make install), not go run")
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

func (j launchdJob) uninstall() error {
	_, _ = j.launchctl("bootout", j.target())
	if err := os.Remove(j.path()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (j launchdJob) status() string {
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

// newLaunchdJob describes the job for this tgbup binary.
// clockList is "00:30, 06:30".
func clockList(times []config.Clock) string {
	out := make([]string, len(times))
	for i, t := range times {
		out[i] = t.String()
	}
	return strings.Join(out, ", ")
}

func newLaunchdJob(st setup, env Env) (launchdJob, error) {
	p := st.paths
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return launchdJob{}, err
	}
	return launchdJob{
		Label: launchdLabel, Exe: exe, AgentsDir: filepath.Join(p.Home, "Library", "LaunchAgents"),
		LogPath: p.RunLog(), UID: os.Getuid(), Env: jobEnv(env.Getenv), Times: st.cfg.Schedule.Clocks(),
		retryDelay: time.Second,
		launchctl:  func(args ...string) ([]byte, error) { return exec.Command("launchctl", args...).CombinedOutput() },
	}, nil
}

// jobState is what launchd reports about the job.
type jobState struct {
	Loaded  bool
	Running bool
	// LastExit is "" before the first run ends.
	LastExit string
}

func (j launchdJob) state() jobState {
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
func (j launchdJob) installedHere() bool {
	b, err := os.ReadFile(j.path())
	return err == nil && bytes.Contains(b, []byte("<string>"+html.EscapeString(j.Exe)+"</string>"))
}

func (j launchdJob) kickstart() error {
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
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(env.Stderr, "tgbup schedule: only macOS (launchd) for now; use cron or systemd to start `tgbup run`")
		return 1
	}
	p, err := loadLocalSetup(env, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup schedule:", err)
		return 1
	}
	j, err := newLaunchdJob(p, env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup schedule:", err)
		return 1
	}
	switch args[0] {
	case "install":
		if p.testDC {
			fmt.Fprintln(env.Stderr, "tgbup schedule: not with TGBUP_TEST_DC=1: the scheduled job runs the real archive")
			return 1
		}
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
