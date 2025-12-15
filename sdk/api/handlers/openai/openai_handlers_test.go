package openai

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

func TestPrependAssistantMessage_PrependsForMessagesArray(t *testing.T) {
	in := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)

	out := prependAssistantMessage(in)
	if bytes.Equal(out, in) {
		t.Fatalf("expected payload to change")
	}

	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages[0].role = %q, want %q", got, "assistant")
	}
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "You are helpful assistant" {
		t.Fatalf("messages[0].content = %q, want %q", got, "You are helpful assistant")
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "user" {
		t.Fatalf("messages[1].role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != "hi" {
		t.Fatalf("messages[1].content = %q, want %q", got, "hi")
	}
	if got := gjson.GetBytes(out, "model").String(); got != "gpt-test" {
		t.Fatalf("model = %q, want %q", got, "gpt-test")
	}

	out2 := prependAssistantMessage(out)
	if got, want := len(gjson.GetBytes(out2, "messages").Array()), 2; got != want {
		t.Fatalf("messages length after second prepend = %d, want %d", got, want)
	}
}

func TestPrependAssistantMessage_IdempotentWhenAssistantFirst(t *testing.T) {
	in := []byte(`{"messages":[{"role":"assistant","content":"already"},{"role":"user","content":"hi"}]}`)
	out := prependAssistantMessage(in)
	if !bytes.Equal(out, in) {
		t.Fatalf("expected payload to remain unchanged when assistant is already first")
	}
}

func TestPrependAssistantMessage_NoMessages_NoChange(t *testing.T) {
	in := []byte(`{"model":"gpt-test"}`)
	out := prependAssistantMessage(in)
	if !bytes.Equal(out, in) {
		t.Fatalf("expected payload to remain unchanged when messages is missing")
	}
}

func TestApplyCopilotUnlimitedModeIfEnabled_Guards(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)

	var nilHandler *OpenAIAPIHandler
	if out := nilHandler.applyCopilotUnlimitedModeIfEnabled(in); !bytes.Equal(out, in) {
		t.Fatalf("expected nil handler to return original payload")
	}

	disabled := &OpenAIAPIHandler{
		BaseAPIHandler: &handlers.BaseAPIHandler{
			Cfg: &config.SDKConfig{CopilotUnlimitedMode: false},
		},
	}
	if out := disabled.applyCopilotUnlimitedModeIfEnabled(in); !bytes.Equal(out, in) {
		t.Fatalf("expected disabled mode to return original payload")
	}

	enabled := &OpenAIAPIHandler{
		BaseAPIHandler: &handlers.BaseAPIHandler{
			Cfg: &config.SDKConfig{CopilotUnlimitedMode: true},
		},
	}
	out := enabled.applyCopilotUnlimitedModeIfEnabled(in)
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages[0].role = %q, want %q", got, "assistant")
	}
}
