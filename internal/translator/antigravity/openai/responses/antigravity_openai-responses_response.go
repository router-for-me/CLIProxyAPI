package responses

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/openai/responses"
	"github.com/tidwall/gjson"
)

func ConvertAntigravityResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	responseResult := gjson.GetBytes(rawJSON, "response")
	if responseResult.Exists() {
		rawJSON = []byte(responseResult.Raw)
	}
	return ConvertGeminiResponseToOpenAIResponses(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

func ConvertAntigravityResponseToOpenAIResponsesNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string {
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

	return ConvertGeminiResponseToOpenAIResponsesNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}
