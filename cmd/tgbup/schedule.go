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
)

const launchdLabel = "com.gitlab.joeunderwood.tgbup"

// runHours are when the schedule starts tgbup run; launchd runs a missed
// start once after the Mac wakes up.
var runHours = []int{0, 6, 12, 18}

type launchdJob struct {
	Label     string
	Exe       string
	AgentsDir string
	LogPath   string
	UID       int
	Env       map[string]string
	launchctl func(args ...string) ([]byte, error)
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
	for _, h := range runHours {
		fmt.Fprintf(&b, "<dict><key>Hour</key><integer>%d</integer><key>Minute</key><integer>30</integer></dict>\n", h)
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
	if out, err := j.launchctl("bootstrap", fmt.Sprintf("gui/%d", j.UID), j.path()); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
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
	hours := make([]string, len(runHours))
	for i, h := range runHours {
		hours[i] = fmt.Sprintf("%02d:30", h)
	}
	s := fmt.Sprintf("installed: %s runs at %s; log %s", j.Exe, strings.Join(hours, ", "), j.LogPath)
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
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup schedule:", err)
		return 1
	}
	jobEnv := map[string]string{}
	for _, k := range []string{"TGBUP_CONFIG", "TGBUP_SECRETS", "TGBUP_STATE_DIR"} {
		if v := env.Getenv(k); v != "" {
			jobEnv[k] = v
		}
	}
	j := launchdJob{
		Label: launchdLabel, Exe: exe, AgentsDir: filepath.Join(p.paths.Home, "Library", "LaunchAgents"),
		LogPath: filepath.Join(p.paths.StateDir, "logs", "run.log"), UID: os.Getuid(), Env: jobEnv,
		launchctl: func(args ...string) ([]byte, error) { return exec.Command("launchctl", args...).CombinedOutput() },
	}
	switch args[0] {
	case "install":
		if strings.Contains(exe, string(filepath.Separator)+"go-build") {
			fmt.Fprintln(env.Stderr, "tgbup schedule: run an installed tgbup (make install), not go run")
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
