package tgclient

import (
	"context"
	"errors"
	"strings"

	"github.com/gotd/td/tgerr"
)

type ErrKind int

const (
	KindOther ErrKind = iota
	KindFlood
	KindInaccessible
	KindAuth
	KindFileReference
	KindCanceled
)

func (k ErrKind) String() string {
	return [...]string{"other", "flood", "inaccessible", "auth", "file_reference", "canceled"}[k]
}

func Classify(err error) ErrKind {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return KindCanceled
	}
	if _, ok := tgerr.AsFloodWait(err); ok {
		return KindFlood
	}
	if tgerr.Is(err, "AUTH_KEY_UNREGISTERED", "AUTH_KEY_INVALID", "AUTH_KEY_DUPLICATED",
		"SESSION_REVOKED", "SESSION_EXPIRED", "USER_DEACTIVATED", "USER_DEACTIVATED_BAN") {
		return KindAuth
	}
	if tgerr.Is(err, "CHANNEL_PRIVATE", "CHANNEL_INVALID", "CHAT_FORBIDDEN", "CHAT_ID_INVALID",
		"PEER_ID_INVALID", "USER_BANNED_IN_CHANNEL", "CHANNEL_PUBLIC_GROUP_NA") {
		return KindInaccessible
	}
	if rpc, ok := tgerr.As(err); ok && strings.HasPrefix(rpc.Type, "FILE_REFERENCE_") {
		return KindFileReference
	}
	return KindOther
}
