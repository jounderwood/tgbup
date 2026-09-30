package tgclient

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgmock"
)

type flakyInvoker struct {
	fails, calls int
	err          error
}

func (f *flakyInvoker) Invoke(context.Context, bin.Encoder, bin.Decoder) error {
	f.calls++
	if f.calls <= f.fails {
		return f.err
	}
	return nil
}

func TestRetryNotInited(t *testing.T) {
	notInited := &tgerr.Error{Code: 400, Type: "CONNECTION_NOT_INITED"}
	mw := retryNotInited{attempts: 3, delay: time.Millisecond}

	inv := &flakyInvoker{fails: 2, err: notInited}
	if err := mw.Handle(inv).Invoke(context.Background(), nil, nil); err != nil || inv.calls != 3 {
		t.Fatalf("transient CONNECTION_NOT_INITED not retried: err=%v calls=%d", err, inv.calls)
	}

	inv = &flakyInvoker{fails: 5, err: notInited}
	if err := mw.Handle(inv).Invoke(context.Background(), nil, nil); !tgerr.Is(err, "CONNECTION_NOT_INITED") || inv.calls != 3 {
		t.Fatalf("retries not bounded: err=%v calls=%d", err, inv.calls)
	}

	other := &flakyInvoker{fails: 1, err: &tgerr.Error{Code: 400, Type: "PHONE_CODE_INVALID"}}
	if err := mw.Handle(other).Invoke(context.Background(), nil, nil); err == nil || other.calls != 1 {
		t.Fatalf("other errors must not be retried: err=%v calls=%d", err, other.calls)
	}
}

func TestTakeoutRequestFileLimit(t *testing.T) {
	if got := takeoutRequest(0).FileMaxSize; got != maxTakeoutFileSize {
		t.Fatalf("unlimited config must request the maximum, got %d", got)
	}
	if got := takeoutRequest(2 << 30).FileMaxSize; got != 2<<30 {
		t.Fatalf("configured limit not passed: %d", got)
	}
	if r := takeoutRequest(0); !r.Files || !r.MessageUsers || !r.MessageChats || !r.MessageMegagroups || !r.MessageChannels {
		t.Fatalf("takeout scopes = %+v", r)
	}
}

func TestTakeoutWrapsWorkAndFinish(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectFunc(func(b bin.Encoder) {
		if _, ok := b.(*tg.AccountInitTakeoutSessionRequest); !ok {
			t.Fatalf("first call %T, want init", b)
		}
	}).ThenResult(&tg.AccountTakeout{ID: 77})
	wrapped := func(inner string) func(bin.Encoder) {
		return func(b bin.Encoder) {
			w, ok := b.(*tg.InvokeWithTakeoutRequest)
			if !ok || w.TakeoutID != 77 {
				t.Fatalf("%s sent as %T, want invokeWithTakeout(77)", inner, b)
			}
			if got := fmt.Sprintf("%T", w.Query); got != inner {
				t.Fatalf("wrapped query %s, want %s", got, inner)
			}
		}
	}
	mock.ExpectFunc(wrapped("*tg.UsersGetUsersRequest")).ThenResult(&tg.UserClassVector{})
	mock.ExpectFunc(wrapped("*tg.AccountFinishTakeoutSessionRequest")).ThenTrue()

	err := runTakeout(context.Background(), mock, 0, func(ctx context.Context, raw *tg.Client) error {
		_, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTelegramOptionsSelectDCList(t *testing.T) {
	prod := telegramOptions(Options{SessionPath: "/s"})
	if prod.DC != 0 || prod.DCList.Options != nil {
		t.Fatalf("prod must use gotd defaults, got DC=%d list=%v", prod.DC, prod.DCList.Options)
	}
	test := telegramOptions(Options{SessionPath: "/s", TestDC: true})
	if test.DC != 2 || len(test.DCList.Options) != len(dcs.Test().Options) {
		t.Fatalf("test DC options = DC %d, %d addrs", test.DC, len(test.DCList.Options))
	}
}
