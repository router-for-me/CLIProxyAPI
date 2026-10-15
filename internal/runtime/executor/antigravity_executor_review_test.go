package executor

import (
	"context"
	"io"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestAntigravityBuildRequestPreservesHiddenThinking(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, field := range []string{"includeThoughts", "include_thoughts"} {
			payload := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","` + field + `":false}}}}`)
			auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "test-project"}}
			req, err := (&AntigravityExecutor{}).buildRequest(t.Context(), auth, "test-token", "gemini-3.8-flash", payload, stream, "", "https://example.invalid")
			if err != nil {
				t.Fatal(err)
			}
			body, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if got := gjson.GetBytes(body, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
				t.Fatalf("stream=%v field=%s: thinkingLevel=%q, want high", stream, field, got)
			}
			if got := gjson.GetBytes(body, "request.generationConfig.thinkingConfig.includeThoughts"); got.Type != gjson.False {
				t.Fatalf("stream=%v field=%s: includeThoughts=%s, want false", stream, field, got.Raw)
			}
			if gjson.GetBytes(body, "request.generationConfig.thinkingConfig.thinkingBudget").Exists() {
				t.Fatal("hidden reasoning must not insert a zero budget")
			}
		}
	}
}

func TestAntigravityBuildRequestClampsNumericBudget(t *testing.T) {
	for _, model := range []string{"gemini-3.8-flash", "gemini-4-argon"} {
		t.Run(model, func(t *testing.T) {
			for _, tc := range []struct {
				budget int
				level  string
			}{{1, "low"}, {512, "low"}, {8192, "medium"}, {32768, "high"}} {
				payload, err := sjson.SetBytes([]byte(`{"request":{}}`), "request.generationConfig.thinkingConfig.thinkingBudget", tc.budget)
				if err != nil {
					t.Fatal(err)
				}
				body := buildRequestBodyFromRawPayload(t, model, payload)
				encoded, errMarshal := sjson.SetBytes([]byte(`{}`), "body", body)
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				if got := gjson.GetBytes(encoded, "body.request.generationConfig.thinkingConfig.thinkingLevel").String(); got != tc.level {
					t.Errorf("budget=%d: level=%q, want %q", tc.budget, got, tc.level)
				}
			}
		})
	}
}

func TestAntigravityBuildRequestReviewPayloadRulesRemainFinal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tools := range []string{"", `,"tools":[{"functionDeclarations":[{"name":"test","parameters":{"type":"object"}}]}]`} {
			calls := 0
			ctx := helps.WithPayloadFinalizer(context.Background(), func(body []byte) []byte {
				calls++
				body, _ = sjson.SetBytes(body, "model", "user-configured-model")
				body, _ = sjson.SetBytes(body, "request.generationConfig.thinkingConfig.includeThoughts", false)
				body, _ = sjson.DeleteBytes(body, "request.generationConfig.thinkingConfig.thinkingLevel")
				return body
			})
			auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "test-project"}}
			payload := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}` + tools + `}}`)
			req, err := (&AntigravityExecutor{}).buildRequest(ctx, auth, "test-token", "gemini-3.8-flash", payload, stream, "", "https://example.invalid")
			if err != nil {
				t.Fatal(err)
			}
			body, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if calls != 1 || gjson.GetBytes(body, "model").String() != "user-configured-model" || gjson.GetBytes(body, "request.generationConfig.thinkingConfig.includeThoughts").Type != gjson.False || gjson.GetBytes(body, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
				t.Fatalf("final payload rules were overwritten or applied %d times: %s", calls, body)
			}
		}
	}
}
