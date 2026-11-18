package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestPayloadReplaceGeminiFinalRoles(t *testing.T) {
	for _, role := range []string{"system", "developer"} {
		for _, stream := range []bool{false, true} {
			t.Run(role+map[bool]string{false: "/execute", true: "/stream"}[stream], func(t *testing.T) {
				cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
					Models: []config.PayloadModelRule{{Name: "*", Protocol: "gemini", FromProtocol: "responses"}},
					Target: config.PayloadReplaceTarget{Role: role, All: true}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
				}}}}
				executor := NewAIStudioExecutor(cfg, "aistudio", nil)
				req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"instructions":"hero root","input":[{"role":"system","content":"hero system"},{"role":"developer","content":"hero developer"},{"role":"user","content":"hero question"},{"role":"assistant","content":[{"type":"output_text","text":"hero answer"}]},{"role":"user","content":"hero followup"}]}`)}
				body, _, errTranslate := executor.translateRequest(context.Background(), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, stream)
				if errTranslate != nil {
					t.Fatal(errTranslate)
				}
				for _, part := range ojson.GetBytes(body, "systemInstruction.parts").Array() {
					if strings.Contains(part.Get("text").String(), "hero") && strings.Contains(part.Get("text").String(), "hero!") != (role == "system") {
						t.Fatalf("system selection must use merged final role: %s", body)
					}
				}
				for _, text := range []string{"root", "system", "developer"} {
					want := "hero " + text
					if role == "system" {
						want = "hero! " + text
					}
					if !strings.Contains(string(body), want) {
						t.Fatalf("missing %q: %s", want, body)
					}
				}
				wantAssistant := "hero answer"
				foundAssistant := false
				for _, content := range ojson.GetBytes(body, "contents").Array() {
					for _, part := range content.Get("parts").Array() {
						if content.Get("role").String() == "model" && part.Get("text").String() == wantAssistant {
							foundAssistant = true
						}
						if content.Get("role").String() == "user" && strings.Contains(part.Get("text").String(), "hero!") {
							t.Fatalf("user text was modified: %s", body)
						}
					}
				}
				if !foundAssistant || strings.Contains(string(body), "hero!!") {
					t.Fatalf("assistant text changed or replacement repeated: %s", body)
				}
			})
		}
	}
}

func TestPayloadReplaceClaudeFinalRoles(t *testing.T) {
	for _, role := range []string{"system", "developer"} {
		for _, stream := range []bool{false, true} {
			t.Run(role+map[bool]string{false: "/execute", true: "/stream"}[stream], func(t *testing.T) {
				cfg := &config.Config{Payload: config.PayloadConfig{
					Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"system": "hero system"}}},
					Replace:  []config.PayloadReplaceRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "claude"}}, Target: config.PayloadReplaceTarget{Role: role, All: true}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}}}},
				}}
				body := executeClaudeContextManagementRequest(t, cfg, []byte(`{"model":"claude-opus-5","system":"original system","messages":[{"role":"user","content":"hero user"},{"role":"assistant","content":"hero assistant"},{"role":"user","content":"hero followup"}]}`), stream)
				wantSystem, wantAssistant := "hero system", "hero assistant"
				if role == "system" {
					wantSystem = "hero! system"
				}
				if ojson.GetBytes(body, "system").String() != wantSystem || ojson.GetBytes(body, `messages.#(role=="assistant").content`).String() != wantAssistant {
					t.Fatalf("final role replacement must follow overrides exactly once: %s", body)
				}
			})
		}
	}
}
