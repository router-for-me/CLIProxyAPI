package signature

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripInvalidClaudeThinkingBlocks_RemovesGPTEncryptedContent(t *testing.T) {
	input := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"codex reasoning","signature":"gAAAAABopenai-encrypted-content"},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("messages.0.content length = %d, want 1: %s", len(content), string(out))
	}
	if got := content[0].Get("text").String(); got != "Answer" {
		t.Fatalf("remaining content text = %q, want Answer", got)
	}
	if strings.Contains(string(out), "gAAAAABopenai-encrypted-content") || strings.Contains(string(out), "codex reasoning") {
		t.Fatalf("invalid thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocksAndEmptyMessages_DropsMessagesLeftEmpty(t *testing.T) {
	input := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"codex reasoning","signature":"gAAAAABopenai-encrypted-content"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	out := StripInvalidClaudeThinkingBlocksAndEmptyMessages(input)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1: %s", len(messages), string(out))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("remaining role = %q, want user", got)
	}
	if strings.Contains(string(out), "gAAAAABopenai-encrypted-content") || strings.Contains(string(out), "codex reasoning") {
		t.Fatalf("invalid thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_RemovesMalformedEPrefix(t *testing.T) {
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"bad","signature":"Ebad"},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1: %s", len(content), string(out))
	}
	if strings.Contains(string(out), "Ebad") || strings.Contains(string(out), "bad") {
		t.Fatalf("malformed E-prefix thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_Base64OnlyKeepsDecodableEPrefix(t *testing.T) {
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"bad","signature":"Ebad"},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input, ClaudeSignatureValidationOptions{Base64Only: true})
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2: %s", len(content), string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_Base64OnlyRemovesInvalidBase64(t *testing.T) {
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"bad","signature":"E!!!invalid!!!"},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input, ClaudeSignatureValidationOptions{Base64Only: true})
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1: %s", len(content), string(out))
	}
	if strings.Contains(string(out), "E!!!invalid!!!") || strings.Contains(string(out), "bad") {
		t.Fatalf("invalid-base64 thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_AllowsEmptySignatureEmptyTextPlaceholder(t *testing.T) {
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","text":"","signature":""},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input, ClaudeSignatureValidationOptions{
		Base64Only:                       true,
		AllowEmptySignatureWithEmptyText: true,
	})
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2: %s", len(content), string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_StrictRemovesMalformedClaudeTree(t *testing.T) {
	sig := base64.StdEncoding.EncodeToString([]byte{0x12, 0xFF, 0xFE, 0xFD})
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"bad","signature":"` + sig + `"},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input, ClaudeSignatureValidationOptions{Strict: true})
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1: %s", len(content), string(out))
	}
	if strings.Contains(string(out), sig) || strings.Contains(string(out), "bad") {
		t.Fatalf("strict-invalid thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_KeepsClaudeSignaturePrefixes(t *testing.T) {
	singleLayer := base64.StdEncoding.EncodeToString([]byte{0x12, 0x34})
	doubleLayer := base64.StdEncoding.EncodeToString([]byte(singleLayer))
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"one","signature":"` + singleLayer + `"},
			{"type":"thinking","thinking":"two","signature":"modelGroup#` + doubleLayer + `"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2: %s", len(content), string(out))
	}
}
