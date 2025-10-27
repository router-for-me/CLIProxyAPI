package executor_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// Test default Zhipu stream chunking without env overrides:
// - First emitted content segment should be <= 2048 bytes
// - Subsequent content segments should be small (<= 128 bytes by default)
func TestZhipuExecutor_DefaultChunking_FirstAndSubsequentSegments(t *testing.T) {
	// Ensure no env overrides influence defaults
	_ = os.Unsetenv("ZHIPU_SSE_CHUNK_BYTES")
	_ = os.Unsetenv("SSE_CHUNK_BYTES")
	_ = os.Unsetenv("ZHIPU_SSE_FIRST_CHUNK_MAX_BYTES")
	_ = os.Unsetenv("SSE_FIRST_CHUNK_MAX_BYTES")

	// Create a long single SSE line upstream to force splitting
	longContent := strings.Repeat("A", 6000)
	ssePayload := fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","choices":[{"delta":{"content":%q}}]}`, longContent)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", ssePayload)
		flusher.Flush()
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := &config.Config{}
	exec := executor.NewZhipuExecutor(cfg)
	ctx := context.Background()
	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "tok", "base_url": srv.URL}}
	req := sdkexec.Request{Model: "glm-4.6", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`)}
	opts := sdkexec.Options{Stream: true, SourceFormat: sdktranslator.FromString("openai")}

	ch, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Helper to extract delta.content from a data line
	parseContent := func(line string) (string, bool) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			return "", false
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "[DONE]" || payload == "" {
			return "", false
		}
		var obj struct {
			Choices []struct {
				Delta map[string]json.RawMessage `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			return "", false
		}
		if len(obj.Choices) == 0 || obj.Choices[0].Delta == nil {
			return "", false
		}
		if v, ok := obj.Choices[0].Delta["content"]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil {
				return s, true
			}
		}
		return "", false
	}

	firstSeen := -1
	maxSubseq := 0
	gotAny := false

	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if len(chunk.Payload) == 0 {
			continue
		}
		// Consume possibly multiple lines in a chunk
		r := bufio.NewReader(strings.NewReader(string(chunk.Payload)))
		for {
			line, err := r.ReadString('\n')
			if line == "" && err != nil {
				break
			}
			if c, ok := parseContent(line); ok {
				gotAny = true
				if firstSeen < 0 {
					firstSeen = len(c)
					if firstSeen > 2048 {
						t.Fatalf("first content segment exceeded 2048 bytes: %d", firstSeen)
					}
				} else {
					if l := len(c); l > maxSubseq {
						maxSubseq = l
					}
				}
			} else {
				// Fallback: raw line size as proxy for segment length
				gotAny = true
				sz := len(strings.TrimSpace(line))
				if firstSeen < 0 {
					firstSeen = sz
					if firstSeen > 2048 {
						t.Fatalf("first segment exceeded 2048 bytes: %d", firstSeen)
					}
				} else if sz > maxSubseq {
					maxSubseq = sz
				}
			}
			if err != nil {
				break
			}
		}
	}

	if !gotAny {
		t.Fatalf("no content segments observed")
	}
	if maxSubseq <= 0 {
		t.Fatalf("did not observe any subsequent segments to validate default chunking")
	}
	if maxSubseq > 128 {
		t.Fatalf("subsequent content segment too large for default (expected <=128): %d", maxSubseq)
	}
}
