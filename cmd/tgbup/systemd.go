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

// unitQuote makes s one word of a unit file line: % (specifiers) and $
// (variables) doubled always, quoted when it has spaces or quotes.
func unitQuote(s string) string {
	s = strings.NewReplacer("%", "%%", "$", "$$").Replace(s)
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// errNoBus: systemd runs here, but this session cannot reach the person's
// systemd: su, sudo -u, a tmux or screen left from an earlier login.
func (j schedJob) errNoBus() error {
	return fmt.Errorf("this session cannot reach your systemd (su, sudo -u or a terminal left from an earlier login?): "+
		"log in to this account directly, or run with XDG_RUNTIME_DIR=/run/user/%d", j.UID)
}

// serviceUnit runs tgbup run once, its output appended to run.log.
func (j schedJob) serviceUnit() []byte {
	var b bytes.Buffer
	b.WriteString("[Unit]\nDescription=tgbup run: archive Telegram and back it up\n\n[Service]\nType=oneshot\n")
	fmt.Fprintf(&b, "ExecStart=%s run\n", unitQuote(j.Exe))
	for _, k := range slices.Sorted(maps.Keys(j.Env)) {
		if strings.ContainsAny(j.Env[k], "\n\r") {
			continue // no path has one; it would start another line of the unit
		}
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
	if j.noBus {
		return j.errNoBus()
	}
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
	if j.noBus {
		return j.errNoBus()
	}
	_, _ = j.systemctl("disable", "--now", j.Label+".timer")
	// The stamp of the last start: left, a timer installed again later would
	// start at once to make up for the time it was off.
	for _, p := range []string{j.timerPath(), j.servicePath(), j.StampPath} {
		if p == "" {
			continue
		}
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
	if j.noBus {
		// Not asked: the timer installed earlier from a login is taken to be on.
		_, err := os.Stat(j.timerPath())
		return jobState{Loaded: err == nil}
	}
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

// taskCommand is the command that runs tgbup the way the schedule would, for
// a NAS's task scheduler, which takes the times in its own form.
func (j schedJob) taskCommand() string {
	env := ""
	for _, k := range slices.Sorted(maps.Keys(j.Env)) {
		env += k + "=" + render.ShellQuote(j.Env[k]) + " "
	}
	return fmt.Sprintf("%s%s run >> %s 2>&1", env, render.ShellQuote(j.Exe), render.ShellQuote(j.LogPath))
}

// cronLine is the crontab line that starts tgbup run at the times, one per
// minute of the hour: cron takes one minute and a list of hours. cron reads %
// in a command as a new line, so it is escaped.
func (j schedJob) cronLine() string {
	byMinute := map[int][]string{}
	for _, t := range j.Times {
		byMinute[t.Minute] = append(byMinute[t.Minute], fmt.Sprint(t.Hour))
	}
	cmd := strings.ReplaceAll(j.taskCommand(), "%", `\%`)
	var lines []string
	for _, m := range slices.Sorted(maps.Keys(byMinute)) {
		lines = append(lines, fmt.Sprintf("%d %s * * * %s", m, strings.Join(byMinute[m], ","), cmd))
	}
	return strings.Join(lines, "\n")
}

// cronInstalled: a line of the person's crontab, not commented out, runs
// this tgbup (written as cronLine does, or by hand).
func (j schedJob) cronInstalled() bool {
	if j.crontab == nil {
		return false
	}
	out, err := j.crontab("-l")
	if err != nil {
		return false
	}
	escaped := strings.ReplaceAll(j.Exe, "%", `\%`)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, " run") {
			continue
		}
		if strings.Contains(line, j.Exe) || strings.Contains(line, escaped) {
			return true
		}
	}
	return false
}
