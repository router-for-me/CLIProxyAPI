package sub2api

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRequestURLAlphaSearch(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeSub2API,
			ChannelBaseUrl: "https://sub2api.example",
		},
		RequestURLPath: "/v1/alpha/search",
		RelayMode:      relayconstant.RelayModeAlphaSearch,
	}

	url, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://sub2api.example/v1/alpha/search", url)
}

func TestAdaptorInheritsNewAPIResponsesCompactSupport(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeSub2API,
			ChannelBaseUrl: "https://sub2api.example",
		},
		RequestURLPath: "/v1/responses/compact",
		RelayMode:      relayconstant.RelayModeResponsesCompact,
	}

	url, err := adaptor.GetRequestURL(info)

	require.NoError(t, err)
	assert.Equal(t, "https://sub2api.example/v1/responses/compact", url)
	assert.Equal(t, "sub2api", adaptor.GetChannelName())
	assert.Empty(t, adaptor.GetModelList())
}

func TestConvertClaudeRequestPreservesAdaptiveThinkingForCompatibleModel(t *testing.T) {
	adaptor := &Adaptor{}
	maxTokens := uint(8192)
	temperature := 0.2
	topP := 0.99
	request := &dto.ClaudeRequest{
		Model:        "gpt-5.6-sol",
		MaxTokens:    &maxTokens,
		Temperature:  &temperature,
		TopP:         &topP,
		Thinking:     &dto.Thinking{Type: "adaptive", Display: "summarized"},
		OutputConfig: json.RawMessage(`{"effort":"xhigh","provider_option":true}`),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.6-sol",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeSub2API,
		},
	}

	converted, err := adaptor.ConvertClaudeRequest(nil, info, request)

	require.NoError(t, err)
	assert.Same(t, request, converted)
	require.NotNil(t, request.Thinking)
	assert.Equal(t, "adaptive", request.Thinking.Type)
	assert.Equal(t, "summarized", request.Thinking.Display)
	assert.JSONEq(t, `{"effort":"xhigh","provider_option":true}`, string(request.OutputConfig))
	assert.Same(t, &temperature, request.Temperature)
	assert.Same(t, &topP, request.TopP)
	assert.Equal(t, "xhigh", info.ReasoningEffort)
	assert.Equal(t, "gpt-5.6-sol", info.UpstreamModelName)
}
