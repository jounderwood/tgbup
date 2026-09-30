package tgclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/takeout"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/time/rate"
)

type Options struct {
	APIID        int
	APIHash      string
	SessionPath  string
	Version      string
	MaxFloodWait time.Duration
	// TestDC connects to Telegram's test servers, a separate environment with its own accounts.
	TestDC bool
}

var ErrNotLoggedIn = errors.New("telegram session is not authorized: run `tgbup login`")

func Run(ctx context.Context, o Options, fn func(ctx context.Context, c *telegram.Client) error) error {
	if err := os.MkdirAll(filepath.Dir(o.SessionPath), 0o700); err != nil {
		return err
	}
	client := telegram.NewClient(o.APIID, o.APIHash, telegramOptions(o))
	return client.Run(ctx, func(ctx context.Context) error { return fn(ctx, client) })
}

func telegramOptions(o Options) telegram.Options {
	opts := telegram.Options{
		SessionStorage: &session.FileStorage{Path: o.SessionPath},
		NoUpdates:      true,
		Middlewares: []telegram.Middleware{
			floodwait.NewSimpleWaiter().WithMaxWait(o.MaxFloodWait),
			ratelimit.New(rate.Every(100*time.Millisecond), 5),
		},
		Device: telegram.DeviceConfig{DeviceModel: "tgbup", SystemVersion: runtime.GOOS, AppVersion: o.Version},
	}
	if o.TestDC {
		opts.DC = 2
		opts.DCList = dcs.Test()
	}
	return opts
}

func RequireAuth(ctx context.Context, c *telegram.Client) error {
	st, err := c.Auth().Status(ctx)
	if err != nil {
		return fmt.Errorf("auth status: %w", err)
	}
	if !st.Authorized {
		return ErrNotLoggedIn
	}
	return nil
}

func SelfID(ctx context.Context, raw *tg.Client) (int64, error) {
	us, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
	if err != nil {
		return 0, fmt.Errorf("get self: %w", err)
	}
	for _, uc := range us {
		if u, ok := uc.(*tg.User); ok {
			return u.ID, nil
		}
	}
	return 0, errors.New("get self: empty response")
}

func WithTakeout(ctx context.Context, c *telegram.Client, maxFileSize int64, fn func(ctx context.Context, raw *tg.Client) error) error {
	cfg := takeout.Config{
		MessageUsers: true, MessageChats: true, MessageMegagroups: true, MessageChannels: true,
		Files: true, FileMaxSize: maxFileSize,
	}
	err := takeout.Run(ctx, c, cfg, func(ctx context.Context, tc *takeout.Client) error {
		return fn(ctx, tg.NewClient(tc))
	})
	if rpc, ok := tgerr.As(err); ok && rpc.Type == "TAKEOUT_INIT_DELAY" {
		return fmt.Errorf("telegram wants you to confirm the data export: open Telegram, allow the request "+
			"in the service notifications (or wait %s), then rerun: %w", time.Duration(rpc.Argument)*time.Second, err)
	}
	return err
}
