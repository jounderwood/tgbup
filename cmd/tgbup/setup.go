package main

import (
	"errors"
	"io/fs"

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
	p, err := config.ResolvePaths(env.Getenv)
	if err != nil {
		return setup{}, err
	}
	load := config.Load
	if !requireConfig {
		load = config.LoadWithoutTargets
	}
	cfg, err := load(p.Config, p.Home)
	if err != nil {
		if requireConfig || !errors.Is(err, fs.ErrNotExist) {
			return setup{}, err
		}
		cfg = config.Default()
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
