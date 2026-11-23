package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func newIssue7865TestAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token":  "token-" + id,
			"refresh_token": "refresh-" + id,
			"project_id":    "project-1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func captureAntigravityCountTokensBody(t *testing.T, cfg *config.Config, authID string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	t.Helper()

	var capturedBody []byte
	var capturedPath string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		capturedPath = r.URL.Path
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			return nil, errRead
		}
		capturedBody = append([]byte(nil), body...)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"totalTokens":42}`)),
		}, nil
	}))

	exec := NewAntigravityExecutor(cfg)
	if _, errCount := exec.CountTokens(ctx, newIssue7865TestAuth(authID), req, opts); errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if capturedPath != antigravityCountTokensPath {
		t.Fatalf("request path = %q, want %q", capturedPath, antigravityCountTokensPath)
	}
	if len(capturedBody) == 0 {
		t.Fatal("countTokens upstream body was not captured")
	}
	return capturedBody
}

func TestAntigravityIssue7865_CountTokensPopulatesRequestModelAcrossFormats(t *testing.T) {
	tests := []struct {
		name      string
		format    sdktranslator.Format
		model     string
		payload   string
		wantModel string
	}{
		{
			name:      "gemini format",
			format:    sdktranslator.FormatGemini,
			model:     "gemini-3.6-flash-high",
			payload:   `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"stream":true}`,
			wantModel: "models/gemini-3.6-flash-high",
		},
		{
			name:      "gemini format strips thinking suffix from request.model",
			format:    sdktranslator.FormatGemini,
			model:     "gemini-3-flash(high)",
			payload:   `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			wantModel: "models/gemini-3-flash",
		},
		{
			name:      "openai chat format",
			format:    sdktranslator.FormatOpenAI,
			model:     "gemini-3-flash",
			payload:   `{"model":"gemini-3-flash","messages":[{"role":"user","content":"hello"}],"stream":true}`,
			wantModel: "models/gemini-3-flash",
		},
		{
			name:      "claude format",
			format:    sdktranslator.FormatClaude,
			model:     "claude-sonnet-4-6",
			payload:   `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"stream":true}`,
			wantModel: "models/claude-sonnet-4-6",
		},
		{
			name:      "openai responses format",
			format:    sdktranslator.FormatOpenAIResponse,
			model:     "gemini-3.8-flash-high",
			payload:   `{"model":"gemini-3.8-flash-high","input":"hello","stream":true}`,
			wantModel: "models/gemini-3.8-flash-high",
		},
		{
			name:      "interactions format strips injected request.stream",
			format:    sdktranslator.FormatInteractions,
			model:     "gemini-3.8-flash-high",
			payload:   `{"model":"gemini-3.8-flash-high","input":"hello","stream":true}`,
			wantModel: "models/gemini-3.8-flash-high",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capturedBody := captureAntigravityCountTokensBody(
				t,
				&config.Config{},
				"issue7865-"+tc.format.String(),
				cliproxyexecutor.Request{
					Model:   tc.model,
					Payload: []byte(tc.payload),
				},
				cliproxyexecutor.Options{
					SourceFormat:   tc.format,
					ResponseFormat: tc.format,
				},
			)

			if ojson.GetBytesNoCopy(capturedBody, "model").Exists() {
				t.Fatalf("top-level model must be deleted, got: %s", string(capturedBody))
			}
			if ojson.GetBytesNoCopy(capturedBody, "project").Exists() {
				t.Fatalf("top-level project must be deleted, got: %s", string(capturedBody))
			}
			if ojson.GetBytesNoCopy(capturedBody, "request.stream").Exists() {
				t.Fatalf("request.stream must be deleted, got: %s", string(capturedBody))
			}
			if got := ojson.GetBytesNoCopy(capturedBody, "request.model").String(); got != tc.wantModel {
				t.Fatalf("request.model = %q, want %q; body=%s", got, tc.wantModel, string(capturedBody))
			}
		})
	}
}

func TestAntigravityIssue7865_CountTokensPreservesExistingAndNormalizesModel(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		payload   string
		wantModel string
	}{
		{
			name:      "preserves caller-supplied request.model",
			model:     "gemini-3-flash",
			payload:   `{"model":"gemini-3-flash","request":{"model":"models/custom-tokenizer","stream":true,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			wantModel: "models/custom-tokenizer",
		},
		{
			name:      "replaces blank request.model from baseModel",
			model:     "gemini-3-flash",
			payload:   `{"model":"gemini-3-flash","request":{"model":"   ","stream":true,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			wantModel: "models/gemini-3-flash",
		},
		{
			name:      "replaces null request.model from baseModel",
			model:     "gemini-3-flash",
			payload:   `{"model":"gemini-3-flash","request":{"model":null,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			wantModel: "models/gemini-3-flash",
		},
		{
			name:      "does not double-prefix models/",
			model:     "models/gemini-3-flash",
			payload:   `{"model":"models/gemini-3-flash","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			wantModel: "models/gemini-3-flash",
		},
		{
			name:      "preserves full resource path model",
			model:     "projects/p/locations/us-central1/publishers/google/models/gemini-3-flash",
			payload:   `{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			wantModel: "projects/p/locations/us-central1/publishers/google/models/gemini-3-flash",
		},
		{
			name:      "falls back to top-level model when req.Model is empty",
			model:     "",
			payload:   `{"model":"gemini-3-flash(high)","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			wantModel: "models/gemini-3-flash",
		},
		{
			name:      "omits request.model when both req.Model and top-level model are empty",
			model:     "",
			payload:   `{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			wantModel: "",
		},
	}

	antigravityFormat := sdktranslator.FromString("antigravity")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capturedBody := captureAntigravityCountTokensBody(
				t,
				&config.Config{},
				"issue7865-passthrough",
				cliproxyexecutor.Request{
					Model:   tc.model,
					Payload: []byte(tc.payload),
				},
				cliproxyexecutor.Options{
					SourceFormat:   antigravityFormat,
					ResponseFormat: sdktranslator.FormatGemini,
				},
			)

			if ojson.GetBytesNoCopy(capturedBody, "model").Exists() {
				t.Fatalf("top-level model must be deleted, got: %s", string(capturedBody))
			}
			if ojson.GetBytesNoCopy(capturedBody, "request.stream").Exists() {
				t.Fatalf("request.stream must be deleted, got: %s", string(capturedBody))
			}
			modelRes := ojson.GetBytesNoCopy(capturedBody, "request.model")
			if tc.wantModel == "" {
				if modelRes.Exists() {
					t.Fatalf("request.model should be absent, got %q; body=%s", modelRes.Raw, string(capturedBody))
				}
			} else if got := modelRes.String(); got != tc.wantModel {
				t.Fatalf("request.model = %q, want %q; body=%s", got, tc.wantModel, string(capturedBody))
			}
		})
	}
}

func TestAntigravityIssue7865_CountTokensPayloadConfigFinalBarrier(t *testing.T) {
	t.Run("override wins over built-in request.model and request.stream cleanup", func(t *testing.T) {
		cfg := &config.Config{
			Payload: config.PayloadConfig{
				Override: []config.PayloadRule{
					{
						Models: []config.PayloadModelRule{{Name: "gemini-3-flash", Protocol: "antigravity"}},
						Params: map[string]any{
							"model":  "models/override-tokenizer",
							"stream": true,
						},
					},
				},
			},
		}
		capturedBody := captureAntigravityCountTokensBody(
			t,
			cfg,
			"issue7865-barrier-override",
			cliproxyexecutor.Request{
				Model:   "gemini-3-flash",
				Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"stream":false}`),
			},
			cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatGemini,
				ResponseFormat: sdktranslator.FormatGemini,
			},
		)

		if ojson.GetBytesNoCopy(capturedBody, "model").Exists() {
			t.Fatalf("top-level model must remain deleted, got: %s", string(capturedBody))
		}
		if got := ojson.GetBytesNoCopy(capturedBody, "request.model").String(); got != "models/override-tokenizer" {
			t.Fatalf("request.model = %q, want %q; body=%s", got, "models/override-tokenizer", string(capturedBody))
		}
		if streamRes := ojson.GetBytesNoCopy(capturedBody, "request.stream"); !streamRes.Exists() || !streamRes.Bool() {
			t.Fatalf("expected Payload.Override request.stream=true to survive final barrier, got: %s", string(capturedBody))
		}
	})

	t.Run("filter removes built-in request.model", func(t *testing.T) {
		cfg := &config.Config{
			Payload: config.PayloadConfig{
				Filter: []config.PayloadFilterRule{
					{
						Models: []config.PayloadModelRule{{Name: "gemini-3-flash", Protocol: "antigravity"}},
						Params: []string{"model"},
					},
				},
			},
		}
		capturedBody := captureAntigravityCountTokensBody(
			t,
			cfg,
			"issue7865-barrier-filter",
			cliproxyexecutor.Request{
				Model:   "gemini-3-flash",
				Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
			},
			cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatGemini,
				ResponseFormat: sdktranslator.FormatGemini,
			},
		)

		if ojson.GetBytesNoCopy(capturedBody, "request.model").Exists() {
			t.Fatalf("expected Payload.Filter to remove request.model, got: %s", string(capturedBody))
		}
	})
}
