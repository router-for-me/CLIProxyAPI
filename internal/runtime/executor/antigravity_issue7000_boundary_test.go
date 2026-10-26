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
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const issue7000LiteralSchema = `{"type":"object","properties":{"anthropic":{"type":"string","enum":["anthropic","json_schema"],"const":"anthropic"},"response_schema":{"$ref":"#/$defs/anthropic"}},"$defs":{"anthropic":{"type":"string"}},"required":["anthropic"],"additionalProperties":false}`

func assertIssue7000PromptSchema(t *testing.T, instruction gjson.Result, schemaJSON string) {
	t.Helper()
	var prompt strings.Builder
	for _, part := range instruction.Get("parts").Array() {
		prompt.WriteString(part.Get("text").String())
		prompt.WriteByte('\n')
	}
	var want any
	if errUnmarshal := json.Unmarshal([]byte(schemaJSON), &want); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	for offset, char := range prompt.String() {
		if char != '{' {
			continue
		}
		var got any
		if errDecode := json.NewDecoder(strings.NewReader(prompt.String()[offset:])).Decode(&got); errDecode == nil && reflect.DeepEqual(got, want) {
			return
		}
	}
	t.Errorf("system instruction must contain the complete unmodified schema %s; got %q", schemaJSON, prompt.String())
}

func TestIssue7000AntigravityAliasesPreserveOtherFieldsAndToolChoice(t *testing.T) {
	for _, container := range []string{"generationConfig", "generation_config"} {
		for index, schemaKey := range []string{"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema"} {
			for _, mode := range []string{"AUTO", "NONE", "ANY", "VALIDATED"} {
				t.Run(container+"/"+schemaKey+"/"+mode, func(t *testing.T) {
					payload := []byte(`{"request":{"generationConfig":{"temperature":0.25},"generation_config":{"stopSequences":["STOP"]},"systemInstruction":{"role":"system","parts":[{"text":"keep system"}]},"contents":[{"role":"user","parts":[{"text":"keep history"}]}],"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}]}]}}`)
					setRaw := func(path, value string) { payload, _ = sjson.SetRawBytes(payload, path, []byte(value)) }
					setRaw("request."+container+"."+schemaKey, issue7000LiteralSchema)
					setRaw("request."+container+".responseMimeType", `"application/json"`)
					setRaw("request."+container+".response_mime_type", `"application/json"`)
					toolContainer := []string{"toolConfig", "tool_config"}[index%2]
					callingKey := []string{"functionCallingConfig", "function_calling_config"}[index/2]
					toolPath := "request." + toolContainer + "." + callingKey
					setRaw(toolPath, fmt.Sprintf(`{"mode":%q,"allowedFunctionNames":["lookup"],"allowed_function_names":["lookup"],"streamFunctionCallArguments":true}`, mode))
					systemKey := "systemInstruction"
					if container == "generation_config" {
						systemKey = "system_instruction"
						setRaw("request."+systemKey, gjson.GetBytes(payload, "request.systemInstruction").Raw)
						payload, _ = sjson.DeleteBytes(payload, "request.systemInstruction")
					}
					body := buildRequestBodyFromRawPayload(t, "claude-opus-5-5-high", payload)
					encoded, errMarshal := json.Marshal(body)
					if errMarshal != nil {
						t.Fatal(errMarshal)
					}
					request := gjson.GetBytes(encoded, "request")
					assertIssue7000PromptSchema(t, request.Get(systemKey), issue7000LiteralSchema)
					for path, want := range map[string]string{
						"generationConfig.temperature": "0.25", "generation_config.stopSequences.0": "STOP",
						systemKey + ".parts.0.text": "keep system", "contents.0.parts.0.text": "keep history",
						"tools.0.functionDeclarations.0.name": "lookup",
					} {
						if got := request.Get(path).String(); got != want {
							t.Errorf("%s = %q, want %q", path, got, want)
						}
					}
					for _, configKey := range []string{"generationConfig", "generation_config"} {
						for _, key := range []string{"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema", "responseMimeType", "response_mime_type"} {
							if request.Get(configKey + "." + key).Exists() {
								t.Errorf("native output trigger survived: %s.%s", configKey, key)
							}
						}
					}
					wantMode := mode
					if mode == "ANY" || mode == "VALIDATED" {
						wantMode = "AUTO"
						for _, key := range []string{"allowedFunctionNames", "allowed_function_names"} {
							if gjson.GetBytes(encoded, toolPath+"."+key).Exists() {
								t.Errorf("forced-tool allowlist survived AUTO downgrade: %s", key)
							}
						}
					}
					if got := gjson.GetBytes(encoded, toolPath+".mode").String(); got != wantMode {
						t.Errorf("tool choice = %q, want %q", got, wantMode)
					}
					if !gjson.GetBytes(encoded, toolPath+".streamFunctionCallArguments").Bool() {
						t.Error("unrelated tool configuration was removed")
					}
					if toolContainer == "tool_config" && request.Get("toolConfig").Exists() {
						t.Error("compatibility conversion introduced a conflicting camelCase tool config")
					}
				})
			}
		}
	}
}

func TestIssue7000AntigravityModelAndPlainRequestBoundaries(t *testing.T) {
	for _, tc := range []struct {
		model   string
		convert bool
	}{
		{"claude-opus-5-5", true}, {"claude-sonnet-5-5-high", true},
		{"claude-opus-5-5-reasoning", true}, {"claude-sonnet-5-5(high)", true},
		{"claude-opus-5-5-20261001", true}, {"claude-sonnet-5-5[1m]", true},
		{"claude-opus-5-5-20261001[1m](high)", true},
		{"claude-opus-5-50-high", false}, {"claude-sonnet-5-50", false},
		{"claude-opus-4-6", false}, {"claude-haiku-5-5", false},
		{"gemini-3.8-flash-high", false}, {"other/claude-opus-5-5-high", false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			payload := []byte(`{"request":{"generationConfig":{"responseSchema":{"type":"object","properties":{"title":{"type":"string"}}},"responseMimeType":"application/json"}}}`)
			body := buildRequestBodyFromRawPayload(t, tc.model, payload)
			encoded, errMarshal := json.Marshal(body)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if got := gjson.GetBytes(encoded, "request.generationConfig.responseSchema").Exists(); got == tc.convert {
				t.Errorf("native schema presence = %t, conversion = %t", got, tc.convert)
			}
			if tc.convert {
				assertIssue7000PromptSchema(t, gjson.GetBytes(encoded, "request.systemInstruction"), `{"type":"object","properties":{"title":{"type":"string"}}}`)
			}
		})
	}
	for _, configJSON := range []string{`{"temperature":0.4}`, `{"responseMimeType":"text/plain"}`} {
		t.Run("plain/"+configJSON, func(t *testing.T) {
			payload := []byte(`{"request":{"generationConfig":` + configJSON + `,"systemInstruction":{"parts":[{"text":"unchanged"}]},"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"]}}}}`)
			body := buildRequestBodyFromRawPayload(t, "claude-opus-5-5-high", payload)
			encoded, errMarshal := json.Marshal(body)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if got := gjson.GetBytes(encoded, "request.systemInstruction.parts.#").Int(); got != 1 {
				t.Errorf("plain request gained instructions: %s", encoded)
			}
			if got := gjson.GetBytes(encoded, "request.toolConfig.functionCallingConfig.mode").String(); got != "VALIDATED" {
				t.Errorf("unrelated legacy tool behavior changed: %q", got)
			}
		})
	}
}

func TestIssue7000AntigravityExecutionAndFinalPayloadRules(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		for _, operation := range []string{"execute", "stream", "count"} {
			for _, rules := range []string{"none", "override", "filter"} {
				t.Run(format.String()+"/"+operation+"/"+rules, func(t *testing.T) {
					captured := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Error(errRead)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						captured <- body
						if operation == "count" {
							if r.URL.Path != antigravityCountTokensPath {
								t.Errorf("count path = %q", r.URL.Path)
							}
							_, _ = w.Write([]byte(`{"totalTokens":17}`))
							return
						}
						if r.URL.Path != antigravityStreamPath {
							t.Errorf("Claude must use stream upstream, got %q", r.URL.Path)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"{}\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":17,\"candidatesTokenCount\":1,\"totalTokenCount\":18}}}\n\n")
					}))
					defer server.Close()
					cfg := &config.Config{DisableCooling: true, Antigravity: config.AntigravityConfig{SensitiveWords: []string{"anthropic"}}}
					models := []config.PayloadModelRule{{Name: "claude-*", Protocol: "antigravity"}}
					switch rules {
					case "override":
						cfg.Payload.Override = []config.PayloadRule{{Models: models, Params: map[string]any{"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "configured anthropic"}}}, "toolConfig.functionCallingConfig.mode": "NONE", "generationConfig.responseMimeType": "configured/mime"}}}
					case "filter":
						cfg.Payload.Filter = []config.PayloadFilterRule{{Models: models, Params: []string{"systemInstruction", "toolConfig", "contents.0"}}}
					}
					model := "claude-opus-5-5-high"
					payload := []byte(`{"model":"` + model + `","temperature":0.25,"tool_choice":"auto"}`)
					if format == sdktranslator.FormatOpenAI {
						payload, _ = sjson.SetRawBytes(payload, "messages", []byte(`[{"role":"system","content":"keep anthropic system"},{"role":"user","content":"first"},{"role":"assistant","content":"second"},{"role":"user","content":"third"}]`))
						payload, _ = sjson.SetRawBytes(payload, "response_format", []byte(`{"type":"json_schema","json_schema":{"schema":`+issue7000LiteralSchema+`}}`))
						payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}]`))
					} else {
						model = "claude-sonnet-5-5-high"
						payload, _ = sjson.SetBytes(payload, "model", model)
						payload, _ = sjson.SetBytes(payload, "instructions", "keep anthropic system")
						payload, _ = sjson.SetRawBytes(payload, "input", []byte(`[{"role":"user","content":"first"},{"role":"assistant","content":"second"},{"role":"user","content":"third"}]`))
						payload, _ = sjson.SetRawBytes(payload, "text.format", []byte(`{"type":"json_schema","schema":`+issue7000LiteralSchema+`}`))
						payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}]`))
					}
					exec := NewAntigravityExecutor(cfg)
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "token", "project_id": "project-test", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
					req := cliproxyexecutor.Request{Model: model, Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: format}
					switch operation {
					case "execute":
						if _, errExecute := exec.Execute(context.Background(), auth, req, opts); errExecute != nil {
							t.Fatal(errExecute)
						}
					case "stream":
						result, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					case "count":
						if _, errCount := exec.CountTokens(context.Background(), auth, req, opts); errCount != nil {
							t.Fatal(errCount)
						}
					}
					body := <-captured
					request := gjson.GetBytes(body, "request")
					if got := request.Get("tools.0.functionDeclarations.0.name").String(); got != "lookup" {
						t.Errorf("real tool lost: %s", body)
					}
					switch rules {
					case "none":
						assertIssue7000PromptSchema(t, request.Get("systemInstruction"), issue7000LiteralSchema)
						if !strings.Contains(request.Get("systemInstruction.parts.0.text").String(), "a\u200bnthropic") {
							t.Errorf("original system instruction was lost or not obfuscated: %s", body)
						}
						for _, key := range []string{"responseSchema", "responseMimeType"} {
							if request.Get("generationConfig." + key).Exists() {
								t.Errorf("native output trigger survived: %s", body)
							}
						}
						if operation != "count" {
							if mode := request.Get("toolConfig.functionCallingConfig.mode").String(); mode != "" && mode != "AUTO" {
								t.Errorf("forced tool mode survived: %s", body)
							}
						}
					case "override":
						if request.Get("systemInstruction.parts.#").Int() != 1 || request.Get("systemInstruction.parts.0.text").String() != "configured anthropic" || request.Get("toolConfig.functionCallingConfig.mode").String() != "NONE" || request.Get("generationConfig.responseMimeType").String() != "configured/mime" {
							t.Errorf("built-in logic changed final override: %s", body)
						}
					case "filter":
						if request.Get("systemInstruction").Exists() || request.Get("toolConfig").Exists() || request.Get("contents.#").Int() != 2 {
							t.Errorf("filter was undone or applied more than once: %s", body)
						}
					}
				})
			}
		}
	}
}
