package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type zaiToolImageRoundTripper func(*http.Request) (*http.Response, error)

func (f zaiToolImageRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestCodexExecutorZAIToolImagesAreRelocatedForResponsesPaths(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			payload := zaiToolImageExecutorPayload()
			var captured []byte
			transport := zaiToolImageRoundTripper(func(req *http.Request) (*http.Response, error) {
				var err error
				captured, err = io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}` + "\n\n"))}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.z.ai/api/v1/", "api_key": "test"}}
			req := cliproxyexecutor.Request{Model: "glm-5.3-flash", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), OriginalRequest: payload, Stream: stream}
			if stream {
				result, err := executor.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			} else if _, err := executor.Execute(ctx, auth, req, opts); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			assertZAIToolImageCapture(t, captured)
		})
	}
}

func zaiToolImageExecutorPayload() []byte {
	parts := make([]string, 0, 15)
	parts = append(parts, `{"type":"input_text","text":"retain this tool text"}`)
	for index := range 14 {
		parts = append(parts, `{"type":"input_image","image_url":`+strconv.Quote(zaiTestImageURL(index))+`,"detail":"original"}`)
	}
	return []byte(`{"model":"glm-5.3-flash","input":[{"type":"function_call_output","call_id":"call_images","output":[` + strings.Join(parts, ",") + `]}]}`)
}

func zaiTestImageURL(index int) string {
	return "data:image/png;base64," + strings.Repeat(string(rune('A'+index)), 2*1024*1024)
}

func assertZAIToolImageCapture(t *testing.T, body []byte) {
	t.Helper()
	input := gjson.GetBytes(body, "input")
	if got := len(input.Array()); got != 2 {
		t.Fatalf("upstream input items = %d, want tool output plus visual message", got)
	}
	if got := input.Get("0.type").String(); got != "function_call_output" {
		t.Fatalf("input.0.type = %q, body starts %s", got, body[:min(len(body), 512)])
	}
	if got := input.Get("0.call_id").String(); got != "call_images" {
		t.Fatalf("call_id = %q", got)
	}
	if got := input.Get("0.output.0.text").String(); got != "retain this tool text" {
		t.Fatalf("tool text = %q", got)
	}
	if got := input.Get("0.output.1.text").String(); got != "Tool-produced image appears in the following visual context." {
		t.Fatalf("tool image reference = %q", got)
	}
	if got := input.Get("1.type").String(); got != "message" || input.Get("1.role").String() != "user" {
		t.Fatalf("relocated message is not immediately after tool output: %s", body[:min(len(body), 1024)])
	}
	if got := input.Get("1.content.0.text").String(); got != "Tool-produced image follows as visual context; treat it as tool output." {
		t.Fatalf("visual marker = %q", got)
	}
	if got := input.Get("1.content.#").Int(); got != 15 {
		t.Fatalf("visual content parts = %d, want marker plus 14 images", got)
	}
	for index := 1; index <= 14; index++ {
		imageURL := zaiTestImageURL(index - 1)
		if got := input.Get("1.content." + strconv.Itoa(index) + ".image_url").String(); got != imageURL {
			t.Fatalf("relocated content %d image bytes changed", index)
		}
		if got := bytes.Count(body, []byte(imageURL)); got != 1 {
			t.Fatalf("image %d appears %d times, want 1", index, got)
		}
		if got := input.Get("1.content." + strconv.Itoa(index) + ".type").String(); got != "input_image" {
			t.Fatalf("relocated content %d type = %q", index, got)
		}
		if got := input.Get("1.content." + strconv.Itoa(index) + ".detail").String(); got != "original" {
			t.Fatalf("relocated content %d detail = %q", index, got)
		}
	}
	if got := bytes.Count(body, []byte("data:image/png;base64,")); got != 14 {
		t.Fatalf("image payload occurrences = %d, want 14", got)
	}
}
