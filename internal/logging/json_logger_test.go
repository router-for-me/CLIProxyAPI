package logging

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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
	firstChunkTimestamp := time.Date(2026, time.July, 25, 12, 34, 56, 123, time.UTC)
	writer.SetFirstChunkTimestamp(firstChunkTimestamp)

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
	if entry.APIResponseTimestamp != firstChunkTimestamp.Format(time.RFC3339Nano) {
		t.Errorf("api_response_timestamp = %q, want %q", entry.APIResponseTimestamp, firstChunkTimestamp.Format(time.RFC3339Nano))
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

func TestJSONRequestLoggingPreservesDecompressionError(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	var buf bytes.Buffer
	err := logger.writeNonStreamingLog(
		&buf, "/v1/chat/completions", "POST", nil, []byte(`{"model":"gpt-4"}`), "", false,
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
		200, map[string][]string{"Content-Encoding": {"gzip"}}, []byte("not-gzip"),
		errors.New("gzip: invalid header"), time.Now(), time.Time{},
	)
	if err != nil {
		t.Fatalf("writeNonStreamingLog failed: %v", err)
	}
	var entry jsonLogPayload
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v", err)
	}
	if entry.Response == nil || entry.Response.DecompressionError != "gzip: invalid header" {
		t.Fatalf("decompression_error = %q", entry.Response.DecompressionError)
	}
}

func TestFileRequestLoggerSetFormat(t *testing.T) {
	logger := NewFileRequestLoggerWithFormat(true, t.TempDir(), "", 10, "text")
	logger.SetFormat("json")
	if got := logger.currentFormat(); got != "json" {
		t.Fatalf("format = %q, want json", got)
	}
	logger.SetFormat("invalid")
	if got := logger.currentFormat(); got != "text" {
		t.Fatalf("format = %q, want text", got)
	}
}

func TestFileRequestLoggerSetFormatConcurrentAccess(t *testing.T) {
	logger := NewFileRequestLoggerWithFormat(true, t.TempDir(), "", 10, "text")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				logger.SetFormat("json")
				_ = logger.currentFormat()
				logger.SetFormat("text")
			}
		}()
	}
	wg.Wait()
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

func TestJSONRequestLoggingCapsFileBackedAPIRequest(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	source, err := NewFileBodySourceInDir(tempDir, "api-request")
	if err != nil {
		t.Fatalf("NewFileBodySourceInDir failed: %v", err)
	}
	part, err := source.CreatePart("request")
	if err != nil {
		t.Fatalf("CreatePart failed: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("z"), maxJSONFileBackedSectionBytes+1024)); err != nil {
		t.Fatalf("failed to write source: %v", err)
	}
	if err := part.Close(); err != nil {
		t.Fatalf("failed to close source: %v", err)
	}

	err = logger.LogRequestWithOptionsAndAllSources(
		"/v1/chat/completions", "POST", nil, []byte(`{"model":"gpt-4"}`),
		200, nil, nil, nil, nil, nil, source, nil, nil, nil, nil, nil,
		false, "req-api-request-123", time.Now(), time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequestWithOptionsAndAllSources failed: %v", err)
	}

	data := readOnlyLogFile(t, tempDir)
	var entry struct {
		APIRequestRaw       string `json:"api_request_raw"`
		APIRequestTruncated bool   `json:"api_request_truncated"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal NDJSON entry: %v", err)
	}
	if !entry.APIRequestTruncated {
		t.Fatalf("expected API request to be marked truncated")
	}
	if len(entry.APIRequestRaw) > maxJSONFileBackedSectionBytes {
		t.Fatalf("API request length = %d, limit = %d", len(entry.APIRequestRaw), maxJSONFileBackedSectionBytes)
	}
}

func TestJSONRequestLoggingMergesInlineAndFileBackedAPISections(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	requestSource, err := NewFileBodySourceInDir(tempDir, "api-request-merge")
	if err != nil {
		t.Fatalf("NewFileBodySourceInDir request: %v", err)
	}
	if err := requestSource.AppendPart([]byte("source-request\n")); err != nil {
		t.Fatalf("AppendPart request: %v", err)
	}
	responseSource, err := NewFileBodySourceInDir(tempDir, "api-response-merge")
	if err != nil {
		t.Fatalf("NewFileBodySourceInDir response: %v", err)
	}
	if err := responseSource.AppendPart([]byte("source-response\n")); err != nil {
		t.Fatalf("AppendPart response: %v", err)
	}

	err = logger.LogRequestWithOptionsAndAllSources(
		"/v1/chat/completions", "POST", nil, nil, 200, nil, nil, nil, nil,
		[]byte("inline-request"), requestSource, []byte("inline-response"), responseSource,
		nil, nil, nil, false, "req-merge-123", time.Now(), time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequestWithOptionsAndAllSources: %v", err)
	}
	data := readOnlyLogFile(t, tempDir)
	var entry jsonLogPayload
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v", err)
	}
	if entry.APIRequestRaw != "source-request\ninline-request" {
		t.Fatalf("api_request_raw = %q", entry.APIRequestRaw)
	}
	if entry.APIResponseRaw != "source-response\ninline-response" {
		t.Fatalf("api_response_raw = %q", entry.APIResponseRaw)
	}
}

func TestJSONRequestLoggingEncodesInvalidUTF8Losslessly(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	binary := []byte{0xff, 0xfe, 0x00, 0x80}
	err := logger.LogRequest(
		"/v1/files", "POST", nil, binary, 200, nil, binary,
		nil, binary, binary, nil, nil, "req-binary-123", time.Now(), time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequest failed: %v", err)
	}
	data := readOnlyLogFile(t, tempDir)
	var entry struct {
		RequestBodyRaw      string `json:"request_body_raw"`
		RequestBodyEncoding string `json:"request_body_encoding"`
		APIRequestRaw       string `json:"api_request_raw"`
		APIRequestEncoding  string `json:"api_request_encoding"`
		APIResponseRaw      string `json:"api_response_raw"`
		APIResponseEncoding string `json:"api_response_encoding"`
		Response            struct {
			BodyRaw      string `json:"body_raw"`
			BodyEncoding string `json:"body_encoding"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v", err)
	}
	want := base64.StdEncoding.EncodeToString(binary)
	if entry.RequestBodyRaw != want || entry.RequestBodyEncoding != "base64" {
		t.Fatalf("request body = %q encoding=%q", entry.RequestBodyRaw, entry.RequestBodyEncoding)
	}
	if entry.APIRequestRaw != want || entry.APIRequestEncoding != "base64" {
		t.Fatalf("API request = %q encoding=%q", entry.APIRequestRaw, entry.APIRequestEncoding)
	}
	if entry.APIResponseRaw != want || entry.APIResponseEncoding != "base64" {
		t.Fatalf("API response = %q encoding=%q", entry.APIResponseRaw, entry.APIResponseEncoding)
	}
	if entry.Response.BodyRaw != want || entry.Response.BodyEncoding != "base64" {
		t.Fatalf("response = %q encoding=%q", entry.Response.BodyRaw, entry.Response.BodyEncoding)
	}
}

func TestJSONRequestLoggingEncodesJSONLikeInvalidUTF8Losslessly(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	payload := []byte{'"', 0xff, '"'}
	if !json.Valid(payload) {
		t.Fatalf("test payload must exercise json.Valid with invalid UTF-8")
	}
	err := logger.LogRequest(
		"/v1/chat/completions", "POST", nil, payload, 200, nil, payload,
		nil, payload, payload, nil, nil, "req-json-binary-123", time.Now(), time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequest failed: %v", err)
	}

	data := readOnlyLogFile(t, tempDir)
	if !utf8.Valid(data) || !json.Valid(data) {
		t.Fatalf("log entry is not valid UTF-8 JSON: %q", data)
	}
	var entry struct {
		RequestBodyRaw      string `json:"request_body_raw"`
		RequestBodyEncoding string `json:"request_body_encoding"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v", err)
	}
	want := base64.StdEncoding.EncodeToString(payload)
	if entry.RequestBodyRaw != want || entry.RequestBodyEncoding != "base64" {
		t.Fatalf("request body = %q encoding=%q", entry.RequestBodyRaw, entry.RequestBodyEncoding)
	}
}

func TestJSONRequestLoggingEncodesInvalidUTF8WebsocketTimelines(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	timeline := []byte{0xff, 0x00, 0xfe}
	err := logger.LogRequest(
		"/v1/responses", "GET", map[string][]string{"Upgrade": {"websocket"}}, nil, 101, nil, nil,
		timeline, nil, nil, timeline, nil, "req-ws-binary-123", time.Now(), time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequest failed: %v", err)
	}

	data := readOnlyLogFile(t, tempDir)
	if !utf8.Valid(data) || !json.Valid(data) {
		t.Fatalf("log entry is not valid UTF-8 JSON: %q", data)
	}
	var entry struct {
		WebsocketTimelineRaw         string `json:"websocket_timeline_raw"`
		WebsocketTimelineEncoding    string `json:"websocket_timeline_encoding"`
		APIWebsocketTimelineRaw      string `json:"api_websocket_timeline_raw"`
		APIWebsocketTimelineEncoding string `json:"api_websocket_timeline_encoding"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v", err)
	}
	want := base64.StdEncoding.EncodeToString(timeline)
	if entry.WebsocketTimelineRaw != want || entry.WebsocketTimelineEncoding != "base64" {
		t.Fatalf("downstream timeline = %q encoding=%q", entry.WebsocketTimelineRaw, entry.WebsocketTimelineEncoding)
	}
	if entry.APIWebsocketTimelineRaw != want || entry.APIWebsocketTimelineEncoding != "base64" {
		t.Fatalf("upstream timeline = %q encoding=%q", entry.APIWebsocketTimelineRaw, entry.APIWebsocketTimelineEncoding)
	}
}

func TestJSONRequestLoggingCompactsStructuredPayloadsForNDJSON(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	payload := []byte("{\n  \"message\": \"hello\",\n  \"items\": [1, 2]\n}")
	err := logger.LogRequest(
		"/v1/chat/completions", "POST", nil, payload, 200, nil, payload,
		nil, payload, payload, nil, nil, "req-pretty-json-123", time.Now(), time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequest failed: %v", err)
	}

	data := readOnlyLogFile(t, tempDir)
	if bytes.Count(data, []byte("\n")) != 1 || data[len(data)-1] != '\n' {
		t.Fatalf("NDJSON entry must occupy exactly one physical line: %q", data)
	}
	var entry jsonLogPayload
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v", err)
	}
	want := `{"message":"hello","items":[1,2]}`
	if string(entry.RequestBody) != want {
		t.Fatalf("request body = %s, want %s", entry.RequestBody, want)
	}
	if string(entry.APIRequest) != want || string(entry.APIResponse) != want {
		t.Fatalf("upstream payloads were not compacted: request=%s response=%s", entry.APIRequest, entry.APIResponse)
	}
	if entry.Response == nil || string(entry.Response.Body) != want {
		t.Fatalf("response body was not compacted: %#v", entry.Response)
	}
}

func TestLimitedFileBodySourceWrites(t *testing.T) {
	for _, appendPayload := range []struct {
		name string
		fn   func(*FileBodySource, []byte) error
	}{
		{name: "bytes", fn: (*FileBodySource).AppendBytes},
		{name: "part", fn: (*FileBodySource).AppendPart},
	} {
		t.Run(appendPayload.name, func(t *testing.T) {
			source, err := newLimitedFileBodySourceInDir(t.TempDir(), appendPayload.name, 32)
			if err != nil {
				t.Fatalf("newLimitedFileBodySourceInDir failed: %v", err)
			}
			defer source.Cleanup()
			if err := appendPayload.fn(source, bytes.Repeat([]byte("x"), 64)); err != nil {
				t.Fatalf("append failed: %v", err)
			}
			data, err := source.Bytes()
			if err != nil {
				t.Fatalf("read source: %v", err)
			}
			if len(data) != 32 || !source.Truncated() {
				t.Fatalf("source length=%d truncated=%v", len(data), source.Truncated())
			}
		})
	}
}

func TestJSONRequestBodyTempFileCapsWhileSpooling(t *testing.T) {
	logger := NewFileRequestLoggerWithFormat(true, t.TempDir(), "", 10, "json")
	payload := bytes.Repeat([]byte("x"), maxJSONFileBackedSectionBytes+1024)
	path, truncated, err := logger.writeRequestBodyTempFile(payload, "json")
	if err != nil {
		t.Fatalf("writeRequestBodyTempFile failed: %v", err)
	}
	defer os.Remove(path)
	if !truncated {
		t.Fatal("request body temp file was not marked truncated")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat request body temp file: %v", err)
	}
	if info.Size() != maxJSONFileBackedSectionBytes {
		t.Fatalf("request body temp file size = %d, want %d", info.Size(), maxJSONFileBackedSectionBytes)
	}

	var buf bytes.Buffer
	if err := writeJSONLog(&buf, "/v1/responses", "POST", nil, nil, path, truncated, 200, nil, nil, "", false, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, time.Now(), time.Time{}, "http", "http"); err != nil {
		t.Fatalf("writeJSONLog failed: %v", err)
	}
	var entry jsonLogPayload
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v", err)
	}
	if !entry.RequestBodyTruncated {
		t.Fatal("request_body_truncated = false, want true")
	}
}

func TestJSONStreamingRequestLoggingMarksQueueDropsTruncated(t *testing.T) {
	writer := &FileStreamingLogWriter{chunkChan: make(chan []byte, 1), format: "json"}
	writer.chunkChan <- []byte("queued")
	writer.WriteChunkAsync([]byte("dropped"))
	if !writer.responseBodyTruncated.Load() {
		t.Fatalf("expected queue drop to mark response body truncated")
	}

	homeWriter := &homeStreamingLogWriter{chunkChan: make(chan []byte, 1), format: "json"}
	homeWriter.chunkChan <- []byte("queued")
	homeWriter.WriteChunkAsync([]byte("dropped"))
	if !homeWriter.responseBodyTruncated.Load() {
		t.Fatalf("expected Home queue drop to mark response body truncated")
	}
}

func TestJSONStreamingRequestLoggingSerializesQueueDropOnResponse(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	streamWriter, err := logger.LogStreamingRequest(
		"/v1/chat/completions", "POST", nil, []byte(`{"model":"gpt-4","stream":true}`),
		"req-queue-drop-123",
	)
	if err != nil {
		t.Fatalf("LogStreamingRequest failed: %v", err)
	}
	writer := streamWriter.(*FileStreamingLogWriter)
	writer.responseBodyTruncated.Store(true)
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data := readOnlyLogFile(t, tempDir)
	var entry jsonLogPayload
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v", err)
	}
	if entry.RequestBodyTruncated {
		t.Fatalf("request body incorrectly marked truncated")
	}
	if entry.Response == nil || !entry.Response.BodyTruncated {
		t.Fatalf("response body missing queue-drop truncation marker")
	}
}

func TestJSONStreamingRequestLoggingCapsTempFileWhileSpooling(t *testing.T) {
	tempDir := t.TempDir()
	responseFile, err := os.CreateTemp(tempDir, "response-body-*.tmp")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	writer := &FileStreamingLogWriter{
		responseBodyFile: responseFile,
		responseBodyPath: responseFile.Name(),
		chunkChan:        make(chan []byte, 2),
		closeChan:        make(chan struct{}),
		errorChan:        make(chan error, 1),
		format:           "json",
	}
	go writer.asyncWriter()
	writer.chunkChan <- bytes.Repeat([]byte("x"), maxJSONStreamingResponseBytes)
	writer.chunkChan <- []byte("overflow")
	close(writer.chunkChan)
	<-writer.closeChan

	info, err := os.Stat(writer.responseBodyPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() > maxJSONStreamingResponseBytes {
		t.Fatalf("temp file size = %d", info.Size())
	}
	if !writer.responseBodyTruncated.Load() {
		t.Fatalf("expected spooling limit to mark response truncated")
	}
}

func TestJSONStreamingRequestLoggingCapsRequestBody(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	writer, err := logger.LogStreamingRequest(
		"/v1/chat/completions", "POST", nil,
		bytes.Repeat([]byte("r"), maxJSONFileBackedSectionBytes+1024),
		"req-stream-request-body-123",
	)
	if err != nil {
		t.Fatalf("LogStreamingRequest failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data := readOnlyLogFile(t, tempDir)
	var entry struct {
		RequestBodyRaw       string `json:"request_body_raw"`
		RequestBodyTruncated bool   `json:"request_body_truncated"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal NDJSON entry: %v", err)
	}
	if !entry.RequestBodyTruncated {
		t.Fatalf("expected request body to be marked truncated")
	}
	if len(entry.RequestBodyRaw) > maxJSONFileBackedSectionBytes {
		t.Fatalf("request body length = %d, limit = %d", len(entry.RequestBodyRaw), maxJSONFileBackedSectionBytes)
	}
}

func TestJSONStreamingRequestLoggingPreservesWebsocketSourceTruncation(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	streamWriter, err := logger.LogStreamingRequest("/v1/responses", "POST", nil, nil, "req-stream-ws-source-123")
	if err != nil {
		t.Fatalf("LogStreamingRequest failed: %v", err)
	}
	source, err := newLimitedFileBodySourceInDir(tempDir, "api-websocket-timeline", 32)
	if err != nil {
		t.Fatalf("newLimitedFileBodySourceInDir failed: %v", err)
	}
	if err := source.AppendBytes(bytes.Repeat([]byte("x"), 64)); err != nil {
		t.Fatalf("AppendBytes failed: %v", err)
	}
	sourceWriter, ok := streamWriter.(interface {
		WriteAPIWebsocketTimelineSource(*FileBodySource) error
	})
	if !ok {
		t.Fatalf("writer type %T does not accept websocket timeline sources", streamWriter)
	}
	if err := sourceWriter.WriteAPIWebsocketTimelineSource(source); err != nil {
		t.Fatalf("WriteAPIWebsocketTimelineSource failed: %v", err)
	}
	if err := streamWriter.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	var entry jsonLogPayload
	if err := json.Unmarshal(readOnlyLogFile(t, tempDir), &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v", err)
	}
	if !entry.APIWebsocketTimelineTruncated {
		t.Fatal("api_websocket_timeline_truncated = false, want true")
	}
}

func TestRequestLogFormatSnapshotSurvivesReload(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	source, err := logger.NewFileBodySourceWithFormat("api-request", "json")
	if err != nil {
		t.Fatalf("NewFileBodySourceWithFormat failed: %v", err)
	}
	if err := source.AppendBytes([]byte(`{"model":"gpt-5"}`)); err != nil {
		t.Fatalf("AppendBytes failed: %v", err)
	}
	logger.SetFormat("text")
	if err := logger.LogRequestWithOptionsAndAllSources(
		"/v1/responses", "POST", nil, nil, 200, nil, nil,
		nil, nil, nil, source, nil, nil, nil, nil, nil, false,
		"req-format-snapshot-123", time.Now(), time.Time{},
	); err != nil {
		t.Fatalf("LogRequestWithOptionsAndAllSources failed: %v", err)
	}
	if data := readOnlyLogFile(t, tempDir); !json.Valid(data) {
		t.Fatalf("request finalized with reloaded text format: %q", data)
	}
}

func TestTextStreamingRequestLoggingIncludesWebsocketTimelineSource(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "text")
	streamWriter, err := logger.LogStreamingRequest("/v1/responses", "POST", nil, nil, "req-text-ws-source-123")
	if err != nil {
		t.Fatalf("LogStreamingRequest failed: %v", err)
	}
	source, err := NewFileBodySourceInDir(tempDir, "api-websocket-timeline")
	if err != nil {
		t.Fatalf("NewFileBodySourceInDir failed: %v", err)
	}
	if err := source.AppendPart([]byte("source-backed websocket timeline")); err != nil {
		t.Fatalf("AppendPart failed: %v", err)
	}
	sourceWriter, ok := streamWriter.(interface {
		WriteAPIWebsocketTimelineSource(*FileBodySource) error
	})
	if !ok {
		t.Fatalf("writer type %T does not accept websocket timeline sources", streamWriter)
	}
	if err := sourceWriter.WriteAPIWebsocketTimelineSource(source); err != nil {
		t.Fatalf("WriteAPIWebsocketTimelineSource failed: %v", err)
	}
	if err := streamWriter.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if data := readOnlyLogFile(t, tempDir); !bytes.Contains(data, []byte("source-backed websocket timeline")) {
		t.Fatalf("text log omitted source-backed websocket timeline: %q", data)
	}
}

func TestJSONStreamingRequestLoggingPropagatesMissingRequestBodyFile(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	streamWriter, err := logger.LogStreamingRequest(
		"/v1/chat/completions", "POST", nil, []byte(`{"model":"gpt-4","stream":true}`),
		"req-missing-body-123",
	)
	if err != nil {
		t.Fatalf("LogStreamingRequest failed: %v", err)
	}
	writer, ok := streamWriter.(*FileStreamingLogWriter)
	if !ok {
		t.Fatalf("writer type = %T", streamWriter)
	}
	if err := os.Remove(writer.requestBodyPath); err != nil {
		t.Fatalf("remove request body temp file: %v", err)
	}
	if err := writer.Close(); err == nil {
		t.Fatalf("Close succeeded with missing request body temp file")
	}
}

func TestJSONRequestLoggingMarksFileBackedWebsocketTimelinesTruncated(t *testing.T) {
	tempDir := t.TempDir()
	logger := NewFileRequestLoggerWithFormat(true, tempDir, "", 10, "json")
	downstream := newLargeFileBodySource(t, tempDir, "downstream-ws")
	upstream := newLargeFileBodySource(t, tempDir, "upstream-ws")

	err := logger.LogRequestWithOptionsAndAllSources(
		"/v1/responses", "GET", map[string][]string{"Upgrade": {"websocket"}}, nil,
		101, nil, nil, nil, downstream, nil, nil, nil, nil, nil, upstream, nil,
		false, "req-ws-truncated-123", time.Now(), time.Time{},
	)
	if err != nil {
		t.Fatalf("LogRequestWithOptionsAndAllSources failed: %v", err)
	}

	data := readOnlyLogFile(t, tempDir)
	var entry struct {
		WebsocketTimelineTruncated    bool `json:"websocket_timeline_truncated"`
		APIWebsocketTimelineTruncated bool `json:"api_websocket_timeline_truncated"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal NDJSON entry: %v", err)
	}
	if !entry.WebsocketTimelineTruncated {
		t.Fatalf("expected downstream websocket timeline to be marked truncated")
	}
	if !entry.APIWebsocketTimelineTruncated {
		t.Fatalf("expected upstream websocket timeline to be marked truncated")
	}
}

func newLargeFileBodySource(t *testing.T, dir, prefix string) *FileBodySource {
	t.Helper()
	source, err := NewFileBodySourceInDir(dir, prefix)
	if err != nil {
		t.Fatalf("NewFileBodySourceInDir failed: %v", err)
	}
	part, err := source.CreatePart("timeline")
	if err != nil {
		t.Fatalf("CreatePart failed: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("w"), maxJSONFileBackedSectionBytes+1024)); err != nil {
		t.Fatalf("failed to write source: %v", err)
	}
	if err := part.Close(); err != nil {
		t.Fatalf("failed to close source: %v", err)
	}
	return source
}

func readOnlyLogFile(t *testing.T, dir string) []byte {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read log dir: %v", err)
	}
	for _, file := range files {
		if filepath.Ext(file.Name()) != ".log" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			t.Fatalf("failed to read log file: %v", err)
		}
		return data
	}
	t.Fatalf("expected log file in %s", dir)
	return nil
}
