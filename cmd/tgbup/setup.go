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
	p, err := config.ResolvePaths(env.Getenv)
	if err != nil {
		return setup{}, err
	}
	cfg, err := config.Load(p.Config, p.Home)
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
	if err := sec.RequireTelegram(); err != nil {
		return setup{}, err
	}
	return setup{paths: p, cfg: cfg, secrets: sec, testDC: env.Getenv("TGBUP_TEST_DC") == "1"}, nil
}

func (s setup) clientOptions() tgclient.Options {
	// A separate session file keeps a test-server login from replacing the real one.
	session := s.paths.Session()
	if s.testDC {
		session += "-test"
	}
	return tgclient.Options{
		APIID:        s.secrets.APIID,
		APIHash:      s.secrets.APIHash,
		SessionPath:  session,
		Version:      version,
		MaxFloodWait: s.cfg.Sync.MaxFloodWait.D(),
		TestDC:       s.testDC,
	}
}
