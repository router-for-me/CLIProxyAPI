package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRelayOpenAIToolResultImagesMergesIntoFollowingUserMessage(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}},
			{"id":"call_2","type":"function","function":{"name":"read","arguments":"{}"}}
		]},
		{"role":"tool","tool_call_id":"call_1","content":[
			{"type":"text","text":"Read image file [image/jpeg]"},
			{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,AA==","detail":"high"}}
		]},
		{"role":"tool","tool_call_id":"call_2","content":"second result"},
		{"role":"user","content":"what is in the image?"}
	]}`)

	got := RelayOpenAIToolResultImages(input)
	messages := gjson.GetBytes(got, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages length = %d, want 4; payload=%s", len(messages), got)
	}
	if messages[1].Get("role").String() != "tool" || messages[2].Get("role").String() != "tool" {
		t.Fatalf("parallel tool results were not kept contiguous: %s", got)
	}
	if content := messages[1].Get("content"); content.Type != gjson.String || content.String() != "Read image file [image/jpeg]" {
		t.Fatalf("tool content = %s, want text-only string", content.Raw)
	}

	content := messages[3].Get("content")
	if !content.IsArray() {
		t.Fatalf("user content is not an array: %s", content.Raw)
	}
	if text := content.Get("0.text").String(); text != openAIToolResultImageRelayNotice {
		t.Fatalf("relay notice = %q", text)
	}
	if imageURL := content.Get("1.image_url.url").String(); imageURL != "data:image/jpeg;base64,AA==" {
		t.Fatalf("relay image URL = %q", imageURL)
	}
	if text := content.Get("2.text").String(); text != "what is in the image?" {
		t.Fatalf("following user text = %q", text)
	}
}

func TestRelayOpenAIToolResultImagesAppendsUserRelayAtEnd(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":[
			{"type":"input_image","image_url":"data:image/png;base64,AA==","detail":"original"}
		]}
	]}`)

	got := RelayOpenAIToolResultImages(input)
	messages := gjson.GetBytes(got, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3; payload=%s", len(messages), got)
	}
	if text := messages[1].Get("content").String(); text != openAIToolResultImageRelayText {
		t.Fatalf("tool placeholder = %q", text)
	}
	if role := messages[2].Get("role").String(); role != "user" {
		t.Fatalf("relay role = %q, want user", role)
	}
	if imageURL := messages[2].Get("content.1.image_url.url").String(); imageURL != "data:image/png;base64,AA==" {
		t.Fatalf("relay image URL = %q", imageURL)
	}
	if detail := messages[2].Get("content.1.image_url.detail").String(); detail != "high" {
		t.Fatalf("relay image detail = %q, want high", detail)
	}
}

func TestRelayOpenAIToolResultImagesConvertsClaudeBase64Image(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}
		]}
	]}`)

	got := RelayOpenAIToolResultImages(input)
	messages := gjson.GetBytes(got, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3; payload=%s", len(messages), got)
	}
	if imageURL := messages[2].Get("content.1.image_url.url").String(); imageURL != "data:image/png;base64,AA==" {
		t.Fatalf("relay image URL = %q", imageURL)
	}
}

func TestRelayOpenAIToolResultImagesLeavesUnsupportedImageShapeUnchanged(t *testing.T) {
	input := []byte(`{"messages":[{"role":"tool","tool_call_id":"call_1","content":[{"type":"image","source":{"type":"file","file_id":"file_123"}}]}]}`)
	got := RelayOpenAIToolResultImages(input)
	if string(got) != string(input) {
		t.Fatalf("unsupported image payload changed:\n got: %s\nwant: %s", got, input)
	}
}

func TestRelayOpenAIToolResultImagesLeavesTextOnlyPayloadUnchanged(t *testing.T) {
	input := []byte(`{"messages":[{"role":"tool","tool_call_id":"call_1","content":"plain text"}]}`)
	got := RelayOpenAIToolResultImages(input)
	if string(got) != string(input) {
		t.Fatalf("text-only payload changed:\n got: %s\nwant: %s", got, input)
	}
	if len(got) > 0 && &got[0] != &input[0] {
		t.Fatal("text-only payload should return the original byte slice")
	}
}
