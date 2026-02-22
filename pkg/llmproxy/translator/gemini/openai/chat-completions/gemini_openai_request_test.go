package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGeminiRemovesUnsupportedGoogleSearchFields(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{"google_search":{"defer_loading":true,"deferLoading":true,"lat":"1"}}
		]
	}`)

	got := ConvertOpenAIRequestToGemini("gemini-2.5-pro", input, false)
	res := gjson.ParseBytes(got)
	tool := res.Get("tools.0.googleSearch")
	if !tool.Exists() {
		t.Fatalf("expected googleSearch tool to exist")
	}
	if tool.Get("defer_loading").Exists() {
		t.Fatalf("expected defer_loading to be removed")
	}
	if tool.Get("deferLoading").Exists() {
		t.Fatalf("expected deferLoading to be removed")
	}
	if tool.Get("lat").String() != "1" {
		t.Fatalf("expected non-problematic fields to remain")
	}
}

func TestConvertOpenAIRequestToGeminiMapsVideoConfigAndModalities(t *testing.T) {
	input := []byte(`{
		"model":"veo-3.1-generate-preview",
		"messages":[{"role":"user","content":"make a video"}],
		"modalities":["video","text"],
		"video_config":{
			"duration_seconds":"8",
			"aspect_ratio":"16:9",
			"resolution":"720p",
			"negative_prompt":"blurry"
		}
	}`)

	got := ConvertOpenAIRequestToGemini("veo-3.1-generate-preview", input, false)
	res := gjson.ParseBytes(got)
	if !res.Get("generationConfig.responseModalities").IsArray() {
		t.Fatalf("expected generationConfig.responseModalities array")
	}
	if res.Get("generationConfig.responseModalities.0").String() != "VIDEO" {
		t.Fatalf("expected first modality VIDEO, got %q", res.Get("generationConfig.responseModalities.0").String())
	}
	if res.Get("generationConfig.videoConfig.durationSeconds").String() != "8" {
		t.Fatalf("expected durationSeconds=8, got %q", res.Get("generationConfig.videoConfig.durationSeconds").String())
	}
	if res.Get("generationConfig.videoConfig.aspectRatio").String() != "16:9" {
		t.Fatalf("expected aspectRatio=16:9, got %q", res.Get("generationConfig.videoConfig.aspectRatio").String())
	}
	if res.Get("generationConfig.videoConfig.resolution").String() != "720p" {
		t.Fatalf("expected resolution=720p, got %q", res.Get("generationConfig.videoConfig.resolution").String())
	}
	if res.Get("generationConfig.videoConfig.negativePrompt").String() != "blurry" {
		t.Fatalf("expected negativePrompt=blurry, got %q", res.Get("generationConfig.videoConfig.negativePrompt").String())
	}
}

func TestConvertOpenAIRequestToGeminiSkipsEmptyAssistantMessage(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":""},
			{"role":"user","content":"second"}
		]
	}`)

	got := ConvertOpenAIRequestToGemini("gemini-2.5-pro", input, false)
	res := gjson.ParseBytes(got)
	if count := len(res.Get("contents").Array()); count != 2 {
		t.Fatalf("expected 2 content entries (assistant empty skipped), got %d", count)
	}
	if res.Get("contents.0.role").String() != "user" || res.Get("contents.1.role").String() != "user" {
		t.Fatalf("expected only user entries, got %s", res.Get("contents").Raw)
	}
}
