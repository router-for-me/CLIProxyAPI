package claudemaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProbeMakesOneSmallNonStreamingRequest(t *testing.T) {
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || r.URL.RawQuery != "" {
			t.Fatal("unexpected probe route")
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("unexpected probe headers")
		}
		var body struct {
			Model     string `json:"model"`
			Stream    bool   `json:"stream"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "selected-model" || body.Stream || body.MaxTokens != 32 || body.Tools != nil || len(body.Messages) != 1 || body.Messages[0].Role != "user" || !strings.Contains(body.Messages[0].Content, probeMarker) {
			t.Fatalf("unexpected probe parameters: %#v", body)
		}
		_, _ = io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"INFERENCE_OK"}]}`)
	})
	result, err := probeHandler(t.Context(), handler, "selected-model")
	if err != nil || result.Status != http.StatusOK || !result.Matched || calls != 1 || result.Stage != BackendErrorNone {
		t.Fatalf("probe result=%+v err=%v calls=%d", result, err, calls)
	}
}

func TestProbeDoesNotExposeResponseContent(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			result, err := probeHandler(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("X-Account", "PRIVATE-CANARY")
				w.Header().Set("X-Backend-Error-Stage", "PRIVATE-CANARY")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"PRIVATE-CANARY"}`)
			}), "model")
			if err != nil || result.Status != status || result.Matched || calls != 1 || strings.Contains(fmt.Sprintf("%+v %v", result, err), "PRIVATE-CANARY") {
				t.Fatalf("unsafe probe failure result: %+v %v", result, err)
			}
		})
	}
}

func TestProbeUsesOnlyInProcessClassifiedErrorStage(t *testing.T) {
	capture := &backendCapture{err: errors.New("finalize Claude CCH: PRIVATE-CANARY")}
	handler, opts := backendTestHandler(t, t.Context(), capture)
	result, err := probeHandler(t.Context(), handler, opts.Model)
	if err != nil || result.Status != http.StatusInternalServerError || result.Matched || result.Stage != BackendErrorSigning || capture.calls != 1 {
		t.Fatalf("probe diagnostics: %+v err=%v calls=%d", result, err, capture.calls)
	}
	if strings.Contains(fmt.Sprintf("%+v %v", result, err), "PRIVATE-CANARY") {
		t.Fatal("probe returned private provider diagnostics")
	}
}

func TestProbeMatchingRequiresPlainMarkerMessage(t *testing.T) {
	for _, tc := range []struct {
		body    string
		match   bool
		failure bool
	}{
		{`{"type":"message","content":[{"type":"text","text":"  INFERENCE_OK\n"}]}`, true, false},
		{`{"type":"message","content":[{"type":"text","text":"INFERENCE_"},{"type":"text","text":"OK"}]}`, true, false},
		{`{"type":"message","content":[{"type":"text","text":"prefix INFERENCE_OK"}]}`, false, false},
		{`{"type":"message","content":[{"type":"tool_use","text":"INFERENCE_OK"}]}`, false, false},
		{`{"type":"error","content":[{"type":"text","text":"INFERENCE_OK"}]}`, false, false},
		{`{"type":"message","content":[]}`, false, false},
		{`PRIVATE-CANARY-invalid-json`, false, true},
	} {
		result, err := probeHandler(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }), "model")
		if result.Matched != tc.match || (err != nil) != tc.failure || strings.Contains(fmt.Sprint(err), "PRIVATE-CANARY") {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestProbeCaptureIsBounded(t *testing.T) {
	writer := &probeResponseWriter{header: make(http.Header)}
	_, _ = writer.Write([]byte(strings.Repeat("x", probeMaxResponseBytes)))
	_, _ = writer.Write([]byte("overflow"))
	if writer.body.Len() != probeMaxResponseBytes || !writer.oversized {
		t.Fatal("probe response capture is not bounded")
	}
	result, err := probeHandler(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("s", probeMaxResponseBytes+1))
	}), "model")
	if err == nil || result.Matched || result.Status != http.StatusOK {
		t.Fatal("oversized response was accepted")
	}
}

func TestProbeCancellationDoesNotStartAnotherRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	_, err := probeHandler(ctx, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }), "model")
	if err != context.Canceled || calls != 0 {
		t.Fatal("canceled probe started inference")
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	result, err := probeHandler(ctx, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}), "model")
	if err != context.Canceled || calls != 1 || result.Status != http.StatusServiceUnavailable {
		t.Fatal("cancellation was not preserved")
	}
}
