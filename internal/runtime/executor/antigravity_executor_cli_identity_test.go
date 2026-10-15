package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityCLIIdentityRequestPaths(t *testing.T) {
	const identity = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	const suffix = " \t\r\n\n  Keep these instructions.\n"
	const userText = identity + "\nUser content must remain unchanged."
	quote := func(text string) string {
		raw, errMarshal := json.Marshal(text)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		return string(raw)
	}
	sources := []struct {
		name     string
		format   sdktranslator.Format
		template string
	}{
		{"chat string", sdktranslator.FormatOpenAI, `{"messages":[{"role":"system","content":%s},{"role":"user","content":%s}]}`},
		{"chat developer array", sdktranslator.FormatOpenAI, `{"messages":[{"role":"developer","content":[{"type":"text","text":%s}]},{"role":"user","content":%s}]}`},
		{"claude string", sdktranslator.FormatClaude, `{"system":%s,"messages":[{"role":"user","content":%s}],"max_tokens":256}`},
		{"claude array", sdktranslator.FormatClaude, `{"system":[{"type":"text","text":%s}],"messages":[{"role":"user","content":%s}],"max_tokens":256}`},
		{"responses instructions", sdktranslator.FormatOpenAIResponse, `{"instructions":%s,"input":[{"role":"user","content":[{"type":"input_text","text":%s}]}]}`},
		{"responses system input", sdktranslator.FormatOpenAIResponse, `{"input":[{"role":"system","content":[{"type":"input_text","text":%s}]},{"role":"user","content":[{"type":"input_text","text":%s}]}]}`},
		{"gemini snake case", sdktranslator.FormatGemini, `{"system_instruction":{"parts":[{"text":%s}]},"contents":[{"role":"user","parts":[{"text":%s}]}]}`},
		{"gemini camel case", sdktranslator.FormatGemini, `{"systemInstruction":{"parts":[{"text":%s}]},"contents":[{"role":"user","parts":[{"text":%s}]}]}`},
		{"interactions string", sdktranslator.FormatInteractions, `{"system_instruction":%s,"input":[{"type":"text","text":%s}]}`},
		{"interactions parts", sdktranslator.FormatInteractions, `{"system_instruction":{"parts":[{"text":%s}]},"input":[{"type":"text","text":%s}]}`},
	}
	models := []config.PayloadModelRule{{Name: "gemini-3-flash"}}
	cases := []struct {
		name    string
		model   string
		system  string
		want    string
		drop    bool
		payload config.PayloadConfig
	}{
		{name: "strip exact prefix", model: "gemini-3-flash", system: identity + suffix, want: suffix},
		{name: "identity only", model: "gemini-3-flash", system: identity, drop: true},
		{name: "default identity unchanged", model: "gemini-3-flash", system: "You are Claude Code, Anthropic's official CLI for Claude.", want: "You are Claude Code, Anthropic's official CLI for Claude."},
		{name: "preset identity unchanged", model: "gemini-3-flash", system: "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.", want: "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."},
		{name: "Claude unchanged", model: "claude-sonnet-4-6", system: identity + suffix, want: identity + suffix},
		{name: "other Gemini unchanged", model: "gemini-3-pro", system: identity + suffix, want: identity + suffix},
		{name: "thinking suffix", model: "gemini-3-flash(high)", system: identity + suffix, want: suffix},
		{name: "payload override wins", model: "gemini-3-flash", system: identity + suffix, want: identity + " configured", payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: map[string]any{"systemInstruction.parts.0.text": identity + " configured"}}}}},
		{name: "payload filter wins", model: "gemini-3-flash", system: identity + suffix, drop: true, payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: models, Params: []string{"systemInstruction"}}}}},
	}
	for _, source := range sources {
		for _, tc := range cases {
			for _, kind := range []string{"execute", "stream", "count"} {
				t.Run(source.name+"/"+tc.name+"/"+kind, func(t *testing.T) {
					var captured []byte
					calls := 0
					ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						calls++
						var errRead error
						captured, errRead = io.ReadAll(req.Body)
						if errRead != nil {
							t.Fatal(errRead)
						}
						wantPath := antigravityGeneratePath
						if kind == "count" {
							wantPath = antigravityCountTokensPath
						} else if kind == "stream" || strings.Contains(tc.model, "claude") || strings.Contains(tc.model, "gemini-3-pro") {
							wantPath = antigravityStreamPath
						}
						if req.URL.Path != wantPath {
							t.Fatalf("path = %s, want %s", req.URL.Path, wantPath)
						}
						// Stop at a synthetic upstream response after capturing the actual wire body.
						return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":400,"message":"local request capture"}}`))}, nil
					}))
					auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "synthetic-token", "expired": time.Now().Add(2 * time.Hour).Format(time.RFC3339), "project_id": "synthetic-project"}}
					req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(fmt.Sprintf(source.template, quote(tc.system), quote(userText)))}
					opts := cliproxyexecutor.Options{SourceFormat: source.format, ResponseFormat: source.format}
					executor := NewAntigravityExecutor(&config.Config{Payload: tc.payload})
					var errRequest error
					switch kind {
					case "execute":
						_, errRequest = executor.Execute(ctx, auth, req, opts)
					case "stream":
						_, errRequest = executor.ExecuteStream(ctx, auth, req, opts)
					case "count":
						_, errRequest = executor.CountTokens(ctx, auth, req, opts)
					}
					if errRequest == nil || calls != 1 {
						t.Fatalf("expected one captured request and synthetic error, calls=%d err=%v", calls, errRequest)
					}
					if tc.drop {
						if gjson.GetBytes(captured, "request.systemInstruction").Exists() {
							t.Fatalf("systemInstruction should be absent: %s", captured)
						}
					} else if got := gjson.GetBytes(captured, "request.systemInstruction.parts.0.text").String(); got != tc.want {
						t.Fatalf("system text = %q, want %q; body=%s", got, tc.want, captured)
					}
					foundUser := false
					for _, content := range gjson.GetBytes(captured, "request.contents").Array() {
						for _, part := range content.Get("parts").Array() {
							if part.Get("text").String() == userText {
								foundUser = true
							}
						}
					}
					if !foundUser {
						t.Fatalf("user text missing or modified: %s", captured)
					}
				})
			}
		}
	}
}

func TestAntigravityCLIIdentitySchemaAndRebuiltAttempts(t *testing.T) {
	const identity = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	for _, withSchema := range []bool{false, true} {
		for _, override := range []bool{false, true} {
			t.Run(fmt.Sprintf("schema=%t/override=%t", withSchema, override), func(t *testing.T) {
				body := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"` + identity + `\n  Body"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
				if withSchema {
					body = []byte(`{"request":{"systemInstruction":{"parts":[{"text":"` + identity + `\n  Body"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"read","parametersJsonSchema":{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"name":{"type":"string"}}}}]}]}}`)
				}
				cfg := &config.Config{}
				want := "\n  Body"
				if override {
					want = identity + " configured"
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-3-flash"}}, Params: map[string]any{"systemInstruction.parts.0.text": want}}}
				}
				req := cliproxyexecutor.Request{Model: "gemini-3-flash"}
				ctx := helps.WithPayloadFinalizer(t.Context(), helps.NewPayloadFinalizer(cfg, "antigravity", req.Model, "antigravity", "request", body, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}))
				executor := NewAntigravityExecutor(cfg)
				auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "synthetic-project"}}
				for attempt := 0; attempt < 2; attempt++ {
					httpReq, errBuild := executor.buildRequest(ctx, auth, "synthetic-token", req.Model, body, attempt == 1, "", antigravityBaseURLDaily)
					if errBuild != nil {
						t.Fatal(errBuild)
					}
					wire, errRead := io.ReadAll(httpReq.Body)
					if errRead != nil {
						t.Fatal(errRead)
					}
					if errClose := httpReq.Body.Close(); errClose != nil {
						t.Fatal(errClose)
					}
					if got := gjson.GetBytes(wire, "request.systemInstruction.parts.0.text").String(); got != want {
						t.Fatalf("attempt %d: system = %q, want %q", attempt, got, want)
					}
					if withSchema && !gjson.GetBytes(wire, "request.tools.0.functionDeclarations.0").Exists() {
						t.Fatalf("tool lost: %s", wire)
					}
					if got := gjson.GetBytes(body, "request.systemInstruction.parts.0.text").String(); got != identity+"\n  Body" {
						t.Fatalf("retry source mutated: %q", got)
					}
				}
			})
		}
	}
}
