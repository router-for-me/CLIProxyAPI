package responses

import (
	"context"

<<<<<<< HEAD:pkg/llmproxy/translator/antigravity/openai/responses/antigravity_openai-responses_response.go
	geminiopenai "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/openai/responses"
=======
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
>>>>>>> upstream/main:internal/translator/antigravity/openai/responses/antigravity_openai-responses_response.go
	"github.com/tidwall/gjson"
)

func ConvertAntigravityResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	responseResult := gjson.GetBytes(rawJSON, "response")
	if responseResult.Exists() {
		rawJSON = []byte(responseResult.Raw)
	}
	out := geminiopenai.ConvertGeminiResponseToOpenAIResponses(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	res := make([][]byte, len(out))
	for i, s := range out {
		res[i] = []byte(s)
	}
	return res
}

func ConvertAntigravityResponseToOpenAIResponsesNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	responseResult := gjson.GetBytes(rawJSON, "response")
	if responseResult.Exists() {
		rawJSON = []byte(responseResult.Raw)
	}

	requestResult := gjson.GetBytes(originalRequestRawJSON, "request")
	if responseResult.Exists() {
		originalRequestRawJSON = []byte(requestResult.Raw)
	}

	requestResult = gjson.GetBytes(requestRawJSON, "request")
	if responseResult.Exists() {
		requestRawJSON = []byte(requestResult.Raw)
	}

	return geminiopenai.ConvertGeminiResponseToOpenAIResponsesNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}
