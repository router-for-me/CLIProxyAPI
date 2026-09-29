package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestDeepCopyResponses(t *testing.T) {
	t.Run("nil source", func(t *testing.T) {
		clone, err := common.DeepCopy[dto.OpenAIResponsesRequest](nil)
		require.Error(t, err)
		assert.Nil(t, clone)
	})

	for _, raw := range []json.RawMessage{nil, {}, []byte(`null`), []byte(`false`), []byte(`0`), []byte(`[]`), []byte(`{"x":1}`)} {
		t.Run(fmt.Sprintf("raw_%q_nil_%t", raw, raw == nil), func(t *testing.T) {
			src := &dto.OpenAIResponsesRequest{}
			populateResponsesCloneFixture(t, reflect.ValueOf(src).Elem(), raw)
			want := &dto.OpenAIResponsesRequest{}
			populateResponsesCloneFixture(t, reflect.ValueOf(want).Elem(), raw)
			clone, err := common.DeepCopy(src)
			require.NoError(t, err)
			assert.Equal(t, src, clone, "including internal state and explicit zero pointers")

			// Mutate every reachable field, including nested pointers and raw bytes.
			// A later channel attempt must still see the original request.
			populateResponsesCloneFixture(t, reflect.ValueOf(clone).Elem(), []byte(`true`))
			assert.Equal(t, want, src)
			retry, err := common.DeepCopy(src)
			require.NoError(t, err)
			assert.Equal(t, want, retry)
		})
	}

	t.Run("absent optional fields", func(t *testing.T) {
		src := &dto.OpenAIResponsesRequest{Model: "gpt-4.1", Input: []byte(`"hello"`)}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, src, clone)
		clone.Input[1] = 'H'
		assert.Equal(t, json.RawMessage(`"hello"`), src.Input)
	})

	t.Run("large input", func(t *testing.T) {
		input := bytes.Repeat([]byte("a"), 1<<20)
		input[0], input[len(input)-1] = '"', '"'
		src := &dto.OpenAIResponsesRequest{Model: "gpt-4.1", Input: input}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, src, clone)
		clone.Input[len(input)/2] = 'b'
		assert.Equal(t, byte('a'), src.Input[len(input)/2])
	})

	t.Run("model mapping on successive channel attempts", func(t *testing.T) {
		src := &dto.OpenAIResponsesRequest{Model: "client-model", Input: []byte(`"hello"`)}
		for _, model := range []string{"first-channel-model", "retry-channel-model"} {
			request, err := common.DeepCopy(src)
			require.NoError(t, err)
			assert.Equal(t, json.RawMessage(`"hello"`), request.Input)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("model_mapping", fmt.Sprintf(`{"client-model":%q}`, model))
			info := &relaycommon.RelayInfo{OriginModelName: src.Model}
			require.NoError(t, helper.ModelMappedHelper(c, info, request))
			assert.Equal(t, model, request.Model)
			assert.Equal(t, "client-model", src.Model)
			request.Input[1] = 'H'
			assert.Equal(t, json.RawMessage(`"hello"`), src.Input)
		}
	})
}

// Populate all mutable fields so newly added request fields also exercise the
// retry-isolation contract, rather than silently escaping a hand-written list.
func populateResponsesCloneFixture(t *testing.T, value reflect.Value, raw json.RawMessage) {
	t.Helper()
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			populateResponsesCloneFixture(t, value.Field(i), raw)
		}
	case reflect.Pointer:
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		populateResponsesCloneFixture(t, value.Elem(), raw)
	case reflect.Slice:
		require.Equal(t, reflect.TypeFor[json.RawMessage](), value.Type())
		if value.Len() > 0 {
			value.Index(0).SetUint('!')
		}
		value.Set(reflect.ValueOf(json.RawMessage(bytes.Clone(raw))))
	case reflect.String:
		value.SetString(value.String() + "gpt-4.1")
	case reflect.Bool:
		value.SetBool(bytes.Equal(raw, []byte(`true`)))
	case reflect.Int:
		if bytes.Equal(raw, []byte(`true`)) {
			value.SetInt(1)
		}
	case reflect.Uint:
		if bytes.Equal(raw, []byte(`true`)) {
			value.SetUint(1)
		}
	case reflect.Float64:
		if bytes.Equal(raw, []byte(`true`)) {
			value.SetFloat(1)
		}
	default:
		require.FailNow(t, "extend clone fixture for new field type", "%s", value.Type())
	}
}

func TestRequestDeepCopyRawMutation(t *testing.T) {
	t.Run("edits before cloning are included", func(t *testing.T) {
		src := &dto.OpenAIResponsesRequest{Input: json.RawMessage(`"hello"`)}
		src.Input[1] = 'H'
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, json.RawMessage(`"Hello"`), clone.Input)
	})

	t.Run("edits after cloning are isolated in both directions", func(t *testing.T) {
		input := make(json.RawMessage, len(`"hello"`), 32)
		copy(input, `"hello"`)
		src := &dto.OpenAIResponsesRequest{Input: input}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)

		clone.Input[1] = 'H'
		assert.Equal(t, json.RawMessage(`"hello"`), src.Input)
		src.Input[2] = 'A'
		assert.Equal(t, json.RawMessage(`"Hello"`), clone.Input)
		clone.Input = append(clone.Input, ' ')
		src.Input = append(src.Input, '\n')
		assert.Equal(t, json.RawMessage("\"Hello\" "), clone.Input)
		assert.Equal(t, json.RawMessage("\"hAllo\"\n"), src.Input)
		clone.Input = json.RawMessage(`"replacement"`)
		assert.Equal(t, json.RawMessage("\"hAllo\"\n"), src.Input)
	})

	t.Run("raw map and slice entries retain nil and empty values", func(t *testing.T) {
		src := &struct {
			Fields map[string]json.RawMessage
			Items  []json.RawMessage
		}{
			Fields: map[string]json.RawMessage{"absent": nil, "empty": {}, "value": []byte(`false`)},
			Items:  []json.RawMessage{nil, {}, []byte(`0`)},
		}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, src, clone)
		clone.Fields["value"][0] = '!'
		clone.Items[2][0] = '1'
		delete(clone.Fields, "absent")
		assert.Contains(t, src.Fields, "absent")
		assert.Equal(t, json.RawMessage(`false`), src.Fields["value"])
		assert.Equal(t, json.RawMessage(`0`), src.Items[2])
	})
}

func TestRequestDeepCopyOtherProtocols(t *testing.T) {
	t.Run("chat nested fields remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"gpt-4.1","max_tokens":0,"stream":false,"thinking_budget":128,
			"messages":[{"role":"assistant","content":[{"type":"text","text":"hello"}],"tool_calls":[{"id":"call_1"}]}],
			"response_format":{"type":"json_schema","json_schema":{"strict":false}}
		}`, func(clone *dto.GeneralOpenAIRequest) {
			clone.Messages[0].ToolCalls[0] = '!'
			clone.Messages[0].Content.([]any)[0].(map[string]any)["text"] = "changed"
			clone.ResponseFormat.JsonSchema[0] = '!'
			*clone.MaxTokens = 1
			*clone.Stream = true
		})
	})

	t.Run("claude dynamic tools and raw fields remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"claude-sonnet-4-5","max_tokens":1024,"temperature":0,
			"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
			"metadata":{"user_id":"test"},"messages":[{"role":"user","content":"hello"}]
		}`, func(clone *dto.ClaudeRequest) {
			clone.Metadata[0] = '!'
			clone.Tools.([]any)[0].(map[string]any)["input_schema"].(map[string]any)["type"] = "array"
			clone.Messages[0].Content = "changed"
			*clone.Temperature = 1
		})
	})

	t.Run("gemini nested parts and configuration remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"contents":[{"role":"model","parts":[{"text":"hello","thoughtSignature":"signature","inlineData":{"mimeType":"text/plain","data":"aGk="}}]}],
			"tools":[{"googleSearch":{}}],"generationConfig":{"responseJsonSchema":{"type":"object"}}
		}`, func(clone *dto.GeminiChatRequest) {
			clone.Contents[0].Parts[0].ThoughtSignature[1] = '!'
			clone.Contents[0].Parts[0].InlineData.Data = "changed"
			clone.Tools[0] = '!'
			clone.GenerationConfig.ResponseJsonSchema[0] = '!'
		})
	})

	t.Run("image extra parameters remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"gpt-image-1","prompt":"a cat","n":1,"watermark":false,
			"image":["https://example.com/image.png"],"parameters":{"seed":0}
		}`, func(clone *dto.ImageRequest) {
			clone.Image[0] = '!'
			clone.Extra["parameters"][0] = '!'
			clone.Extra["new_field"] = json.RawMessage(`true`)
			*clone.N = 2
			*clone.Watermark = true
		})
	})

	t.Run("audio raw parameters and speed remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"tts-1","input":"hello","voice":"alloy","speed":1,"metadata":{"tag":"test"},"ref_audio":"sample"
		}`, func(clone *dto.AudioRequest) {
			clone.Metadata[0] = '!'
			clone.RefAudio[1] = '!'
			*clone.Speed = 2
		})
	})

	t.Run("embedding dynamic input and dimensions remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{"model":"text-embedding-3-small","input":[[1,2],[3]],"dimensions":256}`,
			func(clone *dto.EmbeddingRequest) {
				clone.Input.([]any)[0].([]any)[0] = float64(9)
				*clone.Dimensions = 512
			})
	})

	t.Run("rerank documents and options remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"rerank-v3.5","query":"hello","documents":["first","second"],"top_n":1,"return_documents":false
		}`, func(clone *dto.RerankRequest) {
			clone.Documents[0] = "changed"
			*clone.TopN = 2
			*clone.ReturnDocuments = true
		})
	})
}

// A channel attempt may mutate its clone without changing the request used by
// later attempts. Decode the expected request independently to avoid aliases.
func assertRequestCloneIsolation[T any](t *testing.T, body string, mutate func(*T)) {
	t.Helper()
	var src, want T
	require.NoError(t, common.UnmarshalJsonStr(body, &src))
	require.NoError(t, common.UnmarshalJsonStr(body, &want))
	clone, err := common.DeepCopy(&src)
	require.NoError(t, err)
	require.Equal(t, &want, clone)
	wantJSON, err := common.Marshal(&want)
	require.NoError(t, err)
	cloneJSON, err := common.Marshal(clone)
	require.NoError(t, err)
	assert.Equal(t, string(wantJSON), string(cloneJSON), "preserve the outbound JSON")
	mutate(clone)
	assert.Equal(t, want, src, "a channel attempt must not mutate the original request")
	retry, err := common.DeepCopy(&src)
	require.NoError(t, err)
	assert.Equal(t, &want, retry)
}

func BenchmarkRequestDeepCopy(b *testing.B) {
	for _, size := range []int{256, 4 << 10, 1 << 20, 10 << 20, 40 << 20} {
		input := bytes.Repeat([]byte("a"), size)
		input[0], input[len(input)-1] = '"', '"'
		src := &dto.OpenAIResponsesRequest{Model: "gpt-4.1", Input: input}
		for _, method := range []struct {
			name string
			copy func(*dto.OpenAIResponsesRequest) (*dto.OpenAIResponsesRequest, error)
		}{
			{"reflect", func(src *dto.OpenAIResponsesRequest) (*dto.OpenAIResponsesRequest, error) {
				var dst dto.OpenAIResponsesRequest
				err := copier.CopyWithOption(&dst, src, copier.Option{DeepCopy: true, IgnoreEmpty: true})
				return &dst, err
			}},
			{"raw_bytes", common.DeepCopy[dto.OpenAIResponsesRequest]},
		} {
			b.Run(fmt.Sprintf("%dB/%s", size, method.name), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for b.Loop() {
					clone, err := method.copy(src)
					require.NoError(b, err)
					require.Len(b, clone.Input, size)
				}
			})
		}
	}
}
