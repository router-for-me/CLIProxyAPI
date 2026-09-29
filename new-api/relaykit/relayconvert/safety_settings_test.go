package relayconvert

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIToGeminiSafetySettings(t *testing.T) {
	converters := []struct {
		name    string
		convert func(t *testing.T, meta convmeta.Meta) *dto.GeminiChatRequest
	}{
		{
			name: "chat completions",
			convert: func(t *testing.T, meta convmeta.Meta) *dto.GeminiChatRequest {
				t.Helper()
				got, err := OpenAIChatRequestToGeminiGenerateContent(context.Background(), dto.GeneralOpenAIRequest{
					Model: "gemini-test",
					Messages: []dto.Message{
						{Role: "user", Content: "hello"},
					},
				}, meta)
				require.NoError(t, err)
				return got
			},
		},
		{
			name: "responses",
			convert: func(t *testing.T, meta convmeta.Meta) *dto.GeminiChatRequest {
				t.Helper()
				got, err := OpenAIResponsesRequestToGeminiChat(context.Background(), &dto.OpenAIResponsesRequest{
					Model: "gemini-test",
					Input: []byte(`"hello"`),
				}, meta)
				require.NoError(t, err)
				return got
			},
		},
	}

	for _, converter := range converters {
		t.Run(converter.name, func(t *testing.T) {
			t.Run("nil meta", func(t *testing.T) {
				got := converter.convert(t, nil)
				assert.Empty(t, got.SafetySettings)
				body, err := kitutil.Marshal(got)
				require.NoError(t, err)
				assert.NotContains(t, string(body), `"safetySettings"`)
			})

			t.Run("zero options", func(t *testing.T) {
				got := converter.convert(t, &convmeta.Values{})
				assert.Empty(t, got.SafetySettings)
				body, err := kitutil.Marshal(got)
				require.NoError(t, err)
				assert.NotContains(t, string(body), `"safetySettings"`)
			})

			t.Run("empty thresholds", func(t *testing.T) {
				got := converter.convert(t, &convmeta.Values{Options: &convmeta.Options{
					Gemini: convmeta.GeminiOptions{
						SafetySetting: func(category string) string {
							if category == "HARM_CATEGORY_HARASSMENT" {
								return "BLOCK_NONE"
							}
							return ""
						},
					},
				}})
				assert.Equal(t, []dto.GeminiChatSafetySettings{
					{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_NONE"},
				}, got.SafetySettings)
			})

			t.Run("nonempty thresholds", func(t *testing.T) {
				got := converter.convert(t, &convmeta.Values{Options: &convmeta.Options{
					Gemini: convmeta.GeminiOptions{
						SafetySetting: func(string) string { return "OFF" },
					},
				}})
				require.Len(t, got.SafetySettings, 4)
				for _, setting := range got.SafetySettings {
					assert.Equal(t, "OFF", setting.Threshold)
				}
			})
		})
	}
}
