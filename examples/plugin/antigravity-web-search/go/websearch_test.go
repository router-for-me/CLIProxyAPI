package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestShouldHandleOnlyClaudeTypedWebSearchFixture(t *testing.T) {
	cfg := defaultPluginConfig()
	body := readHTTPFixtureBody(t, "cc-f-3.json")
	req := pluginapi.ServerToolRequest{
		Provider:      "antigravity",
		SourceFormat:  "claude",
		UpstreamModel: "claude-sonnet-4-6",
		Payload:       body,
	}
	if !shouldHandleServerTool(req, cfg) {
		t.Fatal("cc-f-3 typed web_search request was not handled")
	}
	if query := extractUserQuery(body); query != "北京天气 2026年6月10日" {
		t.Fatalf("query = %q, want 北京天气 2026年6月10日", query)
	}
}

func TestShouldHandleClassicClaudeTypedWebSearchFixture(t *testing.T) {
	cfg := defaultPluginConfig()
	body := readHTTPFixtureBody(t, "cc-2.json")
	req := pluginapi.ServerToolRequest{
		Provider:      "antigravity",
		SourceFormat:  "claude",
		UpstreamModel: "claude-sonnet-4-6",
		Payload:       body,
	}
	if !shouldHandleServerTool(req, cfg) {
		t.Fatal("cc-2 typed web_search request was not handled")
	}
	if query := extractUserQuery(body); query != "北京天气 2026年1月30日" {
		t.Fatalf("query = %q, want 北京天气 2026年1月30日", query)
	}
}

func TestShouldNotHandleNativeGoogleSearchModel(t *testing.T) {
	cfg := defaultPluginConfig()
	body := readHTTPFixtureBody(t, "cc-f-3.json")
	req := pluginapi.ServerToolRequest{
		Provider:      "antigravity",
		SourceFormat:  "claude",
		UpstreamModel: defaultWebSearchBackendModel,
		Payload:       body,
	}
	if shouldHandleServerTool(req, cfg) {
		t.Fatal("native web_search backend model should use translator path, not plugin fallback")
	}
}

func TestShouldNotHandleConfiguredNativeGoogleSearchModel(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.BackendModel = "gemini-web-search-next"
	body := readHTTPFixtureBody(t, "cc-f-3.json")
	req := pluginapi.ServerToolRequest{
		Provider:      "antigravity",
		SourceFormat:  "claude",
		UpstreamModel: "gemini-web-search-next(high)",
		Payload:       body,
	}
	if shouldHandleServerTool(req, cfg) {
		t.Fatal("configured native web_search backend model should use translator path, not plugin fallback")
	}
}

func TestShouldNotHandleHostAdvertisedNativeGoogleSearchModel(t *testing.T) {
	cfg := defaultPluginConfig()
	body := readHTTPFixtureBody(t, "cc-f-3.json")
	req := pluginapi.ServerToolRequest{
		Provider:      "antigravity",
		SourceFormat:  "claude",
		UpstreamModel: "gemini-web-search-next",
		Metadata: map[string]any{
			metadataWebSearchModelIDs: []any{"gemini-web-search-next"},
		},
		Payload: body,
	}
	if shouldHandleServerTool(req, cfg) {
		t.Fatal("host-advertised native web_search model should use translator path, not plugin fallback")
	}
}

func TestWebSearchBackendModelPrefersHostMetadata(t *testing.T) {
	cfg := defaultPluginConfig()
	req := pluginapi.ServerToolRequest{
		Metadata: map[string]any{
			metadataWebSearchBackend: "gemini-web-search-next",
			metadataWebSearchModelIDs: []any{
				defaultWebSearchBackendModel,
			},
		},
	}
	if got := webSearchBackendModel(req, cfg); got != "gemini-web-search-next" {
		t.Fatalf("webSearchBackendModel() = %q, want gemini-web-search-next", got)
	}
}

func TestShouldNotHandleOuterToolSearchFixtures(t *testing.T) {
	cfg := defaultPluginConfig()
	for _, name := range []string{"cc-f-1.json", "cc-f-2.json", "cc-f-4.json"} {
		t.Run(name, func(t *testing.T) {
			body := readHTTPFixtureBody(t, name)
			req := pluginapi.ServerToolRequest{
				Provider:     "antigravity",
				SourceFormat: "claude",
				Payload:      body,
			}
			if shouldHandleServerTool(req, cfg) {
				t.Fatalf("%s was handled, want fallback for outer ToolSearch/WebSearch flow", name)
			}
		})
	}
}

func TestConvertGeminiToClaudeSSEStreamIncludesServerToolEvents(t *testing.T) {
	events := convertGeminiToClaudeSSEStream("gemini-3.5-flash", sampleGeminiGroundingResponse())
	joined := strings.Join(events, "")
	for _, needle := range []string{
		"event: message_start",
		`"type":"server_tool_use"`,
		`"type":"web_search_tool_result"`,
		`"web_search_requests":1`,
		"event: message_stop",
	} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("SSE output missing %s:\n%s", needle, joined)
		}
	}
}

func TestConvertGeminiToClaudeNonStreamIncludesSearchUsage(t *testing.T) {
	resp := convertGeminiToClaudeNonStream("gemini-3.5-flash", sampleGeminiGroundingResponse())
	for _, needle := range []string{
		`"type":"server_tool_use"`,
		`"type":"web_search_tool_result"`,
		`"web_search_requests":1`,
		`"url":"https://example.com/weather"`,
	} {
		if !strings.Contains(resp, needle) {
			t.Fatalf("non-stream output missing %s:\n%s", needle, resp)
		}
	}
}

func readHTTPFixtureBody(t *testing.T, name string) []byte {
	t.Helper()
	raw, errRead := os.ReadFile(filepath.Join(webSearchFixtureDir(t), name))
	if errRead != nil {
		t.Fatalf("read fixture %s: %v", name, errRead)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return decodeFirstJSONBody(t, name, trimmed)
	}
	headerEnd := bytes.Index(raw, []byte("\n\n"))
	if headerEnd < 0 {
		t.Fatalf("fixture %s missing HTTP header separator", name)
	}
	return decodeFirstJSONBody(t, name, raw[headerEnd+2:])
}

func decodeFirstJSONBody(t *testing.T, name string, raw []byte) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var body json.RawMessage
	if errDecode := decoder.Decode(&body); errDecode != nil {
		t.Fatalf("decode first JSON body from %s: %v", name, errDecode)
	}
	return body
}

func webSearchFixtureDir(t *testing.T) string {
	t.Helper()
	candidates := []string{
		os.Getenv("CLIPROXY_WEBSEARCH_FIXTURE_DIR"),
		filepath.Join("..", "..", "..", "..", "temp", "anti-websearch"),
		"/Users/sususu/workspace/CLIProxyAPI/temp/anti-websearch",
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if stat, errStat := os.Stat(filepath.Join(candidate, "cc-f-3.json")); errStat == nil && !stat.IsDir() {
			return candidate
		}
	}
	t.Fatal("websearch fixtures not found; set CLIPROXY_WEBSEARCH_FIXTURE_DIR")
	return ""
}

func sampleGeminiGroundingResponse() []byte {
	return []byte(`{
  "response": {
    "candidates": [
      {
        "content": {
          "parts": [
            {"text": "Beijing weather is clear today."}
          ]
        },
        "groundingMetadata": {
          "webSearchQueries": ["Beijing weather June 10 2026"],
          "groundingChunks": [
            {
              "web": {
                "uri": "https://example.com/weather",
                "title": "Beijing Weather"
              }
            }
          ],
          "groundingSupports": [
            {
              "segment": {
                "startIndex": 0,
                "endIndex": 31,
                "text": "Beijing weather is clear today."
              },
              "groundingChunkIndices": [0]
            }
          ]
        }
      }
    ],
    "usageMetadata": {
      "promptTokenCount": 12,
      "candidatesTokenCount": 7
    }
  }
}`)
}
