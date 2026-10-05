package usage

import "github.com/tidwall/gjson"

type TokenEvidence struct{ Fields map[string]int64 }

var tokenEvidencePaths = []string{
	"prompt_tokens", "input_tokens", "completion_tokens", "output_tokens", "total_tokens",
	"cache_read_input_tokens", "cache_creation_input_tokens", "thinking_tokens",
	"prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens",
	"prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_write_tokens",
	"prompt_tokens_details.cache_creation_tokens", "input_tokens_details.cache_creation_tokens",
	"completion_tokens_details.reasoning_tokens", "output_tokens_details.reasoning_tokens", "output_tokens_details.thinking_tokens",
	"promptTokenCount", "candidatesTokenCount", "thoughtsTokenCount", "totalTokenCount", "cachedContentTokenCount", "toolUsePromptTokenCount", "tool_use_prompt_token_count",
	"cache_read_tokens", "cacheReadTokens", "tool_use_tokens", "total_tool_use_tokens", "toolUseTokens", "totalToolUseTokens",
	"total_input_tokens", "total_output_tokens", "reasoning_tokens", "total_thought_tokens",
	"cached_tokens", "total_cached_tokens", "cache_creation_tokens", "cacheCreationTokens", "cache_write_tokens", "cacheWriteTokens",
}

// CaptureTokenEvidence copies only numeric provider token fields from a usage object.
func CaptureTokenEvidence(data []byte) *TokenEvidence {
	var evidence map[string]int64
	nodeRoot := gjson.ParseBytes(data)
	for _, path := range tokenEvidencePaths {
		node := nodeRoot.Get(path)
		if node.Type != gjson.Number {
			continue
		}
		if evidence == nil {
			evidence = make(map[string]int64)
		}
		evidence[path] = node.Int()
	}
	if evidence == nil {
		return nil
	}
	return &TokenEvidence{Fields: evidence}
}

// SafeTokenEvidence also filters evidence supplied by third-party SDK plugins.
func SafeTokenEvidence(source *TokenEvidence) map[string]int64 {
	var evidence map[string]int64
	if source == nil {
		return nil
	}
	for _, path := range tokenEvidencePaths {
		if value, ok := source.Fields[path]; ok {
			if evidence == nil {
				evidence = make(map[string]int64)
			}
			evidence[path] = value
		}
	}
	return evidence
}

func MergeTokenEvidence(existing, update *TokenEvidence) *TokenEvidence {
	fields := SafeTokenEvidence(existing)
	for key, value := range SafeTokenEvidence(update) {
		if fields == nil {
			fields = make(map[string]int64)
		}
		fields[key] = value
	}
	if fields == nil {
		return nil
	}
	return &TokenEvidence{Fields: fields}
}
