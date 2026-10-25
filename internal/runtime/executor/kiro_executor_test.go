package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func kiroExecutorFrame(event, payload string) []byte {
	var headers []byte
	for _, pair := range [][2]string{{":message-type", "event"}, {":event-type", event}} {
		headers = append(headers, byte(len(pair[0])))
		headers = append(headers, pair[0]...)
		headers = append(headers, 7, byte(len(pair[1])>>8), byte(len(pair[1])))
		headers = append(headers, pair[1]...)
	}
	frame := make([]byte, 12)
	binary.BigEndian.PutUint32(frame, uint32(16+len(headers)+len(payload)))
	binary.BigEndian.PutUint32(frame[4:], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:], crc32.ChecksumIEEE(frame[:8]))
	frame = append(frame, headers...)
	frame = append(frame, payload...)
	return binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame))
}

func TestKiroExecutorProtocolsAndFinalPayloadBarrier(t *testing.T) {
	for _, source := range []struct {
		format  sdktranslator.Format
		payload string
	}{
		{sdktranslator.FormatOpenAI, `{"model":"auto","messages":[{"role":"user","content":"Hello"}],"max_tokens":500}`},
		{sdktranslator.FormatClaude, `{"model":"auto","messages":[{"role":"user","content":"Hello"}],"max_tokens":500}`},
		{sdktranslator.FormatOpenAIResponse, `{"model":"auto","input":[{"role":"user","content":[{"type":"input_text","text":"Hello"}]}],"max_output_tokens":500}`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(source.format.String()+map[bool]string{true: "/stream", false: "/nonstream"}[stream], func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if r.URL.Path != "/generateAssistantResponse" || r.Header.Get("Authorization") != "Bearer test-token" {
						t.Errorf("request: %s", r.URL)
					}
					if gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.content").String() != "Configured content" || gjson.GetBytes(body, "profileArn").Exists() || r.Header.Get("x-amzn-kiro-profile-arn") != "" || gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.modelId").String() != "configured-model" {
						t.Errorf("payload config was overwritten: %s", body)
					}
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					_, _ = w.Write(kiroExecutorFrame("assistantResponseEvent", `{"content":"Hello world"}`))
					_, _ = w.Write(kiroExecutorFrame("metadataEvent", `{"tokenUsage":{"uncachedInputTokens":4,"cacheReadInputTokens":10,"outputTokens":2}}`))
				}))
				defer server.Close()
				cfg := &config.Config{Payload: config.PayloadConfig{
					Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "kiro"}}, Params: map[string]any{"conversationState.currentMessage.userInputMessage.content": "Configured content", "conversationState.currentMessage.userInputMessage.modelId": "configured-model"}}},
					Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "kiro"}}, Params: []string{"profileArn"}}},
				}}
				executor := NewKiroExecutor(cfg)
				auth := &coreauth.Auth{Provider: "kiro", Metadata: map[string]any{"access_token": "test-token", "profile_arn": "remove-me", "base_url": server.URL}}
				req := coreexecutor.Request{Model: "auto", Payload: []byte(source.payload)}
				opts := coreexecutor.Options{SourceFormat: source.format, Stream: stream}
				var output []byte
				if stream {
					result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output = append(output, chunk.Payload...)
					}
				} else {
					result, err := executor.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output = result.Payload
				}
				if !bytes.Contains(output, []byte("Hello world")) {
					t.Fatalf("response: %s", output)
				}
				if source.format == sdktranslator.FormatClaude && !bytes.Contains(output, []byte("message")) {
					t.Fatalf("Claude response: %s", output)
				}
				if source.format == sdktranslator.FormatOpenAIResponse && !bytes.Contains(output, []byte("response")) {
					t.Fatalf("Responses response: %s", output)
				}
			})
		}
	}
}

func TestKiroRuntimeProfileHeaderUsesConfiguredProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if got := r.Header.Get("x-amzn-kiro-profile-arn"); got != "configured-profile" || got != gjson.GetBytes(body, "profileArn").String() {
			t.Errorf("profile header %q does not match configured payload %s", got, body)
		}
		ua := r.Header.Get("User-Agent")
		identity := strings.TrimPrefix(r.Header.Get("x-amz-user-agent"), "aws-sdk-js/1.0.39 ")
		if !strings.HasSuffix(ua, identity) {
			t.Errorf("runtime headers carry different identities: %q / %q", ua, identity)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(kiroExecutorFrame("assistantResponseEvent", `{"content":"ok"}`))
	}))
	defer server.Close()
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*", Protocol: "kiro"}},
		Params: map[string]any{"profileArn": "configured-profile"},
	}}}}
	executor := NewKiroExecutor(cfg)
	auth := &coreauth.Auth{Provider: "kiro", Metadata: map[string]any{"access_token": "fixture-access", "profile_arn": "original-profile", "base_url": server.URL}}
	_, err := executor.Execute(context.Background(), auth, coreexecutor.Request{Model: "auto", Payload: []byte(`{"messages":[{"role":"user","content":"Hello"}]}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatal(err)
	}
}

func TestKiroExecutorTruncatedStreamDoesNotComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		frame := kiroExecutorFrame("assistantResponseEvent", `{"content":"partial"}`)
		_, _ = w.Write(frame[:len(frame)-2])
	}))
	defer server.Close()
	executor := NewKiroExecutor(&config.Config{})
	auth := &coreauth.Auth{Provider: "kiro", Metadata: map[string]any{"access_token": "test-token", "base_url": server.URL}}
	result, err := executor.ExecuteStream(context.Background(), auth, coreexecutor.Request{Model: "auto", Payload: []byte(`{"messages":[{"role":"user","content":"Hello"}]}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	var failed bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			failed = true
		}
		if strings.Contains(string(chunk.Payload), "[DONE]") {
			t.Fatal("truncated stream falsely completed")
		}
	}
	if !failed {
		t.Fatal("truncation was swallowed")
	}
}

func TestKiroApplyPatchStreamReportsTranslationFailure(t *testing.T) {
	for _, input := range []string{`{"input":7}`, `{"input":"*** Begin Patch\n*** End Patch"}`} {
		t.Run(input, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				event, _ := sjson.Set(`{"toolUseId":"c1","name":"apply_patch","stop":true}`, "input", input)
				_, _ = w.Write(kiroExecutorFrame("toolUseEvent", event))
			}))
			defer upstream.Close()
			capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
			ctx := coreusage.WithRequestedModelAlias(context.Background(), t.Name())
			original := []byte(`{"model":"auto","stream":true,"tools":[{"type":"custom","name":"apply_patch","description":"Apply a patch"}],"input":[{"role":"user","content":[{"type":"input_text","text":"Fix file"}]}]}`)
			ex := NewKiroExecutor(&config.Config{})
			result, err := ex.ExecuteStream(ctx, &coreauth.Auth{Provider: "kiro", Metadata: map[string]any{"access_token": "fixture", "base_url": upstream.URL}}, coreexecutor.Request{Model: "auto", Payload: original}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: original, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var frames []byte
			var streamError error
			for chunk := range result.Chunks {
				frames = append(frames, chunk.Payload...)
				if chunk.Err != nil {
					streamError = chunk.Err
				}
			}
			valid := input != `{"input":7}`
			if !valid && (streamError == nil || bytes.Count(frames, []byte(`"type":"response.failed"`)) != 1 || bytes.Contains(frames, []byte(`"type":"response.completed"`))) {
				t.Fatalf("translation failure not propagated: error=%v frames=%s", streamError, frames)
			}
			if valid && (streamError != nil || !bytes.Contains(frames, []byte(`"type":"response.completed"`))) {
				t.Fatalf("valid tool call failed: error=%v frames=%s", streamError, frames)
			}
			if record := capture.await(t); record.Failed == valid {
				t.Fatalf("usage outcome mismatch: failed=%v valid=%v", record.Failed, valid)
			}
		})
	}
}
