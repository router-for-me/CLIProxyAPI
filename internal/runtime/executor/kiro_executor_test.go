package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildKiroPayloadIncludesProfileAndMessages(t *testing.T) {
	raw := []byte(`{
		"model":"auto-kiro",
		"messages":[
			{"role":"system","content":"be concise"},
			{"role":"user","content":"hello"}
		]
	}`)

	payload, err := buildKiroPayload(raw, "auto", "arn:aws:codewhisperer:us-east-1:123:profile/abc")
	if err != nil {
		t.Fatalf("buildKiroPayload() error = %v", err)
	}

	if got := gjson.GetBytes(payload, "profileArn").String(); got == "" {
		t.Fatal("profileArn missing")
	}
	content := gjson.GetBytes(payload, "conversationState.currentMessage.userInputMessage.content").String()
	if content != "be concise\n\nhello" {
		t.Fatalf("current content = %q", content)
	}
	if got := gjson.GetBytes(payload, "conversationState.currentMessage.userInputMessage.modelId").String(); got != "auto" {
		t.Fatalf("modelId = %q, want auto", got)
	}
}

func TestKiroEventParserExtractsContent(t *testing.T) {
	parser := newKiroEventParser()
	got := parser.feed([]byte("\x00event{\"content\":\"hel\"}\x00{\"content\":\"lo\"}"))
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (%v)", len(got), got)
	}
	if got[0] != "hel" || got[1] != "lo" {
		t.Fatalf("unexpected chunks: %v", got)
	}
}
