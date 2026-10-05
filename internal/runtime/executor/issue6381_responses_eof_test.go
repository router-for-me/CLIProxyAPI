package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type issue6381RoundTripperFunc func(*http.Request) (*http.Response, error)

func (fn issue6381RoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type issue6381ReadError struct {
	chunks [][]byte
	index  int
}

func (r *issue6381ReadError) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, errors.New("synthetic upstream read error")
	}
	chunk := r.chunks[r.index]
	if len(chunk) > len(p) {
		copy(p, chunk[:len(p)])
		r.chunks[r.index] = chunk[len(p):]
		return len(p), nil
	}
	r.index++
	return copy(p, chunk), nil
}

func TestOpenAICompatExecutorResponsesEOFAfterFinishReasonCompletesStream(t *testing.T) {
	reply := strings.Join([]string{
		`data: {"id":"chatcmpl-eof","object":"chat.completion.chunk","created":1773896263,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-eof","object":"chat.completion.chunk","created":1773896263,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"chatcmpl-eof","object":"chat.completion.chunk","created":1773896263,"model":"test","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		"",
	}, "\n")
	exec, auth, _ := newApplyPatchCompatTestExecutor(t, reply)
	request := []byte(`{"model":"test","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":true}`)

	result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{
		SourceFormat:    translator.FormatOpenAIResponse,
		ResponseFormat:  translator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}

	var output bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error after a completed finish_reason: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if got := strings.Count(output.String(), `"type":"response.completed"`); got != 1 {
		t.Fatalf("response.completed count = %d, want 1; output = %q", got, output.String())
	}
	if !strings.Contains(output.String(), `"input_tokens":3`) || !strings.Contains(output.String(), `"output_tokens":1`) {
		t.Fatalf("late usage was not preserved in response.completed: %q", output.String())
	}
}

func TestOpenAICompatExecutorResponsesEOFAfterApplyPatchFinishReasonCompletesStream(t *testing.T) {
	first, last := applyPatchTestFrames("chat", "apply_patch")
	last = strings.Replace(last, "\ndata: [DONE]\n\n", "\n\n", 1)
	exec, auth, _ := newApplyPatchCompatTestExecutor(t, first+last)
	request := applyPatchTestRequest()

	result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{
		SourceFormat:    translator.FormatOpenAIResponse,
		ResponseFormat:  translator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}

	var output bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected apply_patch stream error after a completed finish_reason: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if got := strings.Count(output.String(), `"type":"response.completed"`); got != 1 {
		t.Fatalf("response.completed count = %d, want 1; output = %q", got, output.String())
	}
	if strings.Contains(output.String(), `"type":"response.failed"`) {
		t.Fatalf("completed apply_patch stream emitted response.failed: %q", output.String())
	}
}

func TestOpenAICompatExecutorResponsesScannerErrorDoesNotSynthesizeCompletion(t *testing.T) {
	reply := strings.Join([]string{
		`data: {"id":"chatcmpl-reset","object":"chat.completion.chunk","created":1773896263,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-reset","object":"chat.completion.chunk","created":1773896263,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
	}, "\n")
	exec := NewOpenAICompatExecutor("custom-compat", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "custom-compat", Attributes: map[string]string{"base_url": "http://responses.test/v1", "api_key": "test"}}
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", issue6381RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(&issue6381ReadError{chunks: [][]byte{
				[]byte(strings.Split(reply, "\n\n")[0] + "\n\n"),
				[]byte(strings.Split(reply, "\n\n")[1] + "\n\n"),
			}}),
			Request: req,
		}, nil
	}))
	request := []byte(`{"model":"test","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":true}`)

	result, errExecuteStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{
		SourceFormat:    translator.FormatOpenAIResponse,
		ResponseFormat:  translator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}

	var output bytes.Buffer
	var streamFailed bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamFailed = true
			continue
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), `"type":"response.output_text.delta"`) {
		t.Fatalf("scanner-error stream did not deliver its valid content frame: %q", output.String())
	}
	if !streamFailed {
		t.Fatalf("scanner error was treated as a successful stream: %q", output.String())
	}
	if strings.Contains(output.String(), `"type":"response.completed"`) {
		t.Fatalf("scanner-error stream emitted response.completed: %q", output.String())
	}
}

func TestOpenAICompatExecutorResponsesEOFBeforeFinishReasonFailsStream(t *testing.T) {
	reply := "data: {\"id\":\"chatcmpl-truncated\",\"object\":\"chat.completion.chunk\",\"created\":1773896263,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	exec, auth, _ := newApplyPatchCompatTestExecutor(t, reply)
	request := []byte(`{"model":"test","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":true}`)

	result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{
		SourceFormat:    translator.FormatOpenAIResponse,
		ResponseFormat:  translator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}

	var output bytes.Buffer
	var streamFailed bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamFailed = true
			continue
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), `"type":"response.output_text.delta"`) {
		t.Fatalf("truncated Responses stream did not deliver its valid content frame: %q", output.String())
	}
	if !streamFailed {
		t.Fatalf("truncated Responses stream completed successfully: %q", output.String())
	}
	if strings.Contains(output.String(), `"type":"response.completed"`) {
		t.Fatalf("truncated Responses stream emitted response.completed: %q", output.String())
	}
}
