package convmeta

import (
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// GuessRelayFormatFromRequest infers the relay format from a request DTO's
// concrete type. Moved from relay/common (which keeps a delegating alias).
func GuessRelayFormatFromRequest(req any) (types.RelayFormat, bool) {
	switch req.(type) {
	case *dto.GeneralOpenAIRequest, dto.GeneralOpenAIRequest:
		return types.RelayFormatOpenAI, true
	case *dto.OpenAIResponsesRequest, dto.OpenAIResponsesRequest:
		return types.RelayFormatOpenAIResponses, true
	case *dto.ClaudeRequest, dto.ClaudeRequest:
		return types.RelayFormatClaude, true
	case *dto.GeminiChatRequest, dto.GeminiChatRequest:
		return types.RelayFormatGemini, true
	case *dto.EmbeddingRequest, dto.EmbeddingRequest:
		return types.RelayFormatEmbedding, true
	case *dto.RerankRequest, dto.RerankRequest:
		return types.RelayFormatRerank, true
	case *dto.ImageRequest, dto.ImageRequest:
		return types.RelayFormatOpenAIImage, true
	case *dto.AudioRequest, dto.AudioRequest:
		return types.RelayFormatOpenAIAudio, true
	default:
		return "", false
	}
}
