package advancedcustom_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel/advancedcustom"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSGLangChannelProtocols(t *testing.T) {
	apiType, ok := common.ChannelType2APIType(constant.ChannelTypeSGLang)
	require.True(t, ok)
	adaptor := relay.GetAdaptor(apiType)
	require.NotNil(t, adaptor)
	assert.Equal(t, "advanced_custom", adaptor.GetChannelName())
	assert.Contains(t, common.GetEndpointTypesByChannelType(constant.ChannelTypeSGLang, "served-model"), constant.EndpointTypeAnthropic)
	assert.Contains(t, common.GetEndpointTypesByChannelType(constant.ChannelTypeSGLang, "served-model"), constant.EndpointTypeOpenAIResponse)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSGLang, ChannelBaseUrl: "https://inference.example/prefix", ApiKey: "test-key"}}
	info.ChannelOtherSettings.AdvancedCustom = common.GetAdvancedCustomPreset(constant.ChannelTypeSGLang)
	adaptor.Init(info)
	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/responses", "/v1/messages"} {
		info.RequestURLPath = path
		adaptor = &advancedcustom.Adaptor{}
		adaptor.Init(info)
		url, err := adaptor.GetRequestURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://inference.example/prefix"+path, url)
	}
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
		info.RelayFormat = format
		info.RequestURLPath = "/v1/messages"
		adaptor = &advancedcustom.Adaptor{}
		adaptor.Init(info)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		headers := http.Header{}
		require.NoError(t, adaptor.SetupRequestHeader(c, &headers, info))
		assert.Equal(t, "Bearer test-key", headers.Get("Authorization"))
	}
	info.RequestURLPath = "/v1/chat/completions"
	adaptor = &advancedcustom.Adaptor{}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request := &dto.GeneralOpenAIRequest{Model: "served-model"}
	converted, err := adaptor.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	assert.Same(t, request, converted)
	info.RequestURLPath = "/v1/messages"
	adaptor = &advancedcustom.Adaptor{}
	adaptor.Init(info)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	claudeRequest := &dto.ClaudeRequest{Model: "served-model", Thinking: &dto.Thinking{Type: "adaptive"}}
	converted, err = adaptor.ConvertClaudeRequest(c, info, claudeRequest)
	require.NoError(t, err)
	assert.Same(t, claudeRequest, converted)
	info.RequestURLPath = "/v1beta/models/test:generateContent"
	adaptor = &advancedcustom.Adaptor{}
	adaptor.Init(info)
	_, err = adaptor.ConvertGeminiRequest(nil, info, &dto.GeminiChatRequest{})
	require.Error(t, err)
}

func TestSGLangRerankRequestAndResponse(t *testing.T) {
	service.InitHttpClient()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/prefix/v1/rerank", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		var request dto.RerankRequest
		require.NoError(t, common.DecodeJson(r.Body, &request))
		assert.Equal(t, "served-reranker", request.Model)
		assert.Equal(t, []any{"first", "second"}, request.Documents)
		_, _ = io.WriteString(w, `[{"index":1,"score":0.9,"document":"second","meta_info":{"prompt_tokens":5}}]`)
	}))
	defer upstream.Close()
	info := &relaycommon.RelayInfo{
		ChannelMeta:    &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSGLang, ChannelBaseUrl: upstream.URL + "/prefix", ApiKey: "test-key"},
		RequestURLPath: "/rerank", RelayMode: relayconstant.RelayModeRerank,
		RelayFormat:  types.RelayFormatOpenAI,
		RerankerInfo: &relaycommon.RerankerInfo{Documents: []any{"first", "second"}, ReturnDocuments: true},
	}
	info.SetEstimatePromptTokens(12)
	adaptor := &advancedcustom.Adaptor{}
	info.ChannelOtherSettings.AdvancedCustom = common.GetAdvancedCustomPreset(constant.ChannelTypeSGLang)
	adaptor.Init(info)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/rerank", nil).WithContext(context.Background())
	converted, err := adaptor.ConvertRerankRequest(c, info.RelayMode, dto.RerankRequest{Model: "served-reranker", Query: "query", Documents: info.Documents})
	require.NoError(t, err)
	body, err := common.Marshal(converted)
	require.NoError(t, err)
	response, err := adaptor.DoRequest(c, info, bytes.NewReader(body))
	require.NoError(t, err)
	usage, apiErr := adaptor.DoResponse(c, response.(*http.Response), info)
	require.Nil(t, apiErr)
	assert.Equal(t, 12, usage.(*dto.Usage).TotalTokens)
	var decoded dto.RerankResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &decoded))
	assert.Equal(t, []dto.RerankResponseResult{{Index: 1, RelevanceScore: 0.9, Document: map[string]any{"text": "second"}}}, decoded.Results)
	assert.Equal(t, 12, decoded.Usage.PromptTokens)
	assert.Zero(t, decoded.Usage.CompletionTokens)
}

func TestSGLangRerankRejectsMalformedResults(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `[{"index":99,"score":1}]`, `[{"index":-1,"score":0}]`, `[{"index":0}]`, `[{"score":1}]`} {
		t.Run(body, func(t *testing.T) {
			adaptor := &advancedcustom.Adaptor{}
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{AdvancedCustom: common.GetAdvancedCustomPreset(constant.ChannelTypeSGLang)}}, RequestURLPath: "/v1/rerank", RelayMode: relayconstant.RelayModeRerank, RerankerInfo: &relaycommon.RerankerInfo{Documents: []any{"only document"}}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			usage, err := adaptor.DoResponse(c, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, info)
			require.NotNil(t, err)
			assert.Nil(t, usage)
			assert.Empty(t, recorder.Body.String())
		})
	}
}
