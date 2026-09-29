package dto

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type OpenAIResponsesCompactionRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input,omitempty"`
	Instructions       json.RawMessage `json:"instructions,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	// Codex compact request parity:
	// https://github.com/openai/codex/commit/53d59722268dde82fb93c1f37964ce196c2a86d7
	// https://github.com/openai/codex/commit/5d6f23a27bf9c90709af527a7108c1c2eadf5123
	Tools                json.RawMessage `json:"tools,omitempty"`
	ParallelToolCalls    json.RawMessage `json:"parallel_tool_calls,omitempty"`
	Reasoning            *Reasoning      `json:"reasoning,omitempty"`
	ServiceTier          string          `json:"service_tier,omitempty"`
	PromptCacheKey       json.RawMessage `json:"prompt_cache_key,omitempty"`
	PromptCacheOptions   json.RawMessage `json:"prompt_cache_options,omitempty"`
	PromptCacheRetention json.RawMessage `json:"prompt_cache_retention,omitempty"`
	Text                 json.RawMessage `json:"text,omitempty"`
}

func (r *OpenAIResponsesCompactionRequest) GetTokenCountMeta() *types.TokenCountMeta {
	var parts []string
	if len(r.Instructions) > 0 {
		parts = append(parts, string(r.Instructions))
	}
	if len(r.Input) > 0 {
		parts = append(parts, string(r.Input))
	}
	return &types.TokenCountMeta{
		CombineText: strings.Join(parts, "\n"),
	}
}

func (r *OpenAIResponsesCompactionRequest) IsStream(c *http.Request) bool {
	return false
}

func (r *OpenAIResponsesCompactionRequest) SetModelName(modelName string) {
	if modelName != "" {
		r.Model = modelName
	}
}
