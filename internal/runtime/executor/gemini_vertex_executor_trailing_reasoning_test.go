package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	issue6630UserItem      = `{"type":"message","role":"user","content":[{"type":"input_text","text":"say hi"}]}`
	issue6630ReasoningItem = `{"type":"reasoning","id":"rs_test_1","summary":[{"type":"summary_text","text":"I should say hi briefly."}]}`
)

// issue6630VertexContents sends a Responses request through the selected Vertex
// executor path and returns the raw contents array received upstream.
func issue6630VertexContents(t *testing.T, cfg *config.Config, authMode string, stream bool, input string) string {
	t.Helper()
	const okBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
		}
		captured <- body
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+okBody+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okBody)
	}))
	defer server.Close()

	ctx := context.Background()
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "vertex"}
	if authMode == "api_key" {
		auth.Attributes = map[string]string{"api_key": "test-key", "base_url": server.URL}
	} else {
		var serviceAccount map[string]any
		if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		auth.Metadata = map[string]any{"project_id": "proxy-test", "location": "global", "service_account": serviceAccount}
		target, errParse := parseTestURL(t, server.URL)
		if errParse != nil {
			t.Fatal(errParse)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		t.Cleanup(transport.CloseIdleConnections)
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
	}

	payload := []byte(fmt.Sprintf(`{"model":"gemini-4-argon","input":[%s],"stream":%t,"store":false}`, input, stream))
	req := cliproxyexecutor.Request{Model: "gemini-4-argon", Payload: payload}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
		Stream:          stream,
	}
	exec := NewGeminiVertexExecutor(cfg)
	if stream {
		result, errStream := exec.ExecuteStream(ctx, auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
	} else if _, errExecute := exec.Execute(ctx, auth, req, opts); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	return gjson.GetBytes(<-captured, "contents").Raw
}

// issue6630AssertContents compares upstream contents with a hand-written JSON
// literal, ignoring object key order.
func issue6630AssertContents(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if errGot := json.Unmarshal([]byte(got), &gotValue); errGot != nil {
		t.Fatalf("decode upstream contents %q: %v", got, errGot)
	}
	if errWant := json.Unmarshal([]byte(want), &wantValue); errWant != nil {
		t.Fatalf("decode expected contents %q: %v", want, errWant)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("upstream contents = %s, want %s", got, want)
	}
}

// TestGeminiVertexDropsTrailingReasoningTurn covers #6630: Vertex rejects a
// trailing thought-only model turn even when an empty user turn follows it.
func TestGeminiVertexDropsTrailingReasoningTurn(t *testing.T) {
	const signature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "trailing reasoning",
			input: issue6630UserItem + `,` + issue6630ReasoningItem,
			want:  `[{"role":"user","parts":[{"text":"say hi"}]}]`,
		},
		{
			name:  "trailing signed reasoning",
			input: issue6630UserItem + `,{"type":"reasoning","id":"rs_test_1","encrypted_content":"` + signature + `","summary":[{"type":"summary_text","text":"I should say hi briefly."}]}`,
			want:  `[{"role":"user","parts":[{"text":"say hi"}]}]`,
		},
		{
			name:  "trailing reasoning after tool output",
			input: issue6630UserItem + `,{"type":"function_call","call_id":"call_1","name":"run","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"done"},` + issue6630ReasoningItem,
			want: `[{"role":"user","parts":[{"text":"say hi"}]},` +
				`{"role":"model","parts":[{"functionCall":{"name":"run","args":{}},"thoughtSignature":"skip_thought_signature_validator"}]},` +
				`{"role":"user","parts":[{"functionResponse":{"name":"run","response":{"result":"done"}}}]}]`,
		},
		{
			name:  "reasoning before user turn is kept",
			input: issue6630UserItem + `,` + issue6630ReasoningItem + `,{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}`,
			want: `[{"role":"user","parts":[{"text":"say hi"}]},` +
				`{"role":"model","parts":[{"text":"I should say hi briefly.","thought":true}]},` +
				`{"role":"user","parts":[{"text":"again"}]}]`,
		},
	}
	for _, tt := range tests {
		for _, authMode := range []string{"api_key", "service_account"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", tt.name, authMode, stream), func(t *testing.T) {
					issue6630AssertContents(t, issue6630VertexContents(t, &config.Config{}, authMode, stream, tt.input), tt.want)
				})
			}
		}
	}
}

// TestGeminiVertexTrailingReasoningDropPrecedesPayloadRules keeps payload rules
// as the final barrier: configured contents are sent exactly as configured.
func TestGeminiVertexTrailingReasoningDropPrecedesPayloadRules(t *testing.T) {
	const configured = `[{"role":"user","parts":[{"text":"configured"}]},{"role":"model","parts":[{"text":"configured thought","thought":true}]}]`
	cfg := &config.Config{Payload: config.PayloadConfig{OverrideRaw: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "gemini-4-argon"}},
		Params: map[string]any{"contents": configured},
	}}}}
	for _, authMode := range []string{"api_key", "service_account"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", authMode, stream), func(t *testing.T) {
				issue6630AssertContents(t, issue6630VertexContents(t, cfg, authMode, stream, issue6630UserItem+`,`+issue6630ReasoningItem), configured)
			})
		}
	}
}
