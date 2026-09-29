package zhipu_4v

import (
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

//	type ZhipuMessage struct {
//		Role       string `json:"role,omitempty"`
//		Content    string `json:"content,omitempty"`
//		ToolCalls  any    `json:"tool_calls,omitempty"`
//		ToolCallId any    `json:"tool_call_id,omitempty"`
//	}
//
//	type ZhipuRequest struct {
//		Model       string         `json:"model"`
//		Stream      bool           `json:"stream,omitempty"`
//		Messages    []ZhipuMessage `json:"messages"`
//		Temperature float64        `json:"temperature,omitempty"`
//		TopP        float64        `json:"top_p,omitempty"`
//		MaxTokens   int            `json:"max_tokens,omitempty"`
//		Stop        []string       `json:"stop,omitempty"`
//		RequestId   string         `json:"request_id,omitempty"`
//		Tools       any            `json:"tools,omitempty"`
//		ToolChoice  any            `json:"tool_choice,omitempty"`
//	}
//
//	type ZhipuV4TextResponseChoice struct {
//		Index        int `json:"index"`
//		ZhipuMessage `json:"message"`
//		FinishReason string `json:"finish_reason"`
//	}
type ZhipuV4Response struct {
	Id                  string                         `json:"id"`
	Created             int64                          `json:"created"`
	Model               string                         `json:"model"`
	TextResponseChoices []dto.OpenAITextResponseChoice `json:"choices"`
	Usage               dto.Usage                      `json:"usage"`
	Error               types.OpenAIError              `json:"error"`
}

//
//type ZhipuV4StreamResponseChoice struct {
//	Index        int          `json:"index,omitempty"`
//	Delta        ZhipuMessage `json:"delta"`
//	FinishReason *string      `json:"finish_reason,omitempty"`
//}

type ZhipuV4StreamResponse struct {
	Id      string                                    `json:"id"`
	Created int64                                     `json:"created"`
	Choices []dto.ChatCompletionsStreamResponseChoice `json:"choices"`
	Usage   dto.Usage                                 `json:"usage"`
}

type tokenData struct {
	Token      string
	ExpiryTime time.Time
}
