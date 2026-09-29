package service

import "github.com/QuantumNous/new-api/pkg/wsmanager"

const ChannelDisabledCloseReason = "channel disabled or deleted"

func CloseActiveWebSocketsForChannel(channelID int, reason string) int {
	return wsmanager.CloseChannelsAndBroadcast([]int{channelID}, reason)
}

func CloseActiveWebSocketsForChannels(channelIDs []int, reason string) int {
	return wsmanager.CloseChannelsAndBroadcast(channelIDs, reason)
}
