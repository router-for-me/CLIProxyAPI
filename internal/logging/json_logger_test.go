package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
