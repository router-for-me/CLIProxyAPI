package executor

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestIssue7967PreservesAntigravityRequestID(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		request   string
		requestID string
	}{
		{
			name:      "agent",
			model:     "gemini-3-flash",
			request:   `{"requestType":"agent","requestId":"normalized-agent","request":{"labels":{"cascade_id":"normalized-cascade","root_cascade_id":"normalized-cascade","trajectory_id":"normalized-trajectory","request_id":"normalized-request","last_step_index":"7"},"contents":[]}}`,
			requestID: "normalized-agent",
		},
		{
			name:      "image",
			model:     "gemini-3-pro-image-preview",
			request:   `{"requestType":"image_gen","requestId":"normalized-image","request":{"contents":[]}}`,
			requestID: "normalized-image",
		},
		{
			name:      "other request type",
			model:     "gemini-3-flash",
			request:   `{"requestType":"custom","requestId":"normalized-custom","request":{"contents":[]}}`,
			requestID: "normalized-custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := ojson.NewDocString(tt.request)
			geminiToAntigravityDocForAuth(doc, cliproxyauth.AntigravityModelSelection{ID: tt.model}, "project", nil)
			if got := doc.Get("requestId").String(); got != tt.requestID {
				t.Fatalf("requestId = %q, want %q", got, tt.requestID)
			}
			if tt.name == "agent" {
				for key, want := range map[string]string{
					"cascade_id":      "normalized-cascade",
					"root_cascade_id": "normalized-cascade",
					"trajectory_id":   "normalized-trajectory",
					"request_id":      "normalized-request",
					"last_step_index": "7",
				} {
					if got := doc.Get("request.labels." + key).String(); got != want {
						t.Errorf("request.labels.%s = %q, want %q", key, got, want)
					}
				}
			}
		})
	}
}

func TestIssue7967NormalizesCountTokensStructuredOutputInstruction(t *testing.T) {
	model := "claude-opus-5-5-high"
	body := captureAntigravityCountTokensBody(t, &config.Config{}, t.Name(), cliproxyexecutor.Request{
		Model: model,
		Payload: []byte(`{
            "messages":[{"role":"system","content":"base rule"},{"role":"user","content":"hello"}],
            "response_format":{"type":"json_schema","json_schema":{"name":"result","schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}}
        }`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	instruction := ojson.GetBytesNoCopy(body, "request.systemInstruction")
	if instruction.Get("role").String() != "user" {
		t.Fatalf("systemInstruction.role = %q, want user: %s", instruction.Get("role").String(), body)
	}
	parts := instruction.Get("parts").Array()
	if len(parts) != 1 || !strings.Contains(parts[0].Get("text").String(), "base rule") || !strings.Contains(parts[0].Get("text").String(), `"ok"`) {
		t.Fatalf("systemInstruction.parts not normalized: %s", instruction.Raw)
	}
}

func TestIssue7967GeneratesSlashDelimitedFallbackRequestID(t *testing.T) {
	doc := ojson.NewDocString(`{"requestType":"custom","request":{"contents":[]}}`)
	geminiToAntigravityDocForAuth(doc, cliproxyauth.AntigravityModelSelection{ID: "gemini-3-flash"}, "project", nil)

	requestID := doc.Get("requestId").String()
	if !strings.HasPrefix(requestID, "custom/") || strings.Contains(requestID, "agent-") {
		t.Fatalf("requestId = %q, want slash-delimited custom/<uuid> fallback", requestID)
	}
}
