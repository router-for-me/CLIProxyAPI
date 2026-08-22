package executor

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestShouldResolveAntigravityWebSearchGroundingURLs_OpenAIResponses(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search"}]}`)
	translated := []byte(`{"request":{"tools":[{"googleSearch":{}}]}}`)

	if !shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatOpenAIResponse, original, translated) {
		t.Fatal("OpenAI Responses web search should resolve Antigravity grounding URLs")
	}
	if shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatOpenAIResponse, []byte(`{"tools":[{"type":"function"}]}`), translated) {
		t.Fatal("non-web-search Responses request should not resolve grounding URLs")
	}
	if shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatOpenAIResponse, original, []byte(`{"request":{"tools":[]}}`)) {
		t.Fatal("request without translated googleSearch should not resolve grounding URLs")
	}
}
