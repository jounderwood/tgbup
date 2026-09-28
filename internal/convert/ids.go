package convert

import "github.com/gotd/td/tg"

const channelIDOffset = 1000000000000

func ChatID(id int64) int64    { return -id }
func ChannelID(id int64) int64 { return -channelIDOffset - id }

func PeerID(p tg.PeerClass) int64 {
	switch v := p.(type) {
	case *tg.PeerUser:
		return v.UserID
	case *tg.PeerChat:
		return ChatID(v.ChatID)
	case *tg.PeerChannel:
		return ChannelID(v.ChannelID)
	}
	return 0
}

func InputPeerID(p tg.InputPeerClass) (int64, bool) {
	switch v := p.(type) {
	case *tg.InputPeerUser:
		return v.UserID, true
	case *tg.InputPeerChat:
		return ChatID(v.ChatID), true
	case *tg.InputPeerChannel:
		return ChannelID(v.ChannelID), true
	}
	return 0, false
}
