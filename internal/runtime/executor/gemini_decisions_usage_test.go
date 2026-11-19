package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestGeminiDecisionsRepeatedCallsDoNotTriggerZeroOutputCooldown guards the R7
// contract: Decisions responses always carry zero output tokens and no candidates
// array, so consecutive calls must publish exactly one usage record each and never
// surface a transient-zone zero-output error that would cool the credential.
func TestGeminiDecisionsRepeatedCallsDoNotTriggerZeroOutputCooldown(t *testing.T) {
	const upstreamBody = `{"answers":{"q":{"type":"noul","noul":0.42,"confidence":0.58}},"usage":{"total_input_tokens":17,"total_output_tokens":0,"total_tokens":17}}`
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1beta/decisions" {
			t.Errorf("upstream path = %q, want /v1beta/decisions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer server.Close()

	plugin := &recordCaptureUsagePlugin{records: make(chan usage.Record, 32)}
	usage.RegisterPlugin(plugin)
	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "decisions-cooldown", Provider: "gemini", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:    "gemini-decision-1",
		Payload:  []byte(`{"input":"hi","questions":{"q":{"type":"noul","instructions":"ok?"}}}`),
		Metadata: map[string]any{cliproxyexecutor.RequestActionMetadataKey: "decisions"},
	}

	for i := 0; i < 3; i++ {
		resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
		if err != nil {
			t.Fatalf("call %d: Execute() error = %v", i, err)
		}
		if string(resp.Payload) != upstreamBody {
			t.Fatalf("call %d: payload = %s, want upstream body", i, resp.Payload)
		}
		record := waitForRecord(t, plugin.records)
		if record.Detail.OutputTokens != 0 {
			t.Fatalf("call %d: output tokens = %d, want 0", i, record.Detail.OutputTokens)
		}
		if record.Detail.InputTokens != 17 {
			t.Fatalf("call %d: input tokens = %d, want 17", i, record.Detail.InputTokens)
		}
		if record.Failed {
			t.Fatalf("call %d: usage record marked failed", i)
		}
	}
	if calls != 3 {
		t.Fatalf("upstream calls = %d, want 3", calls)
	}
	assertNoExtraRecord(t, plugin.records)
}

// TestGeminiDecisionsPublishesExactlyOneRecordPerCall ensures the shared native path
// does not double-publish (Publish + EnsurePublished must collapse to one record).
func TestGeminiDecisionsPublishesExactlyOneRecordPerCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"answers":{},"usage":{"total_input_tokens":5,"total_output_tokens":0,"total_tokens":5}}`)
	}))
	defer server.Close()

	plugin := &recordCaptureUsagePlugin{records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)
	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "decisions-single-record", Provider: "gemini", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:    "gemini-decision-1",
		Payload:  []byte(`{"input":"hi","questions":{"q":{"type":"noul","instructions":"ok?"}}}`),
		Metadata: map[string]any{cliproxyexecutor.RequestActionMetadataKey: "decisions"},
	}
	if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	_ = waitForRecord(t, plugin.records)
	assertNoExtraRecord(t, plugin.records)
}

// TestGeminiDecisionsMissingUsageStillPublishesOneRecord covers an upstream response
// with no usage object at all.
func TestGeminiDecisionsMissingUsageStillPublishesOneRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"answers":{"q":{"type":"noul","noul":0.5,"confidence":0.5}}}`)
	}))
	defer server.Close()

	plugin := &recordCaptureUsagePlugin{records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)
	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "decisions-no-usage", Provider: "gemini", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:    "gemini-decision-1",
		Payload:  []byte(`{"input":"hi","questions":{"q":{"type":"noul","instructions":"ok?"}}}`),
		Metadata: map[string]any{cliproxyexecutor.RequestActionMetadataKey: "decisions"},
	}
	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !ojson.GetBytes(resp.Payload, "answers").Exists() {
		t.Fatalf("payload = %s, want upstream answers preserved", resp.Payload)
	}
	record := waitForRecord(t, plugin.records)
	if record.Detail.TotalTokens != 0 || record.Detail.OutputTokens != 0 {
		t.Fatalf("detail = %+v, want zero-token record", record.Detail)
	}
}

// TestGeminiDecisionsBodyModelNormalizedOnWire confirms the wire body carries the bare
// model ID even when the caller sends the models/ resource form.
func TestGeminiDecisionsBodyModelNormalizedOnWire(t *testing.T) {
	var wireBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		wireBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"answers":{},"usage":{"total_input_tokens":1,"total_output_tokens":0,"total_tokens":1}}`)
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:    "gemini-decision-1",
		Payload:  []byte(`{"model":"gemini-decision-1","input":"hi","questions":{"q":{"type":"noul","instructions":"ok?"}}}`),
		Metadata: map[string]any{cliproxyexecutor.RequestActionMetadataKey: "decisions"},
	}
	if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := ojson.GetBytes(wireBody, "model").String(); got != "gemini-decision-1" {
		t.Fatalf("wire model = %q, want bare ID", got)
	}
	if strings.Contains(string(wireBody), "models/gemini-decision-1") {
		t.Fatalf("wire body must not contain a models/ resource name: %s", wireBody)
	}
}

type recordCaptureUsagePlugin struct {
	records chan usage.Record
}

func (p *recordCaptureUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

// waitForRecord blocks until one usage record arrives; publication is asynchronous.
func waitForRecord(t *testing.T, records <-chan usage.Record) usage.Record {
	t.Helper()
	select {
	case record := <-records:
		return record
	case <-time.After(2 * time.Second):
		t.Fatal("no usage record published")
		return usage.Record{}
	}
}

// assertNoExtraRecord fails when a second record is published for the same request.
func assertNoExtraRecord(t *testing.T, records <-chan usage.Record) {
	t.Helper()
	select {
	case extra := <-records:
		t.Fatalf("unexpected extra usage record: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}
