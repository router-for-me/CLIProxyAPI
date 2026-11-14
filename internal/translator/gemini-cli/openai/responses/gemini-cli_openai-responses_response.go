package responses

import (
	"context"

	"github.com/MoeForge/moego/ojson"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
)

func ConvertGeminiCLIResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	responseResult := ojson.GetBytes(rawJSON, "response")
	if responseResult.Exists() {
		rawJSON = []byte(responseResult.Raw)
	}
	return ConvertGeminiResponseToOpenAIResponses(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

func ConvertGeminiCLIResponseToOpenAIResponsesNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	responseResult := ojson.GetBytes(rawJSON, "response")
	if responseResult.Exists() {
		rawJSON = []byte(responseResult.Raw)
	}

	requestResult := ojson.GetBytes(originalRequestRawJSON, "request")
	if responseResult.Exists() {
		originalRequestRawJSON = []byte(requestResult.Raw)
	}

	requestResult = ojson.GetBytes(requestRawJSON, "request")
	if responseResult.Exists() {
		requestRawJSON = []byte(requestResult.Raw)
	}

	return ConvertGeminiResponseToOpenAIResponsesNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}
