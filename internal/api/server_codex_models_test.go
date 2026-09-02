package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCodexDirectModelsRouteIgnoresUserAgent(t *testing.T) {
	server := newTestServer(t)

	responses := make([][]byte, 0, 2)
	for _, userAgent := range []string{"claude-cli/1.0", "Mozilla/5.0"} {
		req := httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Authorization", "Bearer test-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("User-Agent %q: status = %d, want %d; body=%s", userAgent, rr.Code, http.StatusOK, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("User-Agent %q: invalid JSON: %v", userAgent, err)
		}
		if _, ok := payload["models"]; !ok {
			t.Fatalf("User-Agent %q: response missing models catalog: %s", userAgent, rr.Body.String())
		}
		responses = append(responses, append([]byte(nil), rr.Body.Bytes()...))
	}

	if string(responses[0]) != string(responses[1]) {
		t.Fatalf("Codex catalog varies by User-Agent:\nclaude=%s\nother=%s", responses[0], responses[1])
	}
}
