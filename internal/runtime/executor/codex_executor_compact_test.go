package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// A CPA-sealed capsule must never reach the upstream: it cannot decrypt our ciphertext and
// rejects the whole request. A capsule this instance can open is inlined as plain context, an
// unreadable one is dropped, and an item that is not CPA-sealed may belong to the upstream so it
// must be forwarded untouched.
func TestCodexExecutorNormalizesCPACompactionCapsulesBeforeUpstream(t *testing.T) {
	antigravityCapsule, errSeal := helps.SealAntigravityCompaction("antigravity summary", "gpt-5.4")
	if errSeal != nil {
		t.Fatalf("seal antigravity capsule: %v", errSeal)
	}
	globalCapsule, errSealGlobal := helps.SealAntigravityCompaction("global summary", "gpt-5.4")
	if errSealGlobal != nil {
		t.Fatalf("seal global capsule: %v", errSealGlobal)
	}

	tests := []struct {
		name            string
		capsule         string
		wantSummaryText string
	}{
		{name: "cpa capsule inlined", capsule: antigravityCapsule, wantSummaryText: "antigravity summary"},
		{name: "cpa capsule from another lane inlined", capsule: globalCapsule, wantSummaryText: "global summary"},
		// Capsules minted by an older fork build can no longer be opened, but they are still CPA
		// ciphertext and must be dropped instead of being forwarded.
		{name: "legacy per-lane capsule dropped", capsule: "cpa-compat-compact-v2:AAAAAAAAAAAAAAAA:AAAA"},
		{name: "legacy derived-key capsule dropped", capsule: "cpa-compat-compact-v3:AAAA"},
		{name: "native opaque item preserved", capsule: "upstream-native-opaque", wantSummaryText: "upstream-native-opaque"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
			}))
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			}}
			payload := []byte(`{"model":"gpt-5.4","input":[{"type":"compaction","encrypted_content":"` + tc.capsule + `"},{"type":"message","role":"user","content":"next"}]}`)

			if _, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: payload,
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}); errExecute != nil {
				t.Fatalf("Execute error: %v", errExecute)
			}

			body := string(gotBody)
			if tc.name == "native opaque item preserved" {
				if !strings.Contains(body, tc.capsule) {
					t.Fatalf("native opaque compaction item was dropped: %s", body)
				}
				return
			}
			if strings.Contains(body, tc.capsule) {
				t.Fatalf("CPA ciphertext reached upstream: %s", body)
			}
			if tc.wantSummaryText != "" && !strings.Contains(body, tc.wantSummaryText) {
				t.Fatalf("summary was not inlined for the upstream: %s", body)
			}
		})
	}
}

func TestCodexExecutorCompactAddsDefaultInstructionsWithoutInjectingImageTool(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "missing instructions",
			payload: `{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`,
		},
		{
			name:    "null instructions",
			payload: `{"model":"gpt-5.4","instructions":null,"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				body, _ := io.ReadAll(r.Body)
				gotBody = body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
			}))
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			}}

			resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Alt:          "responses/compact",
				Stream:       false,
			})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if gotPath != "/responses/compact" {
				t.Fatalf("path = %q, want %q", gotPath, "/responses/compact")
			}
			if instructions := gjson.GetBytes(gotBody, "instructions"); instructions.Type != gjson.String || instructions.String() != "" {
				t.Fatalf("instructions = %s, want empty string; body=%s", instructions.Raw, gotBody)
			}
			if gjson.GetBytes(gotBody, "tools").Exists() {
				t.Fatalf("compact request injected image_generation tool: %s", gotBody)
			}
			input := gjson.GetBytes(gotBody, "input").Array()
			if len(input) != 2 || input[1].Get("type").String() != "compaction_trigger" {
				t.Fatalf("compact input order changed: %s", gotBody)
			}
			if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
				t.Fatalf("payload = %s", string(resp.Payload))
			}
		})
	}
}
