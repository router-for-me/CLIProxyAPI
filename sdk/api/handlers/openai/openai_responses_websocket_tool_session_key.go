package openai

import "strings"

func responsesWebsocketToolSessionKey(scopedSessionKey string, connectionID string) string {
	if scopedSessionKey = strings.TrimSpace(scopedSessionKey); scopedSessionKey != "" {
		return scopedSessionKey
	}
	connectionID = strings.TrimSpace(connectionID)
	if connectionID == "" {
		return ""
	}
	return "connection:" + connectionID
}
