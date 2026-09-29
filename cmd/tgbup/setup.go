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
	return setup{paths: p, cfg: cfg, secrets: sec}, nil
}

func (s setup) clientOptions() tgclient.Options {
	return tgclient.Options{
		APIID:        s.secrets.APIID,
		APIHash:      s.secrets.APIHash,
		SessionPath:  s.paths.Session(),
		Version:      version,
		MaxFloodWait: s.cfg.Sync.MaxFloodWait.D(),
	}
}
