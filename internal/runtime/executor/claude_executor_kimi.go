package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	log "github.com/sirupsen/logrus"
)

// Kimi Claude compatibility constants
const (
	// Thinking type constants
	thinkingTypeAdaptive = "adaptive"
	thinkingTypeAuto     = "auto"
	thinkingTypeEnabled  = "enabled"

	// Effort level constants
	effortMinimal = "minimal"
	effortNone    = "none"
	effortLow     = "low"
	effortMedium  = "medium"
	effortHigh    = "high"
	effortMax     = "max"
	effortXHigh   = "xhigh"

	// Thinking budget token constants
	// These values are derived from Claude's adaptive thinking behavior:
	// - minimal/none: Disable thinking entirely (0 tokens)
	// - low: Basic reasoning (1024 tokens)
	// - medium: Standard reasoning (4096 tokens, default)
	// - high/max/xhigh: Deep reasoning (8192 tokens)
	budgetMinimal = 0
	budgetLow     = 1024
	budgetMedium  = 4096
	budgetHigh    = 8192

	// Kimi endpoint identifier
	kimiAPIHost = "api.kimi.com"

	// Beta header values
	betaHeaderBase   = "claude-code-20250219"
	betaHeaderFull   = "claude-code-20250219,interleaved-thinking-2025-05-14"
)

// isKimiClaudeCompatBaseURL checks if the given base URL points to Kimi's Anthropic-compatible endpoint.
// It performs strict host matching to avoid false positives from URLs containing "api.kimi.com" as a substring.
func isKimiClaudeCompatBaseURL(baseURL string) bool {
	normalized := strings.ToLower(strings.TrimSpace(baseURL))
	parsed, err := url.Parse(normalized)
	if err != nil {
		// Fallback to substring matching if URL parsing fails
		return strings.Contains(normalized, kimiAPIHost)
	}
	return strings.Contains(parsed.Host, kimiAPIHost)
}

// applyKimiClaudeBetaHeader sets the Anthropic-Beta header appropriate for Kimi's compatibility layer.
// In strict mode, only the base beta features are included to maximize compatibility.
func applyKimiClaudeBetaHeader(req *http.Request, strict bool) {
	if req == nil {
		return
	}
	if strict {
		req.Header.Set("Anthropic-Beta", betaHeaderBase)
		return
	}
	req.Header.Set("Anthropic-Beta", betaHeaderFull)
}

// isRetryableKimiInvalidRequest determines if a 400 response from Kimi indicates a retryable error.
// Kimi returns generic "invalid_request_error" messages for various incompatibility issues.
// This function identifies those cases to trigger a strict-mode retry.
func isRetryableKimiInvalidRequest(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(string(responseBody)))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "invalid_request_error") ||
		strings.Contains(lower, "invalid request error")
}

// applyKimiClaudeCompatibility applies all necessary transformations to make a Claude request
// compatible with Kimi's Anthropic-compatible endpoint.
func applyKimiClaudeCompatibility(body []byte, strict bool) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	out := stripKimiIncompatibleFields(body, strict)
	out = stripToolReferences(out)
	return out
}

// stripKimiIncompatibleFields removes or transforms fields that Kimi's endpoint cannot handle.
// This includes metadata, context_management, and adaptive thinking configurations.
func stripKimiIncompatibleFields(body []byte, strict bool) []byte {
	out := body
	// Kimi's Anthropic compatibility endpoint may reject metadata.user_id when
	// Claude Code forwards structured values encoded as JSON strings. Metadata is
	// optional for request execution, so strip it to avoid provider-side 400s.
	out, _ = sjson.DeleteBytes(out, "metadata")
	// Kimi often rejects context_management from Claude payloads.
	out, _ = sjson.DeleteBytes(out, "context_management")

	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(out, "thinking.type").String()))
	if thinkingType == thinkingTypeAdaptive || thinkingType == thinkingTypeAuto {
		effort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(out, "output_config.effort").String()))
		// Convert adaptive/auto to a stable enabled budget to avoid upstream drift.
		budget := budgetMedium // Default to medium
		switch effort {
		case effortMinimal, effortNone:
			budget = budgetMinimal
		case effortLow:
			budget = budgetLow
		case effortHigh, effortMax, effortXHigh:
			budget = budgetHigh
		}
		if budget <= 0 {
			out, _ = sjson.DeleteBytes(out, "thinking")
		} else {
			out, _ = sjson.SetBytes(out, "thinking.type", thinkingTypeEnabled)
			out, _ = sjson.SetBytes(out, "thinking.budget_tokens", budget)
		}
		out, _ = sjson.DeleteBytes(out, "output_config.effort")
	}
	if strict {
		// Strict mode keeps thinking enabled but removes incompatible effort controls.
		out, _ = sjson.DeleteBytes(out, "output_config.effort")
	}
	if oc := gjson.GetBytes(out, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
		out, _ = sjson.DeleteBytes(out, "output_config")
	}
	return out
}

// stripToolReferences removes tool_reference content blocks from tool_result messages.
// Kimi's endpoint does not support the tool_reference type, which is Anthropic-specific.
func stripToolReferences(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}

	out := body
	messages.ForEach(func(mi, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		var modified bool
		newBlocks := make([]interface{}, 0, len(content.Array()))
		for _, block := range content.Array() {
			if block.Get("type").String() != "tool_result" {
				newBlocks = append(newBlocks, block.Value())
				continue
			}
			inner := block.Get("content")
			if !inner.IsArray() {
				newBlocks = append(newBlocks, block.Value())
				continue
			}
			innerArr := inner.Array()
			filtered := make([]interface{}, 0, len(innerArr))
			innerModified := false
			for _, ib := range innerArr {
				if ib.Get("type").String() == "tool_reference" {
					innerModified = true
					continue
				}
				filtered = append(filtered, ib.Value())
			}
			if !innerModified {
				newBlocks = append(newBlocks, block.Value())
				continue
			}
			modified = true
			bm, ok := block.Value().(map[string]interface{})
			if !ok {
				newBlocks = append(newBlocks, block.Value())
				continue
			}
			bm["content"] = filtered
			newBlocks = append(newBlocks, bm)
		}
		if modified {
			if b, err := json.Marshal(newBlocks); err == nil {
				out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content", mi.Int()), b)
			} else {
				log.Warnf("failed to strip tool_reference from message %d: %v", mi.Int(), err)
			}
		}
		return true
	})
	return out
}

// tryKimiCompatRetry encapsulates the Kimi compatibility retry logic shared by Execute and ExecuteStream.
// It attempts a request with basic compatibility handling, and if that fails with a retryable error,
// it retries once in strict mode with more aggressive field stripping.
func (e *ClaudeExecutor) tryKimiCompatRetry(
	ctx context.Context,
	isKimiCompatTarget bool,
	statusCode int,
	respBody []byte,
	sendUpstream func([]byte, bool) (*http.Response, error),
	bodyForUpstream, bodyForTranslation *[]byte,
) (*http.Response, error) {
	if !isKimiCompatTarget {
		return nil, nil
	}
	if !isRetryableKimiInvalidRequest(statusCode, respBody) {
		return nil, nil
	}

	log.Debugf("Kimi returned retryable 400, attempting strict-mode retry")
	strictBody := applyKimiClaudeCompatibility(*bodyForUpstream, true)
	*bodyForUpstream = strictBody
	*bodyForTranslation = strictBody

	retryResp, retryErr := sendUpstream(strictBody, true)
	if retryErr != nil {
		log.Warnf("Kimi strict-mode retry failed: %v", retryErr)
		return nil, retryErr
	}
	return retryResp, nil
}
