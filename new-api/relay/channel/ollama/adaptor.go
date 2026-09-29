package ollama

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	adaptor := claude.Adaptor{}
	return adaptor.ConvertClaudeRequest(c, info, request)
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	// OpenAI-compatible chat responses are handled by the OpenAI adaptor, which
	// relies on the thinking-to-content state initialized here.
	(&openai.Adaptor{}).Init(info)
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		return (&claude.Adaptor{}).GetRequestURL(info)
	default:
		switch info.RelayMode {
		case relayconstant.RelayModeEmbeddings:
			return fmt.Sprintf("%s/api/embed", info.ChannelBaseUrl), nil
		case relayconstant.RelayModeResponses:
			return fmt.Sprintf("%s/v1/responses", info.ChannelBaseUrl), nil
		case relayconstant.RelayModeResponsesCompact:
			return fmt.Sprintf("%s/v1/responses/compact", info.ChannelBaseUrl), nil
		case relayconstant.RelayModeCompletions:
			return fmt.Sprintf("%s/api/generate", info.ChannelBaseUrl), nil
		default:
			if info.ChannelOtherSettings.OllamaOpenAIChat {
				return fmt.Sprintf("%s/v1/chat/completions", info.ChannelBaseUrl), nil
			}
			return fmt.Sprintf("%s/api/chat", info.ChannelBaseUrl), nil
		}
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Authorization", "Bearer "+info.ApiKey)
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		claude.CommonClaudeHeadersOperation(c, req, info)
		anthropicVersion := c.Request.Header.Get("anthropic-version")
		if anthropicVersion == "" {
			anthropicVersion = "2023-06-01"
		}
		req.Set("anthropic-version", anthropicVersion)
	}
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	switch info.RelayMode {
	case relayconstant.RelayModeCompletions:
		return openAIToGenerate(c, request)
	default:
		if info.ChannelOtherSettings.OllamaOpenAIChat {
			return (&openai.Adaptor{}).ConvertOpenAIRequest(c, info, request)
		}
		return openAIChatToOllamaChat(c, request)
	}
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return requestOpenAI2Embeddings(request), nil
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	adaptor := openai.Adaptor{}
	return adaptor.ConvertOpenAIResponsesRequest(c, info, request)
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		adaptor := claude.Adaptor{}
		return adaptor.DoResponse(c, resp, info)
	default:
		switch info.RelayMode {
		case relayconstant.RelayModeEmbeddings:
			return ollamaEmbeddingHandler(c, info, resp)
		case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
			adaptor := openai.Adaptor{}
			return adaptor.DoResponse(c, resp, info)
		default:
			// /api/generate always returns native NDJSON; chat follows the channel setting.
			if info.RelayMode != relayconstant.RelayModeCompletions && info.ChannelOtherSettings.OllamaOpenAIChat {
				adaptor := openai.Adaptor{}
				return adaptor.DoResponse(c, resp, info)
			}
			if info.IsStream {
				return ollamaStreamHandler(c, info, resp)
			}
			return ollamaChatHandler(c, info, resp)
		}
	}
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
