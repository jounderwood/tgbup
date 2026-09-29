package config

import (
	"errors"
	"path/filepath"
)

type Paths struct {
	Home     string
	Config   string
	Secrets  string
	StateDir string
}

func ResolvePaths(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	if home == "" {
		return Paths{}, errors.New("HOME is not set")
	}
	cfgDir := getenv("XDG_CONFIG_HOME")
	if cfgDir == "" {
		cfgDir = filepath.Join(home, ".config")
	}
	stateDir := getenv("XDG_STATE_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".local", "state")
	}
	p := Paths{
		Home:     home,
		Config:   filepath.Join(cfgDir, "tgbup", "config.yaml"),
		Secrets:  filepath.Join(cfgDir, "tgbup", "secrets.env"),
		StateDir: filepath.Join(stateDir, "tgbup"),
	}
	if v := getenv("TGBUP_CONFIG"); v != "" {
		p.Config = v
	}
	if v := getenv("TGBUP_SECRETS"); v != "" {
		p.Secrets = v
	}
	if v := getenv("TGBUP_STATE_DIR"); v != "" {
		p.StateDir = v
	}
	return p, nil
}

func (p Paths) Session() string { return filepath.Join(p.StateDir, "session") }
func (p Paths) Lock() string    { return filepath.Join(p.StateDir, "run.lock") }
