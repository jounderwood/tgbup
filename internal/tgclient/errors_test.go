package tgclient_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotd/td/tgerr"

	"github.com/jounderwood/tgbup/internal/tgclient"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want tgclient.ErrKind
	}{
		{tgerr.New(420, "FLOOD_WAIT_30"), tgclient.KindFlood},
		{fmt.Errorf("wrapped: %w", tgerr.New(420, "FLOOD_WAIT_900")), tgclient.KindFlood},
		{tgerr.New(401, "AUTH_KEY_UNREGISTERED"), tgclient.KindAuth},
		{tgerr.New(401, "SESSION_REVOKED"), tgclient.KindAuth},
		{tgerr.New(400, "CHANNEL_PRIVATE"), tgclient.KindInaccessible},
		{tgerr.New(403, "CHAT_FORBIDDEN"), tgclient.KindInaccessible},
		{tgerr.New(400, "FILE_REFERENCE_EXPIRED"), tgclient.KindFileReference},
		{fmt.Errorf("x: %w", context.Canceled), tgclient.KindCanceled},
		{errors.New("boom"), tgclient.KindOther},
	}
	for _, c := range cases {
		if got := tgclient.Classify(c.err); got != c.want {
			t.Errorf("Classify(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
