package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// conversationMessage carries one distinguishable turn of a conversation.
type conversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// messagesBody carries a transcript in the messages shape.
type messagesBody struct {
	Messages []conversationMessage `json:"messages"`
}

// responsesBody carries a transcript in the Responses shape beside an optional cache lane.
type responsesBody struct {
	PromptCacheKey string                `json:"prompt_cache_key,omitempty"`
	Input          []conversationMessage `json:"input"`
}

// conversationTurns builds the transcript one conversation presents at the given turn
// count. The opening message names the conversation, because identity derives from the
// first complete user input.
func conversationTurns(conversation string, turns int) []conversationMessage {
	messages := make([]conversationMessage, 0, turns)
	messages = append(messages, conversationMessage{Role: "user", Content: conversation + " opening"})
	for turn := 1; turn < turns; turn++ {
		messages = append(messages, conversationMessage{Role: "user", Content: fmt.Sprintf("%s turn %d", conversation, turn)})
	}
	return messages
}

// marshalBody renders one request body.
func marshalBody(t *testing.T, body any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return raw
}

// messagesTurn builds one messages-format body for a conversation at the given turn count.
func messagesTurn(t *testing.T, conversation string, turns int) []byte {
	t.Helper()
	return marshalBody(t, messagesBody{Messages: conversationTurns(conversation, turns)})
}

// responsesRoute builds one Responses-format route request for a conversation at the
// given turn count, naming the credential cache lane the request carries.
func responsesRoute(t *testing.T, conversation, promptCacheKey string, turns int) pluginapi.ModelRouteRequest {
	t.Helper()
	return pluginapi.ModelRouteRequest{
		RequestedModel:     "Iterative-Model",
		SourceFormat:       "openai-response",
		AvailableProviders: []string{"codex", "claude"},
		Body: marshalBody(t, responsesBody{
			PromptCacheKey: promptCacheKey,
			Input:          conversationTurns(conversation, turns),
		}),
	}
}
