package util

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIResponsesCallIDLimit = 64

// ShortenOpenAIResponsesCallIDIfNeeded keeps Responses API call_id values under
// OpenAI's 64 byte limit while preserving a stable, low-collision mapping.
func ShortenOpenAIResponsesCallIDIfNeeded(id string) string {
	if len(id) <= openAIResponsesCallIDLimit {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLen := openAIResponsesCallIDLimit - len(suffix)
	if prefixLen <= 0 {
		return suffix[len(suffix)-openAIResponsesCallIDLimit:]
	}
	return id[:prefixLen] + suffix
}

// NormalizeOpenAIResponsesInputCallIDs rewrites input[*].call_id values that
// exceed the Responses API limit. The mapping is deterministic, so matching
// function_call and function_call_output items stay paired.
func NormalizeOpenAIResponsesInputCallIDs(rawJSON []byte) []byte {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.IsArray() {
		return rawJSON
	}

	changed := false
	rebuilt := make([]json.RawMessage, 0, len(input.Array()))
	for _, item := range input.Array() {
		itemRaw := []byte(item.Raw)
		callIDResult := item.Get("call_id")
		if callIDResult.Type == gjson.String {
			callID := callIDResult.String()
			if strings.TrimSpace(callID) != "" {
				shortened := ShortenOpenAIResponsesCallIDIfNeeded(callID)
				if shortened != callID {
					updated, err := sjson.SetBytes(itemRaw, "call_id", shortened)
					if err == nil {
						itemRaw = updated
						changed = true
					}
				}
			}
		}
		rebuilt = append(rebuilt, json.RawMessage(itemRaw))
	}
	if !changed {
		return rawJSON
	}

	inputRaw, err := json.Marshal(rebuilt)
	if err != nil {
		return rawJSON
	}
	updated, err := sjson.SetRawBytes(rawJSON, "input", inputRaw)
	if err != nil {
		return rawJSON
	}
	return updated
}
