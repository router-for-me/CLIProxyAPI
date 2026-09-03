package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// ClaudeCacheAnnotation carries the prompt-cache facts that a Claude streaming
// response reports only once, in message_start: the split of cache_creation
// across the 5m and 1h pools, and the cache_miss_reason diagnostics object.
//
// The authoritative token totals arrive later, in message_delta, which drops
// both. Keeping them separate lets the executor publish the message_delta
// totals unchanged while still recording why the cache missed.
type ClaudeCacheAnnotation struct {
	CacheCreation5mTokens int64
	CacheCreation1hTokens int64
	CacheMissReason       string
	CacheMissedTokens     int64
}

// Empty reports whether the annotation carries nothing worth merging.
func (a ClaudeCacheAnnotation) Empty() bool {
	return a.CacheCreation5mTokens == 0 && a.CacheCreation1hTokens == 0 &&
		a.CacheMissReason == "" && a.CacheMissedTokens == 0
}

// Apply fills in only the fields the detail does not already carry, so a body
// that reported its own split or diagnostics always wins.
func (a ClaudeCacheAnnotation) Apply(detail usage.Detail) usage.Detail {
	if detail.CacheCreation5mTokens == 0 && detail.CacheCreation1hTokens == 0 {
		detail.CacheCreation5mTokens = a.CacheCreation5mTokens
		detail.CacheCreation1hTokens = a.CacheCreation1hTokens
	}
	if strings.TrimSpace(detail.CacheMissReason) == "" {
		detail.CacheMissReason = a.CacheMissReason
		if detail.CacheMissedTokens == 0 {
			detail.CacheMissedTokens = a.CacheMissedTokens
		}
	}
	return detail
}

// ParseClaudeCacheAnnotation extracts the cache split and miss diagnostics from
// a Claude SSE message_start line. Every other event returns false.
func ParseClaudeCacheAnnotation(line []byte) (ClaudeCacheAnnotation, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ClaudeCacheAnnotation{}, false
	}
	root := gjson.ParseBytes(payload)
	if root.Get("type").String() != "message_start" {
		return ClaudeCacheAnnotation{}, false
	}
	message := root.Get("message")
	if !message.Exists() {
		return ClaudeCacheAnnotation{}, false
	}
	annotation := ClaudeCacheAnnotation{
		CacheCreation5mTokens: message.Get("usage.cache_creation.ephemeral_5m_input_tokens").Int(),
		CacheCreation1hTokens: message.Get("usage.cache_creation.ephemeral_1h_input_tokens").Int(),
	}
	applyClaudeCacheMissReason(&annotation, message)
	if annotation.Empty() {
		return ClaudeCacheAnnotation{}, false
	}
	return annotation, true
}

func applyClaudeCacheMissReason(annotation *ClaudeCacheAnnotation, node gjson.Result) {
	reason := node.Get("diagnostics.cache_miss_reason")
	if !reason.Exists() {
		return
	}
	annotation.CacheMissReason = strings.TrimSpace(reason.Get("type").String())
	annotation.CacheMissedTokens = reason.Get("cache_missed_input_tokens").Int()
}

// claudeCacheDiagnosticsFromRoot reads the response-level diagnostics object a
// non-streaming Claude body carries beside its usage node.
func claudeCacheDiagnosticsFromRoot(root gjson.Result) (string, int64) {
	var annotation ClaudeCacheAnnotation
	applyClaudeCacheMissReason(&annotation, root)
	return annotation.CacheMissReason, annotation.CacheMissedTokens
}

// RequestMaxTokens reads the generation cap from the first payload that carries
// one, trying the Anthropic name and the two OpenAI-family spellings so a
// translated request still reports the cap the client asked for.
func RequestMaxTokens(payloads ...[]byte) int64 {
	for _, payload := range payloads {
		if len(payload) == 0 {
			continue
		}
		for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
			if node := gjson.GetBytes(payload, field); node.Exists() {
				if value := node.Int(); value > 0 {
					return value
				}
			}
		}
	}
	return 0
}
