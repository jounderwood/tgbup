package tgclient

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

type recordInvoker struct{ calls []string }

func (r *recordInvoker) Invoke(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
	r.calls = append(r.calls, methodName(input))
	return nil
}

func TestReadOnlyRefusesWritesBeforeSending(t *testing.T) {
	writes := []bin.Encoder{
		&tg.MessagesSendMessageRequest{}, &tg.MessagesSendMediaRequest{}, &tg.MessagesEditMessageRequest{},
		&tg.MessagesDeleteMessagesRequest{}, &tg.MessagesForwardMessagesRequest{}, &tg.MessagesReadHistoryRequest{},
		&tg.ChannelsReadHistoryRequest{}, &tg.MessagesSendReactionRequest{}, &tg.ChannelsJoinChannelRequest{},
		&tg.ChannelsLeaveChannelRequest{}, &tg.MessagesDeleteHistoryRequest{}, &tg.AccountUpdateProfileRequest{},
		&tg.ContactsBlockRequest{}, &tg.AuthLogOutRequest{}, &tg.AuthResetAuthorizationsRequest{},
		&tg.MessagesUpdatePinnedMessageRequest{}, &tg.MessagesSaveDraftRequest{}, &tg.MessagesUpdateDialogFilterRequest{},
		&tg.AccountUpdatePasswordSettingsRequest{}, &tg.AuthSignUpRequest{},
		// The takeout wrapper does not open a way around the guard.
		&tg.InvokeWithTakeoutRequest{Query: &tg.MessagesSendMessageRequest{}},
	}
	next := &recordInvoker{}
	guard := readOnly{}.Handle(next)
	for _, w := range writes {
		var forbidden *ForbiddenError
		if err := guard.Invoke(context.Background(), w, nil); !errors.As(err, &forbidden) {
			t.Errorf("%s: err = %v, want refused", methodName(w), err)
		}
	}
	if len(next.calls) != 0 {
		t.Fatalf("refused calls reached Telegram: %v", next.calls)
	}

	reads := []bin.Encoder{&tg.MessagesGetHistoryRequest{}, &tg.UploadGetFileRequest{},
		&tg.InvokeWithTakeoutRequest{Query: &tg.MessagesGetHistoryRequest{}}}
	for _, r := range reads {
		if err := guard.Invoke(context.Background(), r, nil); err != nil {
			t.Errorf("%s refused: %v", methodName(r), err)
		}
	}
}

// Every allowed name is a real Telegram method: a typo would refuse a call
// tgbup needs, or worse, look like a rule that is not there.
func TestAllowedMethodsExist(t *testing.T) {
	known := map[string]bool{}
	for _, name := range tg.TypesMap() {
		name, _, _ = strings.Cut(name, "#") // "messages.getHistory#4423e6c5"
		known[name] = true
	}
	for _, m := range AllowedMethods() {
		if !known[m] {
			t.Errorf("%s is not a Telegram method", m)
		}
	}
}

// The calls this package makes are all allowed; a new one must be added to
// ReadOnlyMethods with its reason, deliberately.
func TestCallsInUseAreAllowed(t *testing.T) {
	used := []bin.Encoder{
		&tg.MessagesGetDialogsRequest{}, &tg.MessagesGetDialogFiltersRequest{}, &tg.MessagesGetHistoryRequest{},
		&tg.MessagesSearchRequest{}, &tg.MessagesGetSearchCountersRequest{}, &tg.MessagesGetRepliesRequest{},
		&tg.MessagesGetForumTopicsRequest{}, &tg.MessagesGetMessagesRequest{}, &tg.ChannelsGetMessagesRequest{},
		&tg.ChannelsGetFullChannelRequest{}, &tg.UsersGetUsersRequest{}, &tg.UploadGetFileRequest{},
		&tg.AuthSendCodeRequest{}, &tg.AuthSignInRequest{}, &tg.AccountGetPasswordRequest{}, &tg.AuthCheckPasswordRequest{},
		&tg.AccountInitTakeoutSessionRequest{}, &tg.AccountFinishTakeoutSessionRequest{},
	}
	for _, u := range used {
		if ReadOnlyMethods[methodName(u)] == "" {
			t.Errorf("%s is used but not allowed", methodName(u))
		}
	}
	if !slices.IsSorted(AllowedMethods()) {
		t.Error("AllowedMethods is not sorted")
	}
}
