package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jounderwood/tgbup/internal/render"
)

// systemdUnitDir is where systemd looks for the person's own units.
func systemdUnitDir(home string, getenv func(string) string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user")
	}
	return filepath.Join(home, ".config", "systemd", "user")
}

func (j schedJob) servicePath() string { return filepath.Join(j.UnitDir, j.Label+".service") }
func (j schedJob) timerPath() string   { return filepath.Join(j.UnitDir, j.Label+".timer") }

// unitQuote makes s one word of a unit file line: quoted when it has spaces
// or quotes, and % (systemd's specifiers) doubled always.
func unitQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// serviceUnit runs tgbup run once, its output appended to run.log.
func (j schedJob) serviceUnit() []byte {
	var b bytes.Buffer
	b.WriteString("[Unit]\nDescription=tgbup run: archive Telegram and back it up\n\n[Service]\nType=oneshot\n")
	fmt.Fprintf(&b, "ExecStart=%s run\n", unitQuote(j.Exe))
	for _, k := range slices.Sorted(maps.Keys(j.Env)) {
		fmt.Fprintf(&b, "Environment=%s\n", unitQuote(k+"="+j.Env[k]))
	}
	log := strings.ReplaceAll(j.LogPath, "%", "%%")
	fmt.Fprintf(&b, "StandardOutput=append:%s\nStandardError=append:%s\n", log, log)
	return b.Bytes()
}

// timerUnit starts the service at the times, local time; Persistent runs
// once a start missed while the computer was off or asleep.
func (j schedJob) timerUnit() []byte {
	var b bytes.Buffer
	b.WriteString("[Unit]\nDescription=tgbup: start tgbup run at schedule.times\n\n[Timer]\n")
	for _, t := range j.Times {
		fmt.Fprintf(&b, "OnCalendar=*-*-* %02d:%02d:00\n", t.Hour, t.Minute)
	}
	b.WriteString("Persistent=true\n\n[Install]\nWantedBy=timers.target\n")
	return b.Bytes()
}

func (j schedJob) systemdInstall() error {
	if err := os.MkdirAll(filepath.Dir(j.LogPath), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(j.UnitDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(j.servicePath(), j.serviceUnit(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(j.timerPath(), j.timerUnit(), 0o644); err != nil {
		return err
	}
	// restart, not start: an installed timer takes the new times.
	for _, args := range [][]string{{"daemon-reload"}, {"enable", j.Label + ".timer"}, {"restart", j.Label + ".timer"}} {
		if out, err := j.systemctl(args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func (j schedJob) systemdUninstall() error {
	_, _ = j.systemctl("disable", "--now", j.Label+".timer")
	for _, p := range []string{j.timerPath(), j.servicePath()} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	_, _ = j.systemctl("daemon-reload")
	return nil
}

// systemdState reads the timer (enabled and waiting: Loaded) and the
// service (Running, the last run's exit status).
func (j schedJob) systemdState() jobState {
	out, err := j.systemctl("show", j.Label+".timer", j.Label+".service",
		"--property=ActiveState,ExecMainStatus,ExecMainExitTimestampMonotonic")
	if err != nil {
		return jobState{}
	}
	blocks := strings.Split(strings.TrimSpace(string(out)), "\n\n")
	prop := func(block int, key string) string {
		if block >= len(blocks) {
			return ""
		}
		for _, l := range strings.Split(blocks[block], "\n") {
			if k, v, ok := strings.Cut(l, "="); ok && k == key {
				return v
			}
		}
		return ""
	}
	s := jobState{Loaded: prop(0, "ActiveState") == "active"}
	switch prop(1, "ActiveState") {
	case "active", "activating":
		s.Running = true
	}
	if t := prop(1, "ExecMainExitTimestampMonotonic"); t != "" && t != "0" {
		s.LastExit = prop(1, "ExecMainStatus")
	}
	return s
}

// lingering: systemd keeps the person's timers running while they are not
// logged in; without it a server nobody logs in to never runs tgbup.
func (j schedJob) lingering() bool {
	if j.loginctl == nil || j.User == "" {
		return false
	}
	out, err := j.loginctl("show-user", j.User, "--property=Linger", "--value")
	return err == nil && strings.TrimSpace(string(out)) == "yes"
}

func (j schedJob) systemdStatus() string {
	if _, err := os.Stat(j.timerPath()); err != nil {
		return "not installed"
	}
	s := fmt.Sprintf("installed: the systemd timer %s.timer starts %s run at %s; log %s", j.Label, j.Exe, clockList(j.Times), j.LogPath)
	st := j.state()
	switch {
	case !st.Loaded:
		s += "\nsystemd: the timer is not active (run tgbup schedule install again)"
	case st.Running:
		s += "\nrunning now"
	case st.LastExit != "":
		s += "\nlast exit status = " + st.LastExit
	}
	if !j.lingering() {
		s += "\nIt runs only while you are logged in; to run it always: loginctl enable-linger " + j.User
	}
	return s
}

// cronLine is the crontab line that starts tgbup run at the times, one per
// minute of the hour: cron takes one minute and a list of hours.
func (j schedJob) cronLine() string {
	byMinute := map[int][]string{}
	for _, t := range j.Times {
		byMinute[t.Minute] = append(byMinute[t.Minute], fmt.Sprint(t.Hour))
	}
	env := ""
	for _, k := range slices.Sorted(maps.Keys(j.Env)) {
		env += k + "=" + render.ShellQuote(j.Env[k]) + " "
	}
	var lines []string
	for _, m := range slices.Sorted(maps.Keys(byMinute)) {
		lines = append(lines, fmt.Sprintf("%d %s * * * %s%s run >> %s 2>&1", m, strings.Join(byMinute[m], ","), env,
			render.ShellQuote(j.Exe), render.ShellQuote(j.LogPath)))
	}
	return strings.Join(lines, "\n")
}
