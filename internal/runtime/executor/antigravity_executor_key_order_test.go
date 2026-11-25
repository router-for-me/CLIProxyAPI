package executor

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityAgentHTTPKeyOrder(t *testing.T) {
	const request = `{
		"x-root-z":{"z":1,"a":2}, "enabledCreditTypes":["example"], "requestType":"agent", "x-root-a":false,
		"request":{
			"x-request-z":null, "labels":{"x-label-z":"first","x-label-a":"second"}, "x-request-a":{},
			"systemInstruction":{"parts":[{"text":"system"}],"role":"user"},
			"contents":[{"parts":[{"text":"first"},{"text":"second"}],"role":"user"}],
			"sessionId":"example"
		}
	}`
	const overrideContents = `[
		{"parts":[{"text":"first  line\nsecond\tline"},{"text":"second"}],"role":"user"},
		{"parts":[{"x-part-z":1,"thoughtSignature":"signature","x-part-a":2,
			"functionCall":{"args":{"z":900719925474099312345,"a":[{"y":-0,"x":"\u003c\/\\\""},1.200e+03]},"name":"tool","id":"call"}}],"role":"model"},
		{"parts":[{"functionResponse":{"response":{"z":0,"a":{"z":2,"a":1}},"name":"tool","id":"call"}}],"role":"user"}
	]`
	const overrideTools = `[{"functionDeclarations":[{"parameters":{
		"required":["z","a"],"x-schema-z":{"z":0,"a":1},"properties":{
			"z":{"minItems":1,"items":{"required":["b"],"properties":{"b":{"description":"nested","type":"string"}},"type":"object"},"type":"array"},
			"a":{"minLength":2,"description":"example","type":"string"}
		},"x-schema-a":{},"type":"object"
	},"description":"example tool","name":"tool"}]}]`
	const response = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`

	for _, tc := range []struct {
		name, model, requestType, path string
		stream, count, agent, retry    bool
	}{
		{name: "unary", model: "gemini-3-flash", requestType: "agent", path: antigravityGeneratePath, agent: true},
		{name: "stream", model: "gemini-3-flash", requestType: "agent", path: antigravityStreamPath, stream: true, agent: true},
		{name: "claude_unary_over_sse", model: "claude-sonnet-4-6", requestType: "agent", path: antigravityStreamPath, agent: true},
		{name: "retry", model: "gemini-3-flash", requestType: "agent", path: antigravityGeneratePath, agent: true, retry: true},
		{name: "web_search", model: "gemini-3-flash", requestType: "web_search", path: antigravityGeneratePath},
		{name: "image", model: "gemini-3.1-flash-image", requestType: "image_gen", path: antigravityStreamPath},
		{name: "count_tokens", model: "gemini-3-flash", requestType: "agent", path: antigravityCountTokensPath, count: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 1
			if tc.retry {
				attempts = 2
			}
			captured := make(chan []byte, attempts)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != tc.path {
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
				}
				raw, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
				}
				select {
				case captured <- raw:
				default:
					t.Error("unexpected additional request")
				}
				body := response
				w.Header().Set("Content-Type", "application/json")
				if tc.retry && r.Header.Get("Authorization") == "Bearer unavailable-token" {
					w.WriteHeader(http.StatusServiceUnavailable)
					body = `{"error":{"code":503,"status":"UNAVAILABLE","message":"try again"}}`
				} else if tc.path == antigravityStreamPath {
					w.Header().Set("Content-Type", "text/event-stream")
					body = "data: " + response + "\n\n"
				} else if tc.count {
					body = `{"totalTokens":2}`
				}
				if _, errWrite := io.WriteString(w, body); errWrite != nil {
					t.Error(errWrite)
				}
			}))
			defer server.Close()

			models := []config.PayloadModelRule{{Name: "*"}}
			executor := NewAntigravityExecutor(&config.Config{Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{Models: models, Params: map[string]any{"labels.used_claude": "configured"}}},
				OverrideRaw: []config.PayloadRule{{Models: models, Params: map[string]any{
					"contents": overrideContents,
					"tools":    overrideTools,
					"generationConfig": `{"thinkingConfig":{"thinkingLevel":"configured","includeThoughts":false},` +
						`"x-a":{},"topP":0.8,"temperature":0.5,"maxOutputTokens":2500,"candidateCount":2}`,
				}}},
				Filter: []config.PayloadFilterRule{{Models: models, Params: []string{"generationConfig.candidateCount"}}},
			}})
			auth := &cliproxyauth.Auth{
				ID: t.Name(), Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL},
				Metadata: map[string]any{"project_id": "example-project", "access_token": "token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)},
			}
			modelEnums := map[string]string{tc.model: "MODEL_ORDER_TEST"}
			if !tc.retry {
				cliproxyauth.StoreAntigravityModelEnums(t.Context(), auth, modelEnums)
			}
			payload := []byte(strings.Replace(request, `"requestType":"agent"`, `"requestType":"`+tc.requestType+`"`, 1))
			req := cliproxyexecutor.Request{Model: tc.model, Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity, Stream: tc.stream}
			switch {
			case tc.retry:
				manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
				manager.RegisterExecutor(executor)
				manager.SetRetryConfig(0, 0, attempts)
				first := auth.Clone()
				first.ID += "/1"
				first.Metadata["access_token"] = "unavailable-token"
				first.Metadata["project_id"] = "unavailable-project"
				auth.ID += "/2"
				reg := registry.GetGlobalRegistry()
				for _, candidate := range []*cliproxyauth.Auth{first, auth} {
					reg.RegisterClient(candidate.ID, "antigravity", []*registry.ModelInfo{{ID: tc.model}})
					t.Cleanup(func() { reg.UnregisterClient(candidate.ID) })
					registered, errRegister := manager.Register(t.Context(), candidate)
					if errRegister != nil {
						t.Fatal(errRegister)
					}
					cliproxyauth.StoreAntigravityModelEnums(t.Context(), registered, modelEnums)
				}
				if _, errExec := manager.Execute(t.Context(), []string{"antigravity"}, req, opts); errExec != nil {
					t.Fatal(errExec)
				}
			case tc.stream:
				res, errStream := executor.ExecuteStream(t.Context(), auth, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			case tc.count:
				if _, errCount := executor.CountTokens(t.Context(), auth, req, opts); errCount != nil {
					t.Fatal(errCount)
				}
			default:
				if _, errExec := executor.Execute(t.Context(), auth, req, opts); errExec != nil {
					t.Fatal(errExec)
				}
			}

			for i := range attempts {
				var body ojson.Result
				select {
				case raw := <-captured:
					var compact bytes.Buffer
					if errCompact := json.Compact(&compact, raw); errCompact != nil {
						t.Fatalf("invalid outbound JSON: %v", errCompact)
					}
					if !bytes.Equal(raw, compact.Bytes()) {
						t.Errorf("attempt %d: outbound JSON contains %d bytes of formatting whitespace", i+1, len(raw)-compact.Len())
					}
					body = ojson.ParseBytes(raw)
					if text := body.Get("request.contents.0.parts.0.text"); text.Raw != `"first  line\nsecond\tline"` {
						t.Errorf("text whitespace or escaping changed: %s", text.Raw)
					}
				default:
					t.Fatalf("outbound HTTP body missing for attempt %d", i+1)
				}
				if !tc.agent {
					assertAntigravityKeyOrder(t, body, "request.contents.0", "parts", "role")
					continue
				}
				if tc.retry {
					want := []string{"unavailable-project", "example-project"}[i]
					if got := body.Get("project").Str; got != want {
						t.Errorf("project at attempt %d = %q, want %q", i+1, got, want)
					}
				}
				assertAntigravityOrderedAgentBody(t, body, tc.model == "claude-sonnet-4-6")
			}
		})
	}
}

func assertAntigravityOrderedAgentBody(t *testing.T, body ojson.Result, expectsToolConfig bool) {
	t.Helper()
	assertAntigravityKeyOrder(t, body, "", "project", "requestId", "request", "model", "userAgent", "requestType", "x-root-z", "enabledCreditTypes", "x-root-a")
	requestKeys := []string{"contents", "systemInstruction", "tools", "labels", "generationConfig", "sessionId", "x-request-z", "x-request-a"}
	if expectsToolConfig {
		requestKeys = append(requestKeys, "toolConfig")
	}
	assertAntigravityKeyOrder(t, body, "request", requestKeys...)
	assertAntigravityKeyOrder(t, body, "request.labels", "cascade_id", "last_step_index", "model_enum", "request_id", "root_cascade_id", "trajectory_id", "used_claude", "used_claude_conservative", "used_non_gemini_model", "x-label-z", "x-label-a")
	assertAntigravityKeyOrder(t, body, "request.systemInstruction", "role", "parts")
	assertAntigravityKeyOrder(t, body, "request.contents.1", "role", "parts")
	assertAntigravityKeyOrder(t, body, "request.contents.1.parts.0", "functionCall", "thoughtSignature", "x-part-z", "x-part-a")
	assertAntigravityKeyOrder(t, body, "request.contents.1.parts.0.functionCall", "id", "name", "args")
	assertAntigravityKeyOrder(t, body, "request.contents.2.parts.0.functionResponse", "id", "name", "response")
	assertAntigravityKeyOrder(t, body, "request.tools.0.functionDeclarations.0", "name", "description", "parameters")
	const schema = "request.tools.0.functionDeclarations.0.parameters"
	assertAntigravityKeyOrder(t, body, schema, "type", "properties", "required", "x-schema-z", "x-schema-a")
	assertAntigravityKeyOrder(t, body, schema+".properties", "a", "z")
	assertAntigravityKeyOrder(t, body, schema+".properties.a", "type", "description", "minLength")
	assertAntigravityKeyOrder(t, body, schema+".properties.z", "type", "items", "minItems")
	assertAntigravityKeyOrder(t, body, schema+".properties.z.items", "type", "properties", "required")
	assertAntigravityKeyOrder(t, body, schema+".properties.z.items.properties.b", "type", "description")
	assertAntigravityKeyOrder(t, body, "request.generationConfig", "maxOutputTokens", "thinkingConfig", "x-a", "topP", "temperature")
	assertAntigravityKeyOrder(t, body, "request.generationConfig.thinkingConfig", "includeThoughts", "thinkingLevel")

	for path, want := range map[string]string{
		"x-root-z":                                              `{"z":1,"a":2}`,
		"request.labels.used_claude":                            `"configured"`,
		"request.contents.0.parts":                              `[{"text":"first  line\nsecond\tline"},{"text":"second"}]`,
		"request.contents.1.parts.0.functionCall.args":          `{"a":[{"x":"\u003c\/\\\"","y":-0},1.200e+03],"z":900719925474099312345}`,
		"request.contents.2.parts.0.functionResponse.response":  `{"a":{"a":1,"z":2},"z":0}`,
		schema + ".required":                                    `["z","a"]`,
		schema + ".x-schema-z":                                  `{"z":0,"a":1}`,
		"request.generationConfig.maxOutputTokens":              `2500`,
		"request.generationConfig.thinkingConfig.thinkingLevel": `"configured"`,
	} {
		if got := body.Get(path).Raw; got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
}

func assertAntigravityKeyOrder(t *testing.T, body ojson.Result, path string, want ...string) {
	t.Helper()
	if path != "" {
		body = body.Get(path)
	}
	var got []string
	body.ForEach(func(key, _ ojson.Result) bool {
		got = append(got, key.Str)
		return true
	})
	if !slices.Equal(got, want) {
		t.Errorf("keys at %q = %v, want %v", path, got, want)
	}
}
