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
	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
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

// retryNotInited repeats a call on CONNECTION_NOT_INITED, which Telegram (test
// servers especially) sometimes returns transiently; gotd's own e2e tests do the same.
type retryNotInited struct {
	attempts int
	delay    time.Duration
}

func (r retryNotInited) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		var err error
		for i := 0; i < r.attempts; i++ {
			if err = next.Invoke(ctx, input, output); !tgerr.Is(err, "CONNECTION_NOT_INITED") {
				return err
			}
			select {
			case <-time.After(r.delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	}
}

func telegramOptions(o Options) telegram.Options {
	opts := telegram.Options{
		SessionStorage: &session.FileStorage{Path: o.SessionPath},
		NoUpdates:      true,
		Middlewares: []telegram.Middleware{
			retryNotInited{attempts: 3, delay: 500 * time.Millisecond},
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

// Telegram's largest file (Premium upload limit); without an explicit file_max_size
// takeout rejects files with TAKEOUT_FILE_TOO_BIG.
const maxTakeoutFileSize = 4000 << 20

func takeoutRequest(maxFileSize int64) *tg.AccountInitTakeoutSessionRequest {
	if maxFileSize <= 0 {
		maxFileSize = maxTakeoutFileSize
	}
	r := &tg.AccountInitTakeoutSessionRequest{}
	r.SetMessageUsers(true)
	r.SetMessageChats(true)
	r.SetMessageMegagroups(true)
	r.SetMessageChannels(true)
	r.SetFiles(true)
	r.SetFileMaxSize(maxFileSize)
	return r
}

type takeoutInvoker struct {
	id   int64
	next tg.Invoker
}

func (t takeoutInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	obj, ok := input.(bin.Object)
	if !ok {
		return fmt.Errorf("takeout: %T is not a TL object", input)
	}
	return t.next.Invoke(ctx, &tg.InvokeWithTakeoutRequest{TakeoutID: t.id, Query: obj}, output)
}

// runTakeout differs from gotd's takeout.Run in sending finishTakeoutSession
// inside invokeWithTakeout too; Telegram rejects it otherwise with TAKEOUT_REQUIRED.
func runTakeout(ctx context.Context, invoker tg.Invoker, maxFileSize int64, fn func(ctx context.Context, raw *tg.Client) error) error {
	session, err := tg.NewClient(invoker).AccountInitTakeoutSession(ctx, takeoutRequest(maxFileSize))
	if err != nil {
		return fmt.Errorf("init takeout session: %w", err)
	}
	raw := tg.NewClient(takeoutInvoker{id: session.ID, next: invoker})
	fnErr := fn(ctx, raw)
	finish := &tg.AccountFinishTakeoutSessionRequest{}
	finish.SetSuccess(fnErr == nil)
	if _, err := raw.AccountFinishTakeoutSession(ctx, finish); err != nil {
		return errors.Join(fnErr, fmt.Errorf("finish takeout session: %w", err))
	}
	return fnErr
}

func WithTakeout(ctx context.Context, c *telegram.Client, maxFileSize int64, fn func(ctx context.Context, raw *tg.Client) error) error {
	return takeoutError(runTakeout(ctx, c, maxFileSize, fn))
}

// TakeoutNotConfirmed means Telegram is waiting for the user to allow the
// data export in an app; it is an expected first-run step, not a failure.
type TakeoutNotConfirmed struct {
	Wait time.Duration
	err  error
}

func (e *TakeoutNotConfirmed) Error() string {
	return fmt.Sprintf("Telegram asks to confirm the data export: open Telegram, allow the request in the "+
		"\"Telegram\" service chat, then run the same command again (without confirming it is allowed in %s)", e.Wait)
}

func (e *TakeoutNotConfirmed) Unwrap() error { return e.err }

func takeoutError(err error) error {
	if rpc, ok := tgerr.As(err); ok && rpc.Type == "TAKEOUT_INIT_DELAY" {
		return &TakeoutNotConfirmed{Wait: time.Duration(rpc.Argument) * time.Second, err: err}
	}
	return err
}
