package logging

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

func TestResponseDiagnosticsAreBoundedAndReleased(t *testing.T) {
	capture := NewDeferredAPICapture()
	capture.RecordResponseError(nil)
	capture.RecordResponseError(errors.New(" \n"))
	if got := capture.ResponseDiagnostics(); len(got) != 0 {
		t.Fatalf("empty diagnostics = %v", got)
	}
	for range maxResponseDiagnostics + 1 {
		capture.RecordResponseError(errors.New("Authorization: Bearer secret-value\n" + strings.Repeat("界", 10000)))
	}
	diagnostics := capture.ResponseDiagnostics()
	if len(diagnostics) != maxResponseDiagnostics+1 {
		t.Fatalf("diagnostic count = %d", len(diagnostics))
	}
	for _, entry := range diagnostics {
		message := entry.Error.Error()
		if utf8.RuneCountInString(message) > diagnosticLogRuneLimit+3 || strings.ContainsAny(message, "\r\n") || strings.Contains(message, "secret-value") {
			t.Fatalf("unbounded or unsafe diagnostic: %q", message)
		}
	}
	if diagnostics[len(diagnostics)-1].Error.Error() != "[LOCAL ERRORS TRUNCATED]" {
		t.Fatal("missing diagnostic truncation marker")
	}
	if got := capture.RenderAPIResponse(); len(got) != 0 {
		t.Fatalf("local diagnostics fabricated an upstream response: %s", got)
	}
	capture.Close()
	capture.RecordResponseError(io.ErrUnexpectedEOF)
	if got := capture.ResponseDiagnostics(); len(got) != 0 || capture.diagnostics != nil {
		t.Fatal("closed capture retained local diagnostics")
	}
}

func TestResponseDiagnosticsSurviveOmittedAttempts(t *testing.T) {
	capture := NewDeferredAPICapture()
	for range MaxDeferredAPIResponseAttempts + 1 {
		capture.RecordRequest(nil, func(int, time.Time, []byte, bool, bool) DeferredAPIRequest {
			return func() []byte { return []byte("request") }
		})
	}
	capture.RecordResponseError(io.ErrUnexpectedEOF)
	if got := capture.ResponseDiagnostics(); len(got) != 1 || got[0].Error.Error() != io.ErrUnexpectedEOF.Error() {
		t.Fatalf("final attempt diagnostic was lost: %v", got)
	}
}

func TestResponseDiagnosticsInFileAndHomeLogs(t *testing.T) {
	for _, home := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("home=%t/streaming=%t", home, streaming), func(t *testing.T) {
				stub := &stubHomeRequestLogClient{heartbeatOK: true}
				original := currentHomeRequestLogClient
				currentHomeRequestLogClient = func() homeRequestLogClient { return stub }
				t.Cleanup(func() { currentHomeRequestLogClient = original })
				logsDir := t.TempDir()
				logger := NewFileRequestLogger(true, logsDir, "", 0)
				logger.SetHomeEnabled(home)
				capture := NewDeferredAPICapture()
				capture.RecordResponseError(io.ErrUnexpectedEOF)
				entries := append([]*interfaces.ErrorMessage{{StatusCode: 502, Error: errors.New("existing API error")}}, capture.ResponseDiagnostics()...)
				body := []byte(`{"error":"Failed to read upstream response"}`)
				upstream := []byte("=== API RESPONSE 1 ===\nStatus: 200\nBody:\n{\"partial\":true}\n")
				if streaming {
					writer, errLog := logger.LogStreamingRequest("/v1/responses", http.MethodPost, nil, nil, "diagnostic")
					if errLog != nil {
						t.Fatal(errLog)
					}
					if errStatus := writer.WriteStatus(502, map[string][]string{"Content-Type": {"text/event-stream"}}); errStatus != nil {
						t.Fatal(errStatus)
					}
					writer.WriteChunkAsync(body)
					if errResponse := writer.WriteAPIResponse(upstream); errResponse != nil {
						t.Fatal(errResponse)
					}
					if errErrors := writer.WriteAPIResponseErrors(entries); errErrors != nil {
						t.Fatal(errErrors)
					}
					if errClose := writer.Close(); errClose != nil {
						t.Fatal(errClose)
					}
				} else {
					// Diagnostics must be formatted separately from compressed client payloads.
					var compressed bytes.Buffer
					writer := gzip.NewWriter(&compressed)
					if _, errWrite := writer.Write(body); errWrite != nil {
						t.Fatal(errWrite)
					}
					if errClose := writer.Close(); errClose != nil {
						t.Fatal(errClose)
					}
					if errLog := logger.LogRequest("/v1/responses", http.MethodPost, nil, nil, 502, map[string][]string{"Content-Encoding": {"gzip"}}, compressed.Bytes(), nil, nil, upstream, nil, entries, "diagnostic", time.Now(), time.Now()); errLog != nil {
						t.Fatal(errLog)
					}
				}
				var logText string
				if home {
					if len(stub.pushed) != 1 {
						t.Fatalf("Home log count = %d", len(stub.pushed))
					}
					var payload homeRequestLogPayload
					if errDecode := json.Unmarshal(stub.pushed[0], &payload); errDecode != nil {
						t.Fatal(errDecode)
					}
					logText = payload.RequestLog
				} else {
					paths, errGlob := filepath.Glob(filepath.Join(logsDir, "*.log"))
					if errGlob != nil || len(paths) != 1 {
						t.Fatalf("logs = %v, error = %v", paths, errGlob)
					}
					data, errRead := os.ReadFile(paths[0])
					if errRead != nil {
						t.Fatal(errRead)
					}
					logText = string(data)
				}
				before, response, found := strings.Cut(logText, "=== RESPONSE ===\n")
				if !found || !strings.Contains(response, "Status: 502\nError: unexpected EOF\n\nHeaders:\n") || !strings.Contains(response, "\nBody:\n"+string(body)) {
					t.Fatalf("downstream response missing diagnostics or body:\n%s", logText)
				}
				if strings.Contains(before, "unexpected EOF") || strings.Count(before, "=== API ERROR RESPONSE ===") != 1 || !strings.Contains(before, "existing API error") || !strings.Contains(before, string(upstream)) {
					t.Fatalf("diagnostics modified upstream sections:\n%s", logText)
				}
				if strings.Contains(logText, "DECOMPRESSION ERROR") {
					t.Fatalf("diagnostics broke decompression:\n%s", logText)
				}
			})
		}
	}
}

func TestWebsocketTranscriptKeepsLocalDiagnosticsWithoutHTTPBody(t *testing.T) {
	capture := NewDeferredAPICapture()
	capture.RecordResponseError(io.ErrUnexpectedEOF)
	logger := NewFileRequestLogger(true, t.TempDir(), "", 0)
	var output bytes.Buffer
	if errWrite := logger.writeNonStreamingLog(&output, "/v1/responses", http.MethodGet, nil, nil, "", []byte("Event: websocket.error\n"), nil, nil, nil, nil, nil, nil, nil, capture.ResponseDiagnostics(), http.StatusSwitchingProtocols, nil, []byte("not a websocket response body"), nil, time.Now(), time.Time{}); errWrite != nil {
		t.Fatal(errWrite)
	}
	_, response, found := strings.Cut(output.String(), "=== RESPONSE ===\n")
	if !found || !strings.Contains(response, "Error: unexpected EOF") || strings.Contains(response, "Status:") || strings.Contains(response, "Headers:") || strings.Contains(response, "Body:") || strings.Contains(response, "not a websocket response body") {
		t.Fatalf("invalid websocket diagnostic section:\n%s", output.String())
	}
}
