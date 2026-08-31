package codebuddy

import (
	"net/http"
	"regexp"
	"testing"
)

func TestGenerateRequestUUIDFormat(t *testing.T) {
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for i := 0; i < 32; i++ {
		value := GenerateRequestUUID()
		if !uuidPattern.MatchString(value) {
			t.Fatalf("unexpected UUID format: %q", value)
		}
	}
	if GenerateRequestUUID() == GenerateRequestUUID() {
		t.Fatal("expected unique UUIDs")
	}
}

func TestGenerateHexIDLength(t *testing.T) {
	if got := GenerateHexID(16); len(got) != 32 {
		t.Fatalf("GenerateHexID(16) length = %d, want 32", len(got))
	}
	if got := GenerateHexID(8); len(got) != 16 {
		t.Fatalf("GenerateHexID(8) length = %d, want 16", len(got))
	}
}

func TestApplyChatHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://copilot.tencent.com/v2/chat/completions", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	ApplyChatHeaders(req, " 12345 ")

	expected := map[string]string{
		"X-Agent-Intent":          "craft",
		"X-Agent-Purpose":         "conversation_topic",
		"X-IDE-Type":              "WorkBuddy",
		"X-IDE-Name":              "WorkBuddy",
		"X-IDE-Version":           "5.2.5",
		"X-Private-Data":          "false",
		"X-Domain":                DefaultDomain,
		"X-Product":               "SaaS",
		"X-Requested-With":        "XMLHttpRequest",
		"User-Agent":              ClientUserAgent,
		"X-User-Id":               "12345",
		"X-B3-Sampled":            "1",
		"x-stainless-arch":        "arm64",
		"x-stainless-lang":        "js",
		"x-stainless-os":          "MacOS",
		"x-stainless-retry-count": "0",
	}
	for key, want := range expected {
		if got := req.Header.Get(key); got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}

	// Random headers must be present and non-empty per request.
	randomHeaders := []string{
		"X-Conversation-ID",
		"X-Conversation-Request-ID",
		"X-Conversation-Message-ID",
		"X-Request-ID",
		"traceparent",
		"b3",
		"X-B3-TraceId",
		"X-B3-SpanId",
		"X-Trace-ID",
		"x-stainless-package-version",
		"x-stainless-runtime",
		"x-stainless-runtime-version",
	}
	for _, key := range randomHeaders {
		if req.Header.Get(key) == "" {
			t.Errorf("header %s must be set", key)
		}
	}

	traceID := req.Header.Get("X-B3-TraceId")
	spanID := req.Header.Get("X-B3-SpanId")
	if req.Header.Get("traceparent") != "00-"+traceID+"-"+spanID+"-01" {
		t.Errorf("traceparent does not match trace/span ids")
	}
}

func TestBuildChatCompletionsURL(t *testing.T) {
	if got := BuildChatCompletionsURL(""); got != DefaultBaseURL+ChatCompletionsPath {
		t.Errorf("default base URL mismatch: %q", got)
	}
	if got := BuildChatCompletionsURL("https://example.com/"); got != "https://example.com"+ChatCompletionsPath {
		t.Errorf("override base URL mismatch: %q", got)
	}
}
