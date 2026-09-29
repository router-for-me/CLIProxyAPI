package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponseModelComparisonAndLog(t *testing.T) {
	for _, tc := range []struct {
		name     string
		models   []string
		returned string
		mismatch bool
	}{
		{name: "absent", models: []string{"", "  "}},
		{name: "requested model", models: []string{"requested"}, returned: "requested"},
		{name: "mapped model", models: []string{"mapped"}, returned: "mapped"},
		{name: "different model", models: []string{"other"}, returned: "other", mismatch: true},
		{name: "case differs", models: []string{"Requested"}, returned: "Requested"},
		{name: "requested prefix", models: []string{"requested-2026-09-01"}, returned: "requested-2026-09-01"},
		{name: "mapped case differs", models: []string{"MAPPED"}, returned: "MAPPED"},
		{name: "mapped prefix", models: []string{"mapped-2026-09-01"}, returned: "mapped-2026-09-01"},
		{name: "reverse prefix still warns", models: []string{"request"}, returned: "request", mismatch: true},
		{name: "suffix", models: []string{"other-requested"}, returned: "other-requested"},
		{name: "provider path", models: []string{"vendor/requested"}, returned: "vendor/requested"},
		{name: "provider path case differs", models: []string{"vendor/REQUESTED"}, returned: "vendor/REQUESTED"},
		{name: "provider path with dated mapped model", models: []string{"vendor/mapped-2026-09-01"}, returned: "vendor/mapped-2026-09-01", mismatch: true},
		{name: "nested provider path", models: []string{"accounts/vendor/models/requested"}, returned: "accounts/vendor/models/requested"},
		{name: "provider path reverse prefix still warns", models: []string{"vendor/request"}, returned: "vendor/request", mismatch: true},
		{name: "provider path other model still warns", models: []string{"vendor/other"}, returned: "vendor/other", mismatch: true},
		{name: "provider path only still warns", models: []string{"vendor/"}, returned: "vendor/", mismatch: true},
		{name: "compatible difference survives matching frames", models: []string{"mapped", "Requested", "", "requested"}, returned: "Requested"},
		{name: "warning supersedes compatible difference", models: []string{"Requested", "other"}, returned: "other", mismatch: true},
		{name: "compatible difference cannot erase warning", models: []string{"other", "Requested"}, returned: "other", mismatch: true},
		{name: "mismatch survives later frames", models: []string{"mapped", "other", "", "requested"}, returned: "other", mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{
				OriginModelName: "requested",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "mapped", IsModelMapped: true},
			}
			for _, name := range tc.models {
				info.ObserveResponseModel(name)
			}
			other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 0, 0, 1)
			var stored struct {
				ResponseModel *relaycommon.ResponseModel `json:"response_model"`
			}
			require.NoError(t, common.UnmarshalJsonStr(other.JSONString(), &stored))
			if tc.returned == "" {
				assert.Nil(t, stored.ResponseModel)
				return
			}
			require.NotNil(t, stored.ResponseModel)
			assert.Equal(t, &relaycommon.ResponseModel{
				RequestedModel: "requested", UpstreamModel: "mapped", ReturnedModel: tc.returned,
			}, stored.ResponseModel)
			assert.NotContains(t, other.JSONString(), "mismatch")
			assert.Equal(t, tc.mismatch, stored.ResponseModel.Mismatch())
			assert.Equal(t, "mapped", info.UpstreamModelName)
		})
	}
}

func TestResponseModelLogOmitsUnchangedModel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream string
		returned string
		mapped   bool
		record   bool
	}{
		{name: "same model", upstream: "requested", returned: "requested"},
		{name: "no upstream name", returned: "requested"},
		{name: "mapped model", upstream: "mapped", returned: "mapped", mapped: true, record: true},
		{name: "mapped response echoes request", upstream: "mapped", returned: "requested", mapped: true, record: true},
		{name: "prefix difference", upstream: "requested", returned: "requested-2026-09-01", record: true},
		{name: "case difference", upstream: "requested", returned: "REQUESTED", record: true},
		{name: "mismatch", upstream: "requested", returned: "other", record: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{
				OriginModelName: "requested",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: tc.upstream, IsModelMapped: tc.mapped},
			}
			info.ObserveResponseModel(tc.returned)
			other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 0, 0, 1)
			require.NotNil(t, info.ResponseModel)
			assert.Equal(t, float64(1), other.Snapshot()["model_ratio"])
			if tc.record {
				assert.Equal(t, *info.ResponseModel, other.Snapshot()["response_model"])
			} else {
				assert.NotContains(t, other.Snapshot(), "response_model")
			}
		})
	}
}

func TestResponseModelEmptyExpectedNamesDoNotMatchEveryPrefix(t *testing.T) {
	for _, tc := range []struct{ requested, upstream string }{
		{}, {upstream: "mapped"}, {requested: "requested"},
	} {
		t.Run(tc.requested+"/"+tc.upstream, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				OriginModelName: tc.requested,
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: tc.upstream},
			}
			info.ObserveResponseModel("other")
			require.NotNil(t, info.ResponseModel)
			assert.True(t, info.ResponseModel.Mismatch())
		})
	}
}

func TestResponseModelExpectedProviderPath(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested string
		upstream  string
		returned  string
		mismatch  bool
	}{
		{name: "requested carries provider path", requested: "vendor/requested", returned: "requested", mismatch: true},
		{name: "same provider path on both sides", requested: "vendor/requested", returned: "vendor/requested-2026-09-01"},
		{name: "provider path alone does not match", requested: "vendor/requested", returned: "vendor", mismatch: true},
		{name: "different model behind same provider still warns", requested: "vendor/requested", returned: "vendor/other", mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				OriginModelName: tc.requested,
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: tc.upstream},
			}
			info.ObserveResponseModel(tc.returned)
			require.NotNil(t, info.ResponseModel)
			assert.Equal(t, tc.returned, info.ResponseModel.ReturnedModel)
			assert.Equal(t, tc.mismatch, info.ResponseModel.Mismatch())
		})
	}
}

func TestResponseModelRetryResetsObservation(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "requested")
	info := &relaycommon.RelayInfo{OriginModelName: "requested"}
	info.InitChannelMeta(c)
	info.ObserveResponseModel("other")
	require.True(t, info.ResponseModel.Mismatch())
	info.InitChannelMeta(c)
	assert.Nil(t, info.ResponseModel)
	info.ObserveResponseModel("requested")
	assert.False(t, info.ResponseModel.Mismatch())
}

func TestResponseModelHandlersCaptureBeforeConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	chat := `{"id":"chat_1","model":"returned","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`
	chatStream := "data: " + `{"id":"chat_1","model":"returned","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}` + "\n\ndata: " + `{"id":"chat_1","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}` + "\n\ndata: [DONE]\n\n"
	responses := `{"id":"resp_1","model":"returned","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`
	responsesStream := "data: " + `{"type":"response.created","response":{"id":"resp_1","model":"returned"}}` + "\n\ndata: " + `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}` + "\n\n"
	claudeBody := `{"id":"msg_1","type":"message","model":"returned","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`
	claudeStream := "data: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","model":"returned","role":"assistant","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}` + "\n\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\ndata: " + `{"type":"message_stop"}` + "\n\n"
	geminiBody := `{"modelVersion":"returned","candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}`
	geminiStream := "data: " + geminiBody + "\n\n"
	for _, tc := range []struct {
		name    string
		body    string
		format  types.RelayFormat
		stream  bool
		handler func(*gin.Context, *relaycommon.RelayInfo, *http.Response) (*dto.Usage, *types.NewAPIError)
	}{
		{"chat", chat, types.RelayFormatOpenAI, false, openai.OpenaiHandler},
		{"chat stream", chatStream, types.RelayFormatOpenAI, true, openai.OaiStreamHandler},
		{"responses", responses, types.RelayFormatOpenAIResponses, false, openai.OaiResponsesHandler},
		{"responses stream", responsesStream, types.RelayFormatOpenAIResponses, true, openai.OaiResponsesStreamHandler},
		{"responses to chat", responses, types.RelayFormatOpenAI, false, openai.OaiResponsesToChatHandler},
		{"responses to chat stream", responsesStream, types.RelayFormatOpenAI, true, openai.OaiResponsesToChatStreamHandler},
		{"responses buffered to chat", responsesStream, types.RelayFormatOpenAI, false, openai.OaiResponsesToChatBufferedStreamHandler},
		{"chat to responses", chat, types.RelayFormatOpenAIResponses, false, openai.OaiChatToResponsesHandler},
		{"chat to responses stream", chatStream, types.RelayFormatOpenAIResponses, true, openai.OaiChatToResponsesStreamHandler},
		{"claude", claudeBody, types.RelayFormatClaude, false, func(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
			return claude.ClaudeHandler(c, resp, info)
		}},
		{"claude stream", claudeStream, types.RelayFormatClaude, true, func(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
			return claude.ClaudeStreamHandler(c, resp, info)
		}},
		{"claude to responses stream", claudeStream, types.RelayFormatOpenAIResponses, true, func(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
			return claude.ClaudeResponsesStreamHandler(c, resp, info)
		}},
		{"gemini", geminiBody, types.RelayFormatGemini, false, gemini.GeminiTextGenerationHandler},
		{"gemini stream", geminiStream, types.RelayFormatGemini, true, gemini.GeminiTextGenerationStreamHandler},
		{"gemini to chat", geminiBody, types.RelayFormatOpenAI, false, gemini.GeminiChatHandler},
		{"gemini to responses", geminiBody, types.RelayFormatOpenAIResponses, false, gemini.GeminiResponsesHandler},
		{"gemini to responses stream", geminiStream, types.RelayFormatOpenAIResponses, true, gemini.GeminiResponsesStreamHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{
				OriginModelName: "requested", RelayFormat: tc.format, RelayMode: relayconstant.RelayModeChatCompletions,
				IsStream: tc.stream, DisablePing: true, StartTime: time.Now(),
				ChannelMeta:         &relaycommon.ChannelMeta{UpstreamModelName: "mapped"},
				ThinkingContentInfo: relaycommon.ThinkingContentInfo{IsFirstThinkingContent: true},
			}
			usage, apiErr := tc.handler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))})
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, 5, usage.TotalTokens)
			require.NotNil(t, info.ResponseModel)
			assert.Equal(t, &relaycommon.ResponseModel{RequestedModel: "requested", UpstreamModel: "mapped", ReturnedModel: "returned"}, info.ResponseModel)
			assert.True(t, info.ResponseModel.Mismatch())
			other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 0, 0, 1)
			assert.Equal(t, *info.ResponseModel, other.Snapshot()["response_model"])
		})
	}
}

func TestResponseModelSharedResponsesAccumulator(t *testing.T) {
	// WebSocket turns and HTTP SSE use this same accumulator. A model-free
	// terminal must not erase the model declared before an interrupted stream.
	info := &relaycommon.RelayInfo{OriginModelName: "requested", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "mapped"}}
	accumulator := service.NewResponsesUsageAccumulator(info)
	accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.created", Response: &dto.OpenAIResponsesResponse{Model: "returned"}})
	accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.failed", Response: &dto.OpenAIResponsesResponse{Usage: &dto.Usage{InputTokens: 2, OutputTokens: 3}}})
	assert.Equal(t, 5, accumulator.Finish().TotalTokens)
	require.NotNil(t, info.ResponseModel)
	assert.True(t, info.ResponseModel.Mismatch())
	assert.Equal(t, "returned", info.ResponseModel.ReturnedModel)
}

func TestResponseModelDoesNotRecordSynthesizedModel(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "requested", RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "mapped"},
	}
	_, apiErr := openai.OaiResponsesToChatBufferedStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	})
	require.Nil(t, apiErr)
	assert.Nil(t, info.ResponseModel)
}
