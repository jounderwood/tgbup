package tgclient

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tgerr"
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
