package executor

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutor_OpenAIStructuredOutputReachesOutputConfigFormat(t *testing.T) {
	const schema = `{"type":"object","properties":{"title":{"type":"string"},"difficulty":{"type":"string","enum":["EASY","HARD"]},"notes":{"type":["string","null"]}},"required":["title","difficulty","notes"],"additionalProperties":false}`
	responsesPayload := []byte(`{"model":"claude-sonnet-5-5","instructions":"You are a coding agent.","input":[{"role":"user","content":[{"type":"input_text","text":"Suggest a recipe."}]}],"reasoning":{"effort":"high"},"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}],"tool_choice":"auto","parallel_tool_calls":false,"text":{"format":{"type":"json_schema","name":"recipe","description":"A dish for two.","strict":true,"schema":` + schema + `}}}`)
	chatPayload := []byte(`{"model":"claude-sonnet-5-5","reasoning_effort":"high","messages":[{"role":"system","content":"You are a coding agent."},{"role":"user","content":"Suggest a recipe."}],"tools":[{"type":"function","function":{"name":"shell","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}],"tool_choice":"auto","parallel_tool_calls":false,"response_format":{"type":"json_schema","json_schema":{"name":"recipe","description":"A dish for two.","strict":true,"schema":` + schema + `}}}`)

	oauth := func() *cliproxyauth.Auth {
		metadata := claudeOAuthTestMetadata()
		metadata["access_token"] = "sk-ant-oat01-structured"
		return &cliproxyauth.Auth{Provider: "claude", Metadata: metadata}
	}
	cloakOff := &config.Config{}
	cloakOff.DisableClaudeCloakMode = true

	for _, source := range []struct {
		name    string
		format  sdktranslator.Format
		payload []byte
	}{
		{name: "responses", format: sdktranslator.FormatOpenAIResponse, payload: responsesPayload},
		{name: "chat", format: sdktranslator.FormatOpenAI, payload: chatPayload},
	} {
		for _, tc := range []struct {
			name        string
			cfg         *config.Config
			auth        *cliproxyauth.Auth
			countTokens bool
		}{
			{name: "api key", cfg: &config.Config{}, auth: &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-structured"}}, countTokens: true},
			{name: "oauth, cloaking off", cfg: cloakOff, auth: oauth()},
			{name: "oauth, cloaking auto", cfg: &config.Config{}, auth: oauth()},
		} {
			methods := []string{"Execute", "ExecuteStream"}
			if tc.countTokens {
				methods = append(methods, "CountTokens")
			}
			for _, method := range methods {
				t.Run(source.name+"/"+tc.name+"/"+method, func(t *testing.T) {
					body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(tc.cfg), tc.auth, cliproxyexecutor.Request{Model: "claude-sonnet-5-5", Payload: source.payload}, cliproxyexecutor.Options{SourceFormat: source.format}, method)
					if got := ojson.GetBytes(body, "output_config.format").Value(); !reflect.DeepEqual(got, ojson.Parse(`{"type":"json_schema","schema":`+schema+`}`).Value()) {
						t.Fatalf("output_config.format = %#v; body=%s", got, body)
					}
					if strings.Contains(string(body), "conforms strictly") {
						t.Errorf("schema instruction must not be sent beside output_config.format: %s", body)
					}
					if !strings.Contains(string(body), "A dish for two.") || !strings.Contains(string(body), "Schema Name: recipe") {
						t.Errorf("format name/description lost: %s", body)
					}
					if method != "CountTokens" && ojson.GetBytes(body, "output_config.effort").String() != "high" {
						t.Errorf("output_config.effort lost: %s", body)
					}
				})
			}
		}
	}
}

func TestClaudeExecutor_OpenAIStructuredOutputLegacyModelKeepsInstruction(t *testing.T) {
	const schema = `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-structured"}}
	for _, model := range []string{"claude-sonnet-4-20250514", "anthropic/claude-opus-4-1", "claude-3-7-sonnet-20250219"} {
		for _, source := range []struct {
			name    string
			format  sdktranslator.Format
			payload []byte
		}{
			{
				name:    "responses",
				format:  sdktranslator.FormatOpenAIResponse,
				payload: []byte(`{"model":"` + model + `","input":[{"role":"user","content":[{"type":"input_text","text":"Suggest a recipe."}]}],"text":{"format":{"type":"json_schema","name":"recipe","strict":true,"schema":` + schema + `}}}`),
			},
			{
				name:    "chat",
				format:  sdktranslator.FormatOpenAI,
				payload: []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"Suggest a recipe."}],"response_format":{"type":"json_schema","json_schema":{"name":"recipe","strict":true,"schema":` + schema + `}}}`),
			},
		} {
			for _, method := range []string{"Execute", "ExecuteStream", "CountTokens"} {
				t.Run(model+"/"+source.name+"/"+method, func(t *testing.T) {
					body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(&config.Config{}), auth, cliproxyexecutor.Request{Model: model, Payload: source.payload}, cliproxyexecutor.Options{SourceFormat: source.format}, method)
					if ojson.GetBytes(body, "output_config.format").Exists() {
						t.Fatalf("output_config.format sent to legacy model %q: %s", model, body)
					}
					if !strings.Contains(string(body), "conforms strictly") {
						t.Errorf("schema instruction missing for legacy model %q: %s", model, body)
					}
				})
			}
		}
	}
}

func TestClaudeExecutor_OpenAIStructuredOutputLocalCountAndPayloadFinal(t *testing.T) {
	const model = "claude-sonnet-5-5"
	const schema = `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`
	payload := []byte(`{"model":"` + model + `","input":[{"role":"user","content":[{"type":"input_text","text":"Suggest a recipe."}]}],"text":{"format":{"type":"json_schema","name":"recipe","strict":true,"schema":` + schema + `}}}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-structured"}}

	// Payload rules must see the built-in output_config.format and stay final.
	for _, filter := range []bool{false, true} {
		name := "override"
		if filter {
			name = "filter"
		}
		t.Run("upstream/"+name, func(t *testing.T) {
			models := []config.PayloadModelRule{{Name: model, Match: []map[string]any{{"output_config.format.type": "json_schema"}}}}
			cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: map[string]any{"output_config.format.type": "custom_override", "observed_output_format": true}}}}}
			if filter {
				cfg.Payload = config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: models, Params: []string{"output_config.format"}}}}
			}
			body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(cfg), auth, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, "Execute")
			if filter {
				if ojson.GetBytes(body, "output_config.format").Exists() {
					t.Fatalf("filtered output_config.format survived: %s", body)
				}
			} else if ojson.GetBytes(body, "output_config.format.type").String() != "custom_override" || !ojson.GetBytes(body, "observed_output_format").Bool() {
				t.Fatalf("override did not observe and supersede output_config.format: %s", body)
			}
		})
	}

	// Local CountTokens (custom gateway) counts output_config.format.schema without a description.
	localAuth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-structured", "base_url": "https://gateway.invalid"}}
	exec := NewClaudeExecutor(&config.Config{})
	plainPayload := []byte(`{"model":"` + model + `","input":[{"role":"user","content":[{"type":"input_text","text":"Suggest a recipe."}]}]}`)
	plainResp, errPlain := exec.CountTokens(context.Background(), localAuth, cliproxyexecutor.Request{Model: model, Payload: plainPayload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errPlain != nil {
		t.Fatalf("local CountTokens plain: %v", errPlain)
	}
	schemaResp, errCount := exec.CountTokens(context.Background(), localAuth, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errCount != nil {
		t.Fatalf("local CountTokens schema: %v", errCount)
	}
	plainTokens := ojson.GetBytes(plainResp.Payload, "input_tokens").Int()
	schemaTokens := ojson.GetBytes(schemaResp.Payload, "input_tokens").Int()
	if plainTokens <= 0 || schemaTokens <= plainTokens {
		t.Fatalf("expected schema local input_tokens > plain (%d > %d)", schemaTokens, plainTokens)
	}
}
