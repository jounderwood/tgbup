package config

import (
	"errors"
	"os"
	"path/filepath"
)

// Folder is where tgbup keeps what a person looks at: the config, the logs,
// and by default the archive and the backup on this Mac.
const Folder = "Tgbup"

type Paths struct {
	Home   string
	Config string
	// Secrets and StateDir are hidden: keys, passwords and the Telegram
	// session must not travel with a folder that gets copied or synced.
	Secrets  string
	StateDir string
	Logs     string
}

// ResolvePaths finds tgbup's files. A setup from before ~/Tgbup (config and
// secrets in ~/.config/tgbup) keeps using them.
func ResolvePaths(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	if home == "" {
		return Paths{}, errors.New("HOME is not set")
	}
	stateDir := getenv("XDG_STATE_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".local", "state")
	}
	stateDir = filepath.Join(stateDir, "tgbup")
	if v := getenv("TGBUP_STATE_DIR"); v != "" {
		stateDir = v
	}
	root := filepath.Join(home, Folder)
	p := Paths{
		Home:     home,
		Config:   filepath.Join(root, "config.yaml"),
		Secrets:  filepath.Join(stateDir, "secrets.env"),
		StateDir: stateDir,
		Logs:     filepath.Join(root, "logs"),
	}
	legacy := getenv("XDG_CONFIG_HOME")
	if legacy == "" {
		legacy = filepath.Join(home, ".config")
	}
	legacy = filepath.Join(legacy, "tgbup")
	if !exists(p.Config) && exists(filepath.Join(legacy, "config.yaml")) {
		p.Config, p.Logs = filepath.Join(legacy, "config.yaml"), filepath.Join(stateDir, "logs")
	}
	if !exists(p.Secrets) && exists(filepath.Join(legacy, "secrets.env")) {
		p.Secrets = filepath.Join(legacy, "secrets.env")
	}
	if v := getenv("TGBUP_CONFIG"); v != "" {
		p.Config, p.Logs = v, filepath.Join(filepath.Dir(v), "logs")
	}
	if v := getenv("TGBUP_SECRETS"); v != "" {
		p.Secrets = v
	}
	return p, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Session is the MTProto session file; the test-server one is separate so a
// test login never replaces the real session.
func (p Paths) Session(testDC bool) string {
	if testDC {
		return filepath.Join(p.StateDir, "session-test")
	}
	return filepath.Join(p.StateDir, "session")
}

func (p Paths) Lock() string { return filepath.Join(p.StateDir, "run.lock") }

// RunLog is the output of runs from the schedule and the web page.
func (p Paths) RunLog() string { return filepath.Join(p.Logs, "run.log") }
