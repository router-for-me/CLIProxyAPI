package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

func TestJSONRequestLogging(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")

	reqHeaders := map[string][]string{
		"Authorization": {"Bearer secret-token"},
		"User-Agent":    {"test-agent"},
	}
	respHeaders := map[string][]string{
		"Content-Type": {"application/json"},
	}
	reqBody := []byte(`{"model":"gpt-4","max_tokens":100}`)
	respBody := []byte(`{"id":"chatcmpl-123","object":"chat.completion"}`)

	reqTime := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	respTime := reqTime.Add(200 * time.Millisecond)

	err := logger.LogRequest(
		"/v1/chat/completions",
		"POST",
		reqHeaders,
		reqBody,
		200,
		respHeaders,
		respBody,
		nil,
		nil,
		nil,
		nil,
		nil,
		"req-json-123",
		reqTime,
		respTime,
	)
	if err != nil {
		t.Fatalf("LogRequest failed: %v", err)
	}

	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("failed to read temp log dir: %v", err)
	}

	if len(files) == 0 {
		t.Fatalf("expected at least 1 log file, found 0")
	}

	logPath := filepath.Join(tempDir, files[0].Name())
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	var entry jsonLogPayload
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal NDJSON entry: %v, raw data: %s", err, string(data))
	}

	if entry.URL != "/v1/chat/completions" {
		t.Errorf("expected URL /v1/chat/completions, got %s", entry.URL)
	}
	if entry.Method != "POST" {
		t.Errorf("expected Method POST, got %s", entry.Method)
	}

	if entry.Headers["Authorization"][0] != "Bearer s***n" && entry.Headers["Authorization"][0] != "Bearer s***" {
		// Masked check
		if entry.Headers["Authorization"][0] == "Bearer secret-token" {
			t.Errorf("Authorization header was not masked")
		}
	}

	var reqBodyObj map[string]interface{}
	if err := json.Unmarshal(entry.RequestBody, &reqBodyObj); err != nil {
		t.Fatalf("failed to unmarshal request_body JSON: %v", err)
	}
	if reqBodyObj["model"] != "gpt-4" {
		t.Errorf("expected model gpt-4, got %v", reqBodyObj["model"])
	}

	if entry.Response == nil {
		t.Fatalf("expected response object to be non-nil")
	}
	if entry.Response.Status != 200 {
		t.Errorf("expected status 200, got %d", entry.Response.Status)
	}
}

func TestJSONStreamingRequestLogging(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")

	writer, err := logger.LogStreamingRequest(
		"/v1/chat/completions",
		"POST",
		map[string][]string{"User-Agent": {"test-agent"}},
		[]byte(`{"model":"gpt-4","stream":true}`),
		"req-stream-123",
	)
	if err != nil {
		t.Fatalf("LogStreamingRequest failed: %v", err)
	}

	_ = writer.WriteStatus(200, map[string][]string{"Content-Type": {"text/event-stream"}})
	writer.WriteChunkAsync([]byte("data: {\"choices\":[]}\n\n"))
	_ = writer.WriteAPIRequest([]byte(`{"upstream":"req"}`))
	_ = writer.WriteAPIResponse([]byte(`{"upstream":"resp"}`))

	time.Sleep(50 * time.Millisecond)

	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	files, err := os.ReadDir(tempDir)
	if err != nil || len(files) == 0 {
		t.Fatalf("expected log file in tempDir")
	}

	logPath := filepath.Join(tempDir, files[0].Name())
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	var entry jsonLogPayload
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal NDJSON streaming entry: %v, raw data: %s", err, string(data))
	}

	if entry.URL != "/v1/chat/completions" {
		t.Errorf("expected URL /v1/chat/completions, got %s", entry.URL)
	}
	if entry.APIRequest == nil && entry.APIRequestRaw == "" {
		t.Errorf("expected non-empty APIRequest")
	}
	if entry.APIResponse == nil && entry.APIResponseRaw == "" {
		t.Errorf("expected non-empty APIResponse")
	}
}

func TestJSONRequestLoggingSerializesErrorsAndMasksAddonHeaders(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")

	err := logger.LogRequest(
		"/v1/chat/completions",
		"POST",
		nil,
		[]byte(`{"model":"gpt-4"}`),
		502,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		[]*interfaces.ErrorMessage{{
			StatusCode: http.StatusBadGateway,
			Error:      errors.New("upstream connection failed"),
			Addon: http.Header{
				"Authorization": {"Bearer secret-token"},
			},
		}},
		"req-error-123",
		time.Now(),
		time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequest failed: %v", err)
	}

	files, err := os.ReadDir(tempDir)
	if err != nil || len(files) == 0 {
		t.Fatalf("expected log file in tempDir")
	}
	data, err := os.ReadFile(filepath.Join(tempDir, files[0].Name()))
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	var entry struct {
		APIResponseErrors []struct {
			StatusCode int                 `json:"status_code"`
			Error      string              `json:"error"`
			Addon      map[string][]string `json:"addon,omitempty"`
		} `json:"api_response_errors"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal NDJSON entry: %v", err)
	}
	if len(entry.APIResponseErrors) != 1 {
		t.Fatalf("expected one API error, got %d", len(entry.APIResponseErrors))
	}
	if entry.APIResponseErrors[0].Error != "upstream connection failed" {
		t.Fatalf("error = %q, want upstream connection failed", entry.APIResponseErrors[0].Error)
	}
	if got := entry.APIResponseErrors[0].Addon["Authorization"][0]; got == "Bearer secret-token" {
		t.Fatalf("Authorization addon header was not masked")
	}
}

func TestFileRequestLoggerSetFormat(t *testing.T) {
	logger := NewFileRequestLoggerWithFormat(true, t.TempDir(), "", 10, "text")
	logger.SetFormat("json")
	if logger.format != "json" {
		t.Fatalf("format = %q, want json", logger.format)
	}
	logger.SetFormat("invalid")
	if logger.format != "text" {
		t.Fatalf("format = %q, want text", logger.format)
	}
}

func TestJSONRequestLoggingPreservesAPIWebsocketTimeline(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")

	err := logger.LogRequest(
		"/v1/chat/completions",
		"POST",
		nil,
		[]byte(`{"model":"gpt-4"}`),
		200,
		nil,
		[]byte(`{"ok":true}`),
		nil,
		nil,
		nil,
		[]byte("connected\nframe: response.completed"),
		nil,
		"req-ws-123",
		time.Now(),
		time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequest failed: %v", err)
	}

	files, err := os.ReadDir(tempDir)
	if err != nil || len(files) == 0 {
		t.Fatalf("expected log file in tempDir")
	}
	data, err := os.ReadFile(filepath.Join(tempDir, files[0].Name()))
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	var entry struct {
		APIWebsocketTimelineRaw string `json:"api_websocket_timeline_raw"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal NDJSON entry: %v", err)
	}
	if entry.APIWebsocketTimelineRaw != "connected\nframe: response.completed" {
		t.Fatalf("timeline = %q", entry.APIWebsocketTimelineRaw)
	}
}

func TestJSONStreamingRequestLoggingCapsResponseBody(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")

	writer, err := logger.LogStreamingRequest(
		"/v1/chat/completions",
		"POST",
		nil,
		[]byte(`{"model":"gpt-4","stream":true}`),
		"req-large-stream-123",
	)
	if err != nil {
		t.Fatalf("LogStreamingRequest failed: %v", err)
	}
	_ = writer.WriteStatus(200, map[string][]string{"Content-Type": {"text/event-stream"}})
	writer.WriteChunkAsync(bytes.Repeat([]byte("x"), maxJSONStreamingResponseBytes+1024))
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	files, err := os.ReadDir(tempDir)
	if err != nil || len(files) == 0 {
		t.Fatalf("expected log file in tempDir")
	}
	data, err := os.ReadFile(filepath.Join(tempDir, files[0].Name()))
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	var entry struct {
		Response struct {
			BodyRaw       string `json:"body_raw"`
			BodyTruncated bool   `json:"body_truncated"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal NDJSON entry: %v", err)
	}
	if !entry.Response.BodyTruncated {
		t.Fatalf("expected response body to be marked truncated")
	}
	if len(entry.Response.BodyRaw) > maxJSONStreamingResponseBytes {
		t.Fatalf("response body length = %d, limit = %d", len(entry.Response.BodyRaw), maxJSONStreamingResponseBytes)
	}
}

func TestJSONRequestLoggingCapsFileBackedAPIResponse(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	source, err := NewFileBodySourceInDir(tempDir, "api-response")
	if err != nil {
		t.Fatalf("NewFileBodySourceInDir failed: %v", err)
	}
	part, err := source.CreatePart("response")
	if err != nil {
		t.Fatalf("CreatePart failed: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("y"), maxJSONFileBackedSectionBytes+1024)); err != nil {
		t.Fatalf("failed to write source: %v", err)
	}
	if err := part.Close(); err != nil {
		t.Fatalf("failed to close source: %v", err)
	}

	err = logger.LogRequestWithOptionsAndAllSources(
		"/v1/chat/completions",
		"POST",
		nil,
		[]byte(`{"model":"gpt-4"}`),
		502,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		source,
		nil,
		nil,
		nil,
		false,
		"req-api-response-123",
		time.Now(),
		time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequestWithOptionsAndAllSources failed: %v", err)
	}

	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("failed to read tempDir: %v", err)
	}
	var data []byte
	for _, file := range files {
		if filepath.Ext(file.Name()) != ".log" {
			continue
		}
		data, err = os.ReadFile(filepath.Join(tempDir, file.Name()))
		if err != nil {
			t.Fatalf("failed to read log: %v", err)
		}
		break
	}
	if len(data) == 0 {
		t.Fatalf("expected request log file")
	}

	var entry struct {
		APIResponseRaw       string `json:"api_response_raw"`
		APIResponseTruncated bool   `json:"api_response_truncated"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal NDJSON entry: %v", err)
	}
	if !entry.APIResponseTruncated {
		t.Fatalf("expected API response to be marked truncated")
	}
	if len(entry.APIResponseRaw) > maxJSONFileBackedSectionBytes {
		t.Fatalf("API response length = %d, limit = %d", len(entry.APIResponseRaw), maxJSONFileBackedSectionBytes)
	}
}

func TestJSONRequestLoggingKeepsDownstreamWebsocketTimelineInJSON(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")

	err := logger.LogRequest(
		"/v1/responses",
		"GET",
		map[string][]string{"Upgrade": {"websocket"}},
		nil,
		101,
		nil,
		nil,
		[]byte("client: open\nserver: ready"),
		nil,
		nil,
		nil,
		nil,
		"req-downstream-ws-123",
		time.Now(),
		time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequest failed: %v", err)
	}

	files, err := os.ReadDir(tempDir)
	if err != nil || len(files) == 0 {
		t.Fatalf("expected log file in tempDir")
	}
	data, err := os.ReadFile(filepath.Join(tempDir, files[0].Name()))
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	var entry struct {
		DownstreamTransport  string `json:"downstream_transport"`
		WebsocketTimelineRaw string `json:"websocket_timeline_raw"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("expected NDJSON websocket log, got %v: %s", err, string(data))
	}
	if entry.DownstreamTransport != "websocket" {
		t.Fatalf("downstream transport = %q, want websocket", entry.DownstreamTransport)
	}
	if entry.WebsocketTimelineRaw != "client: open\nserver: ready" {
		t.Fatalf("timeline = %q", entry.WebsocketTimelineRaw)
	}
}
