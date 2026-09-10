package responses

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Keep text signature carriers off the client-visible reasoning timeline. The
// existing replay cache bounds their lifetime and size and supports Home storage.
func cacheGeminiResponsesTextSignatures(modelName, messageID, text string, signatures []string) bool {
	if messageID == "" || text == "" {
		return false
	}
	textHash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	items := make([][]byte, 0, len(signatures))
	for _, signature := range signatures {
		if _, ok := compatibleGeminiResponsesCarrierSignature(signature, geminiResponsesCarrierText); !ok {
			return false
		}
		item := []byte(`{"type":"thought_signature","targetKind":"text"}`)
		item, _ = sjson.SetBytes(item, "thoughtSignature", signature)
		item, _ = sjson.SetBytes(item, "targetHash", textHash)
		items = append(items, item)
	}
	return cache.CacheAntigravityReasoningReplayItems(thinking.ParseSuffix(modelName).ModelName, "gemini-responses-text:"+messageID, items)
}

func restoreGeminiResponsesTextSignatures(modelName string, items []gjson.Result) []gjson.Result {
	var restored []gjson.Result
	skip := make(map[int]bool)
	for index, item := range items {
		if skip[index] {
			continue
		}
		restored = append(restored, item)
		text, ok := openAIResponsesAssistantVisibleText(item)
		messageID := strings.TrimSpace(item.Get("id").String())
		if !ok || messageID == "" {
			continue
		}
		cached, found := cache.GetAntigravityReasoningReplayItems(thinking.ParseSuffix(modelName).ModelName, "gemini-responses-text:"+messageID)
		if !found {
			continue
		}
		// Replay the cached prefix in its original order, then retain uncached
		// explicit carriers. Skipping cached entries instead would reorder A,B
		// into B,A when the client also supplied an explicit carrier for A.
		replayed := make(map[string]bool)
		textHash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		for _, raw := range cached {
			entry := gjson.ParseBytes(raw)
			signature := entry.Get("thoughtSignature").String()
			if entry.Get("targetHash").String() != textHash {
				continue
			}
			carrier := []byte(`{"type":"reasoning","summary":[]}`)
			carrier, _ = sjson.SetBytes(carrier, "encrypted_content", encodeGeminiResponsesCarrier(signature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText))
			restored = append(restored, gjson.ParseBytes(carrier))
			replayed[signature] = true
		}
		for adjacent := index + 1; adjacent < len(items) && isOpenAIResponsesDetachedCarrier(items[adjacent]); adjacent++ {
			signature, direction, target, _, valid := decodeGeminiResponsesCarrier(items[adjacent].Get("encrypted_content").String())
			if valid && direction == geminiResponsesCarrierPrevious && target == geminiResponsesCarrierText && replayed[signature] {
				skip[adjacent] = true
			}
		}
	}
	return restored
}
