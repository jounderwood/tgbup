package tgclient

import (
	"context"
	"fmt"
	"slices"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// ReadOnlyMethods are the only Telegram API methods tgbup may call. Every
// other call — sending, editing, deleting, forwarding, marking as read,
// reacting, joining, leaving, changing settings — is refused before it is
// sent, so a bug cannot change anything in the account.
//
// Each entry says why it is needed; none of them changes chats or messages.
var ReadOnlyMethods = map[string]string{
	// Reading the archive.
	"messages.getDialogs":        "the chat list",
	"messages.getDialogFilters":  "Telegram folders, to choose chats",
	"messages.getHistory":        "messages of a chat; with limit 1, its message count",
	"messages.search":            "pinned messages; message counts of a kind",
	"messages.getSearchCounters": "message counts by kind (photos, files…) for the dry run",
	"messages.getReplies":        "message counts of a forum topic",
	"messages.getForumTopics":    "forum topics and their titles",
	"messages.getMessages":       "messages by id: one again for a fresh file reference, or up to 100 to confirm they were deleted",
	"channels.getMessages":       "channel messages by id: one again for a fresh file reference, or up to 100 to confirm they were deleted",
	"channels.getFullChannel":    "which basic group a supergroup was upgraded from",
	"users.getUsers":             "the account's own id; whether the session is signed in",
	"upload.getFile":             "downloading media",
	// Signing in: creates this computer's session, like any Telegram app.
	"auth.sendCode":       "login: send the code",
	"auth.signIn":         "login: the code",
	"account.getPassword": "login: two-step verification parameters",
	"auth.checkPassword":  "login: two-step verification password",
	// Takeout: an export session that Telegram asks the person to allow.
	"account.initTakeoutSession":   "start a data export (sync --takeout)",
	"account.finishTakeoutSession": "end that export",
	// gotd's own connection housekeeping.
	"help.getConfig":           "connection: data center list",
	"help.getNearestDc":        "connection: nearest data center",
	"help.getCdnConfig":        "connection: CDN keys",
	"auth.exportAuthorization": "connection: use the session on a file's data center",
	"auth.importAuthorization": "connection: use the session on a file's data center",
}

// ForbiddenError is a refused call.
type ForbiddenError struct{ Method string }

func (e *ForbiddenError) Error() string {
	return fmt.Sprintf("tgbup only reads from Telegram and refused to call %s", e.Method)
}

// readOnly is the middleware that enforces ReadOnlyMethods.
type readOnly struct{}

func (readOnly) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		if m := methodName(input); ReadOnlyMethods[m] == "" {
			return &ForbiddenError{Method: m}
		}
		return next.Invoke(ctx, input, output)
	}
}

// methodName is the TL name of a call, looking inside the takeout wrapper.
func methodName(in bin.Encoder) string {
	if w, ok := in.(*tg.InvokeWithTakeoutRequest); ok {
		return methodName(w.Query)
	}
	if t, ok := in.(interface{ TypeName() string }); ok {
		return t.TypeName()
	}
	return fmt.Sprintf("%T", in)
}

// AllowedMethods lists ReadOnlyMethods, sorted, for documentation and tests.
func AllowedMethods() []string {
	out := make([]string, 0, len(ReadOnlyMethods))
	for m := range ReadOnlyMethods {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}
