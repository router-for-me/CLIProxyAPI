package responses

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_VideoInput(t *testing.T) {
	tests := []struct {
		name string
		part string
		want string
	}{
		{
			name: "remote URL",
			part: `{"type":"input_video","video_url":"https://example.com/clip.mp4?part=1&name=a%20b"}`,
			want: `{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4?part=1&name=a%20b"}}`,
		},
		{
			name: "base64 data URL",
			part: `{"type":"input_video","video_url":"data:video/mp4;base64,AAECAwQ="}`,
			want: `{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAECAwQ="}}`,
		},
		{
			name: "processing mode",
			part: `{"type":"input_video","video_url":"https://example.com/clip.webm","processing":"agentic"}`,
			want: `{"type":"video_url","video_url":{"url":"https://example.com/clip.webm","processing":"agentic"}}`,
		},
		{
			name: "object video URL",
			part: `{"type":"input_video","video_url":{"url":"https://example.com/clip.mp4","processing":"static"}}`,
			want: `{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4","processing":"static"}}`,
		},
		{
			name: "chat video part in Responses content",
			part: `{"type":"video_url","video_url":{"url":"data:video/webm;base64,AAECAwQ=","processing":"static"}}`,
			want: `{"type":"video_url","video_url":{"url":"data:video/webm;base64,AAECAwQ=","processing":"static"}}`,
		},
		{
			name: "top-level processing overrides object processing",
			part: `{"type":"input_video","video_url":{"url":"https://example.com/clip.mp4","processing":"static"},"processing":"agentic"}`,
			want: `{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4","processing":"agentic"}}`,
		},
		{
			name: "missing URL remains a video for upstream validation",
			part: `{"type":"input_video"}`,
			want: `{"type":"video_url","video_url":{}}`,
		},
		{
			name: "invalid URL is not coerced to a string",
			part: `{"type":"input_video","video_url":123}`,
			want: `{"type":"video_url","video_url":{"url":123}}`,
		},
	}

	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tt.name, stream), func(t *testing.T) {
				raw := []byte(`{"input":[{"role":"user","content":[` + tt.part + `]}]}`)
				out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("video-model", raw, stream)
				content := gjson.GetBytes(out, "messages.0.content").Array()
				if len(content) != 1 {
					t.Fatalf("video content was lost: got %d parts, want 1; output=%s", len(content), out)
				}
				var want any
				if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
					t.Fatal(err)
				}
				if got := content[0].Value(); !reflect.DeepEqual(got, want) {
					t.Fatalf("video part = %s, want %s", content[0].Raw, tt.want)
				}
			})
		}
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MixedVideoInputOrder(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"Compare these clips and this image."},
		{"type":"input_video","video_url":"https://example.com/first.mp4"},
		{"type":"input_image","image_url":"https://example.com/frame.png","detail":"low"},
		{"type":"input_video","video_url":"data:video/mp4;base64,AAECAwQ=","processing":"static"},
		{"type":"input_text","text":"Describe the differences."}
	]}]}`)
	wantJSON := `[
		{"type":"text","text":"Compare these clips and this image."},
		{"type":"video_url","video_url":{"url":"https://example.com/first.mp4"}},
		{"type":"image_url","image_url":{"url":"https://example.com/frame.png","detail":"low"}},
		{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAECAwQ=","processing":"static"}},
		{"type":"text","text":"Describe the differences."}
	]`
	var want any
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatal(err)
	}
	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("video-model", raw, false)
	if got := gjson.GetBytes(out, "messages.0.content").Value(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed content order or media changed: output=%s", out)
	}
}
