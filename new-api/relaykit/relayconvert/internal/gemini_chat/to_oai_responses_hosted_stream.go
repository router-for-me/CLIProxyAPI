package geminichat

import (
	"fmt"

	"github.com/QuantumNous/new-api/relaykit/dto"
	oaichat "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/oai_chat"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

// GeminiHostedStreamBridge accumulates grounding metadata until the provider
// stream ends. Gemini commonly reports the search queries after emitting the
// answer text; delaying the synthetic Responses item keeps one complete,
// canonical action instead of emitting partial or duplicate tool calls.
type GeminiHostedStreamBridge struct {
	queries []string
	seen    map[string]struct{}
}

func NewGeminiHostedStreamBridge() *GeminiHostedStreamBridge {
	return &GeminiHostedStreamBridge{seen: make(map[string]struct{})}
}

func (b *GeminiHostedStreamBridge) Observe(response *dto.GeminiChatResponse) {
	if b == nil {
		return
	}
	if b.seen == nil {
		b.seen = make(map[string]struct{})
	}
	for _, query := range GroundingWebSearchQueries(response) {
		if _, exists := b.seen[query]; exists {
			continue
		}
		b.seen[query] = struct{}{}
		b.queries = append(b.queries, query)
	}
}

func (b *GeminiHostedStreamBridge) Finalize(state *oaichat.ChatToResponsesStreamState) ([]oaichat.ChatToResponsesStreamEvent, error) {
	if b == nil || len(b.queries) == 0 {
		return nil, nil
	}
	if state == nil {
		return nil, fmt.Errorf("Chat-to-Responses stream state is required")
	}
	action, err := kitutil.Marshal(map[string]any{
		"type":    "search",
		"queries": append([]string(nil), b.queries...),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal Gemini web-search action: %w", err)
	}
	callID := fmt.Sprintf("ws_%s", kitutil.GetUUID())
	events, err := state.StartHostedTool(oaichat.HostedToolStreamStart{
		Type:   "web_search_call",
		ID:     callID,
		Action: action,
	})
	if err != nil {
		return nil, err
	}
	completed, err := state.CompleteHostedTool(oaichat.HostedToolStreamResult{
		Type: "web_search_call",
		ID:   callID,
	})
	if err != nil {
		return nil, err
	}
	return append(events, completed...), nil
}
