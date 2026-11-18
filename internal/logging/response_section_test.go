package logging

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

func TestResponseSectionLayout(t *testing.T) {
	for _, tt := range []struct {
		name        string
		status      int
		headers     map[string][]string
		body        string
		diagnostics []string
		want        string
	}{
		{
			name:        "local error",
			status:      http.StatusBadGateway,
			headers:     map[string][]string{"Content-Type": {"application/json; charset=utf-8"}},
			body:        `{"error":"Failed to read Codex search response"}`,
			diagnostics: []string{"unexpected EOF"},
			want: "=== RESPONSE ===\nStatus: 502\nError: unexpected EOF\n\n" +
				"Headers:\nContent-Type: application/json; charset=utf-8\n\n" +
				"Body:\n{\"error\":\"Failed to read Codex search response\"}\n",
		},
		{
			name:   "successful response without headers",
			status: http.StatusOK,
			body:   `{"ok":true}`,
			want:   "=== RESPONSE ===\nStatus: 200\n\nHeaders:\n<none>\n\nBody:\n{\"ok\":true}\n",
		},
		{
			name:    "body leading newline is preserved",
			status:  http.StatusOK,
			headers: map[string][]string{"Content-Type": {"text/event-stream"}},
			body:    "\r\ndata: [DONE]\n\n",
			want:    "=== RESPONSE ===\nStatus: 200\n\nHeaders:\nContent-Type: text/event-stream\n\nBody:\n\r\ndata: [DONE]\n\n\n",
		},
		{
			name:   "empty response",
			status: http.StatusNoContent,
			want:   "=== RESPONSE ===\nStatus: 204\n\nHeaders:\n<none>\n\nBody:\n\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if errWrite := writeResponseSection(&output, tt.status, true, tt.headers, strings.NewReader(tt.body), nil, true, tt.diagnostics); errWrite != nil {
				t.Fatal(errWrite)
			}
			if got := output.String(); got != tt.want {
				t.Fatalf("response layout = %q, want %q", got, tt.want)
			}

			var entries []*interfaces.ErrorMessage
			for _, diagnostic := range tt.diagnostics {
				entries = append(entries, &interfaces.ErrorMessage{Error: responseDiagnostic(diagnostic)})
			}
			logger := NewFileRequestLogger(true, t.TempDir(), "", 0)
			formatted := logger.formatLogContent("/v1/responses", http.MethodPost, nil, nil, nil, nil, nil, nil, []byte(tt.body), tt.status, tt.headers, entries)
			_, section, found := strings.Cut(formatted, "=== RESPONSE ===\n")
			if !found || "=== RESPONSE ===\n"+section != tt.want {
				t.Fatalf("buffered response layout = %q, want %q", section, tt.want)
			}
		})
	}
}
