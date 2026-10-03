package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

type setup struct {
	paths   config.Paths
	cfg     config.Config
	secrets config.Secrets
	testDC  bool
}

func loadSetup(env Env, requireConfig bool) (setup, error) {
	st, err := loadLocalSetup(env, requireConfig)
	if err != nil {
		return setup{}, err
	}
	return st, st.secrets.RequireTelegram()
}

// loadLocalSetup is loadSetup for commands that work on the archive alone
// (backup, deps) and need no Telegram credentials.
func loadLocalSetup(env Env, requireConfig bool) (setup, error) {
	if err := checkTestIsolation(env); err != nil {
		return setup{}, err
	}
	return resolveSetup(env, requireConfig)
}

// checkTestIsolation: only the session file differs on the test servers, so
// with the default config, secrets or state, test chats would go into the
// real archive and backups, and a test password would replace the real one.
func checkTestIsolation(env Env) error {
	if env.Getenv("TGBUP_TEST_DC") != "1" {
		return nil
	}
	for _, k := range []string{"TGBUP_CONFIG", "TGBUP_SECRETS", "TGBUP_STATE_DIR"} {
		if env.Getenv(k) == "" {
			return fmt.Errorf("TGBUP_TEST_DC=1 needs its own TGBUP_CONFIG, TGBUP_SECRETS and TGBUP_STATE_DIR (%s is not set): the defaults are the real archive, password and state", k)
		}
	}
	return nil
}

// resolveSetup loads paths, config and secrets.
func resolveSetup(env Env, requireConfig bool) (setup, error) {
	p, err := config.ResolvePaths(env.Getenv)
	if err != nil {
		return setup{}, err
	}
	load := config.Load
	if !requireConfig {
		load = config.LoadWithoutTargets
	}
	cfg, err := load(p.Config, p.Home)
	switch {
	case errors.Is(err, fs.ErrNotExist) && requireConfig:
		return setup{}, fmt.Errorf("no config at %s yet: run tgbup init", p.Config)
	case errors.Is(err, fs.ErrNotExist):
		cfg = config.Default()
		cfg.ArchiveDir = expandTilde(cfg.ArchiveDir, p.Home)
		if env.Getenv("TGBUP_TEST_DC") == "1" {
			// Never the real archive. The config is written now: a later write of
			// any other setting would otherwise create it from the template, whose
			// archive_dir is the real ~/Tgbup/archive.
			cfg.ArchiveDir = filepath.Join(filepath.Dir(p.Config), "archive")
			if err := config.SetValue(p.Config, []string{"archive_dir"}, cfg.ArchiveDir); err != nil {
				return setup{}, err
			}
		}
	case err != nil:
		return setup{}, err
	}
	sec, err := config.LoadSecrets(p.Secrets, env.Getenv)
	if err != nil {
		return setup{}, err
	}
	return setup{paths: p, cfg: cfg, secrets: sec, testDC: env.Getenv("TGBUP_TEST_DC") == "1"}, nil
}

func (s setup) clientOptions() tgclient.Options {
	return tgclient.Options{
		APIID:        s.secrets.APIID,
		APIHash:      s.secrets.APIHash,
		SessionPath:  s.paths.Session(s.testDC),
		Version:      version,
		MaxFloodWait: s.cfg.Sync.MaxFloodWait.D(),
		TestDC:       s.testDC,
	}
}
