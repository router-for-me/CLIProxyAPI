package handlers

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

func TestBuildErrorResponseBodySummarizesHTMLAndKeepsJSON(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", htmlErrorSummaryMaxRunes+80)
	htmlBody := "<!DOCTYPE html><html><head><title>502 Bad Gateway</title><script>alert(1)</script><style>body{color:red}</style></head><body><h1>502 Bad Gateway</h1><p>" + long + "</p></body></html>"
	got := BuildErrorResponseBody(502, htmlBody)
	message := gjson.GetBytes(got, "error.message").String()
	if message == "" {
		t.Fatalf("error.message empty, body=%s", got)
	}
	if strings.Contains(message, "<") || strings.Contains(message, ">") {
		t.Fatalf("error.message still has tags: %q", message)
	}
	if strings.Contains(message, "alert") || strings.Contains(message, "color:red") {
		t.Fatalf("error.message kept non-visible markup: %q", message)
	}
	if !strings.Contains(message, "502 Bad Gateway") {
		t.Fatalf("error.message = %q, want visible heading", message)
	}
	if utf8.RuneCountInString(message) > htmlErrorSummaryMaxRunes {
		t.Fatalf("error.message length = %d, want <= %d", utf8.RuneCountInString(message), htmlErrorSummaryMaxRunes)
	}
	if strings.Contains(string(got), "<html") || strings.Contains(string(got), "alert(1)") {
		t.Fatalf("client body leaked HTML: %s", got)
	}

	jsonBody := `{"error":{"message":"Invalid value: '<div class=\"x\">'","type":"invalid_request_error"}}`
	if gotJSON := string(BuildErrorResponseBody(400, jsonBody)); gotJSON != jsonBody {
		t.Fatalf("JSON body = %s, want intact %s", gotJSON, jsonBody)
	}

	const plain = "provider unavailable"
	if gotPlain := gjson.GetBytes(BuildErrorResponseBody(502, plain), "error.message").String(); gotPlain != plain {
		t.Fatalf("plain message = %q, want %q", gotPlain, plain)
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkSummarizesHTML(t *testing.T) {
	t.Parallel()

	htmlBody := `<html><head><title>Just a moment...</title></head><body><h1>Checking your browser</h1><script>challenge()</script></body></html>`
	chunk := BuildOpenAIResponsesStreamErrorChunk(502, htmlBody, 3)
	message := gjson.GetBytes(chunk, "error.message").String()
	if strings.Contains(message, "<") || strings.Contains(message, "challenge()") {
		t.Fatalf("stream error.message = %q, want plain summary", message)
	}
	if !strings.Contains(message, "Just a moment...") || !strings.Contains(message, "Checking your browser") {
		t.Fatalf("stream error.message = %q, want visible text", message)
	}

	jsonBody := `{"error":{"type":"invalid_request_error","message":"keep <div> intact","code":"invalid_request_error"}}`
	jsonChunk := BuildOpenAIResponsesStreamErrorChunk(400, jsonBody, 1)
	if got := gjson.GetBytes(jsonChunk, "error.message").String(); got != "keep <div> intact" {
		t.Fatalf("JSON stream message = %q, want original JSON message", got)
	}
}

func TestSummarizeHTMLClientMessageEmptyPage(t *testing.T) {
	t.Parallel()

	got, ok := SummarizeHTMLClientMessage(`<html><body><img src="x"></body></html>`)
	if !ok || got != "upstream HTML error" {
		t.Fatalf("summary = %q ok=%v, want upstream HTML error", got, ok)
	}
	if _, okJSON := SummarizeHTMLClientMessage(`{"error":{"message":"<html>no</html>"}}`); okJSON {
		t.Fatal("JSON body was treated as HTML")
	}
}
