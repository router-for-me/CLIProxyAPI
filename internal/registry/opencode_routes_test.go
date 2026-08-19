package registry

import (
	"strings"
	"testing"
)

func TestResolveOpenCodeProtocol_ZenResponsesModels(t *testing.T) {
	cases := map[string]string{
		"gpt-5.6-luna":   "responses",
		"grok-4.5":       "responses",
		"muse-spark-1.2": "responses",
	}
	for model, want := range cases {
		got := ResolveOpenCodeProtocol("zen", model)
		if got != want {
			t.Errorf("ResolveOpenCodeProtocol(zen,%q) = %q, want %q", model, got, want)
		}
	}
}

func TestResolveOpenCodeProtocol_ZenMessagesAndChat(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5":   "messages",
		"minimax-m2.7":    "chat",
		"deepseek-v4-pro": "chat",
	}
	for model, want := range cases {
		got := ResolveOpenCodeProtocol("zen", model)
		if got != want {
			t.Errorf("ResolveOpenCodeProtocol(zen,%q) = %q, want %q", model, got, want)
		}
	}
}

func TestResolveOpenCodeProtocol_ZenGemini(t *testing.T) {
	got := ResolveOpenCodeProtocol("zen", "gemini-3.7-flash")
	if got != "gemini" {
		t.Fatalf("gemini path = %q, want gemini", got)
	}
	path := OpenCodeModelPath("zen", "gemini-3.7-flash")
	want := "/v1/models/gemini-3.7-flash"
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestResolveOpenCodeProtocol_GoPrefixRules(t *testing.T) {
	cases := map[string]string{
		"gpt-5.6-luna":    "responses",
		"grok-4.5":        "responses",
		"claude-opus-5":   "messages",
		"qwen3.7-max":     "messages",
		"minimax-m2.7":    "messages",
		"deepseek-v4-pro": "chat",
		"mimo-v2.5":       "chat",
		"hy3":             "chat",
	}
	for model, want := range cases {
		got := ResolveOpenCodeProtocol("go", model)
		if got != want {
			t.Errorf("ResolveOpenCodeProtocol(go,%q) = %q, want %q", model, got, want)
		}
	}
}

func TestResolveOpenCodeProtocol_GoGeminiUnknown(t *testing.T) {
	if got := ResolveOpenCodeProtocol("go", "gemini-3.7-flash"); got != "" {
		t.Fatalf("Go gemini should be unknown, got %q", got)
	}
}

func TestResolveOpenCodeProtocol_UnknownModelErrors(t *testing.T) {
	if got := ResolveOpenCodeProtocol("zen", "nonexistent-model-v9"); got != "" {
		t.Fatalf("unknown model should return empty, got %q", got)
	}
}

func TestResolveOpenCodeProtocol_FreeTierSuffix(t *testing.T) {
	if got := ResolveOpenCodeProtocol("zen", "deepseek-v4-flash-free"); got != "chat" {
		t.Fatalf("free model = %q, want chat", got)
	}
}

func TestResolveOpenCodeProtocol_CaseInsensitive(t *testing.T) {
	if got := ResolveOpenCodeProtocol("ZEN", "GPT-5.6-LUNA"); got != "responses" {
		t.Fatalf("case-insensitive = %q, want responses", got)
	}
}

func TestOpenCodeModelPath_AllRoutes(t *testing.T) {
	cases := map[string]string{
		"responses": "/v1/responses",
		"messages":  "/v1/messages",
		"chat":      "/v1/chat/completions",
	}
	for proto, wantPath := range cases {
		model := routePathToModel(proto)
		got := OpenCodeModelPath("zen", model)
		if got != wantPath {
			t.Errorf("path(%q) = %q, want %q", model, got, wantPath)
		}
	}
}

func routePathToModel(proto string) string {
	switch proto {
	case "responses":
		return "gpt-5.6-luna"
	case "messages":
		return "claude-opus-5"
	case "chat":
		return "deepseek-v4-pro"
	}
	return ""
}

func TestOpenCodeRoutes_JsonCoversDocs(t *testing.T) {
	docsZenRoutes := strings.Count(string(embeddedOpenCodeRoutesJSON), "zen")
	if docsZenRoutes == 0 {
		t.Fatal("embedded routes missing zen section")
	}
	for _, id := range []string{"gpt-5.6-luna", "grok-4.5", "claude-opus-5", "gemini-3.7-flash"} {
		if !OpenCodeModelKnown("zen", id) {
			t.Errorf("zen route for %q should be known (covered by docs)", id)
		}
	}
	for _, id := range []string{"gpt-5.6-luna", "grok-4.5", "deepseek-v4-pro"} {
		if !OpenCodeModelKnown("go", id) {
			t.Errorf("go route for %q should be known", id)
		}
	}
}
