package main

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator/builtin"
)

func main() {
	rawRequest := []byte(`{"messages":[{"content":[{"text":"Hello! Gemini","type":"text"}],"role":"user"}],"model":"gemini-2.5-pro","stream":false}`)
	fmt.Println("Has gemini->openai response translator:", translator.HasResponseTransformerByFormatName(
		translator.FormatGemini,
		translator.FormatOpenAI,
	))

	translatedRequest := translator.TranslateRequestByFormatName(
		translator.FormatOpenAI,
		translator.FormatGemini,
		"gemini-2.5-pro",
		rawRequest,
		false,
	)

	fmt.Printf("Translated request to Gemini format:\n%s\n\n", translatedRequest)

	claudeResponse := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"Okay, here's what's going through my mind. I need to schedule a meeting"},{"thoughtSignature":"","functionCall":{"name":"schedule_meeting","args":{"topic":"Q3 planning","attendees":["Bob","Alice"],"time":"10:00","date":"2025-03-27"}}}]},"finishReason":"STOP","avgLogprobs":-0.50018133435930523}],"usageMetadata":{"promptTokenCount":117,"candidatesTokenCount":28,"totalTokenCount":474,"trafficType":"PROVISIONED_THROUGHPUT","promptTokensDetails":[{"modality":"TEXT","tokenCount":117}],"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":28}],"thoughtsTokenCount":329},"modelVersion":"gemini-2.5-pro","createTime":"2025-08-15T04:12:55.249090Z","responseId":"x7OeaIKaD6CU48APvNXDyA4"}`)

	convertedResponse := translator.TranslateNonStreamByFormatName(
		context.Background(),
		translator.FormatGemini,
		translator.FormatOpenAI,
		"gemini-2.5-pro",
		rawRequest,
		translatedRequest,
		claudeResponse,
		nil,
	)

	fmt.Printf("Converted response for OpenAI clients:\n%s\n", convertedResponse)
}
