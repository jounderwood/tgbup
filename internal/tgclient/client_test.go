package tgclient

import (
	"testing"

	"github.com/gotd/td/telegram/dcs"
)

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
