package palm

import "github.com/QuantumNous/new-api/relaykit/dto"

type PaLMChatMessage struct {
	Author  string `json:"author"`
	Content string `json:"content"`
}

type PaLMFilter struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type PaLMPrompt struct {
	Messages []PaLMChatMessage `json:"messages"`
}

type PaLMChatRequest struct {
	Prompt         PaLMPrompt `json:"prompt"`
	Temperature    *float64   `json:"temperature,omitempty"`
	CandidateCount int        `json:"candidateCount,omitempty"`
	TopP           float64    `json:"topP,omitempty"`
	TopK           uint       `json:"topK,omitempty"`
}

type PaLMError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

type PaLMChatResponse struct {
	Candidates []PaLMChatMessage `json:"candidates"`
	Messages   []dto.Message     `json:"messages"`
	Filters    []PaLMFilter      `json:"filters"`
	Error      PaLMError         `json:"error"`
}
