//go:build e2e

package tgclient

import (
	"context"
	"errors"

	"github.com/gotd/td/telegram"
)

// RunTestServerWritable is Run without the read-only guard, for the e2e
// fixture that creates and removes test chats. It exists only in builds with
// the e2e tag, never in the tgbup binary, and only on the test servers.
func RunTestServerWritable(ctx context.Context, o Options, fn func(ctx context.Context, c *telegram.Client) error) error {
	if !o.TestDC {
		return errors.New("writing is allowed only on Telegram's test servers")
	}
	return run(ctx, o, false, fn)
}
