package helps

import (
	"fmt"
	"strings"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResolveKimiBaseURL resolves the upstream API base URL for Kimi API requests based on auth.
func ResolveKimiBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if auth.Attributes != nil {
			if raw := strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/"); raw != "" {
				return raw
			}
		}
		if auth.Metadata != nil {
			if raw, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(raw) != "" {
				return strings.TrimRight(strings.TrimSpace(raw), "/")
			}
		}
		if kimiauth.IsKimiAIAuth(auth) {
			return kimiauth.KimiAIAPIBaseURL
		}
	}
	return kimiauth.KimiAPIBaseURL
}

// ResolveKimiResponsesURL resolves the upstream URL for Kimi Responses API requests.
func ResolveKimiResponsesURL(auth *cliproxyauth.Auth) string {
	baseURL := ResolveKimiBaseURL(auth)
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/responses"
	}
	return baseURL + "/v1/responses"
}

// ResolveKimiChatURL resolves the upstream URL for Kimi Chat Completions requests.
func ResolveKimiChatURL(auth *cliproxyauth.Auth) string {
	baseURL := ResolveKimiBaseURL(auth)
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/chat/completions"
	}
	return baseURL + "/v1/chat/completions"
}

// ResolveKimiClaudeBaseURL resolves the base URL for Kimi Claude Messages delegation.
func ResolveKimiClaudeBaseURL(auth *cliproxyauth.Auth) string {
	baseURL := ResolveKimiBaseURL(auth)
	return strings.TrimSuffix(baseURL, "/v1")
}

func isResponsesToolCall(item gjson.Result) bool {
	t := strings.TrimSpace(item.Get("type").String())
	return t == "function_call" || t == "custom_tool_call"
}

func isResponsesToolOutput(item gjson.Result) bool {
	t := strings.TrimSpace(item.Get("type").String())
	return t == "function_call_output" || t == "custom_tool_call_output"
}

// NormalizeKimiResponsesInput normalizes the input array for Kimi Responses API requests.
// When parallel function calls are emitted, Kimi requires all corresponding tool outputs
// to follow immediately without intervening non-tool messages (e.g. developer/user messages).
// This function defers intervening non-tool items until all outputs for the current batch
// of function calls have been emitted, keeping the outputs contiguous.
func NormalizeKimiResponsesInput(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, nil
	}

	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body, nil
	}

	items := input.Array()
	if len(items) == 0 {
		return body, nil
	}

	reordered := false
	result := make([]gjson.Result, 0, len(items))

	for i := 0; i < len(items); {
		if !isResponsesToolCall(items[i]) {
			result = append(result, items[i])
			i++
			continue
		}

		startCalls := i
		endCalls := i
		callIDs := make(map[string]int)
		callIDCount := 0
		for endCalls < len(items) && isResponsesToolCall(items[endCalls]) {
			callID := translatorcommon.ExtractResponsesCallID(items[endCalls])
			if callID != "" {
				callIDs[callID]++
				callIDCount++
			}
			endCalls++
		}

		result = append(result, items[startCalls:endCalls]...)

		if callIDCount == 0 {
			i = endCalls
			continue
		}

		needed := make(map[string]int, len(callIDs))
		for k, v := range callIDs {
			needed[k] = v
		}
		remainingNeeded := callIDCount
		lastMatchingIdx := -1

		for j := endCalls; j < len(items) && remainingNeeded > 0; j++ {
			it := items[j]
			if isResponsesToolCall(it) {
				break
			}
			if isResponsesToolOutput(it) {
				cid := translatorcommon.ExtractResponsesCallID(it)
				if count := needed[cid]; count > 0 {
					needed[cid]--
					remainingNeeded--
					lastMatchingIdx = j
				}
			}
		}

		if remainingNeeded == 0 && lastMatchingIdx >= endCalls {
			matchingOutputs := make([]gjson.Result, 0, callIDCount)
			interveningItems := make([]gjson.Result, 0)
			consumed := make(map[string]int, len(callIDs))
			for k, v := range callIDs {
				consumed[k] = v
			}

			for j := endCalls; j <= lastMatchingIdx; j++ {
				it := items[j]
				if isResponsesToolOutput(it) {
					cid := translatorcommon.ExtractResponsesCallID(it)
					if count := consumed[cid]; count > 0 {
						consumed[cid]--
						matchingOutputs = append(matchingOutputs, it)
						continue
					}
				}
				interveningItems = append(interveningItems, it)
			}

			if len(interveningItems) > 0 {
				reordered = true
			}

			result = append(result, matchingOutputs...)
			result = append(result, interveningItems...)

			i = lastMatchingIdx + 1
		} else {
			i = endCalls
		}
	}

	if !reordered {
		return body, nil
	}

	rebuilt := make([]string, 0, len(result))
	for _, it := range result {
		rebuilt = append(rebuilt, it.Raw)
	}

	updated, errSet := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if errSet != nil {
		return body, fmt.Errorf("normalize kimi responses input: %w", errSet)
	}
	return updated, nil
}
