package executor_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func isEmojiRuneTest(r rune) bool {
	if r == 0xFE0F || r == 0xFE0E || r == 0x200D {
		return true
	}
	if r >= 0x1F3FB && r <= 0x1F3FF {
		return true
	}
	if r >= 0x1F1E6 && r <= 0x1F1FF {
		return true
	}
	if r >= 0xE0020 && r <= 0xE007F {
		return true
	}
	switch {
	case r >= 0x1F600 && r <= 0x1F64F:
		return true
	case r >= 0x1F300 && r <= 0x1F5FF:
		return true
	case r >= 0x1F680 && r <= 0x1F6FF:
		return true
	case r >= 0x1F900 && r <= 0x1F9FF:
		return true
	case r >= 0x1FA70 && r <= 0x1FAFF:
		return true
	case r >= 0x2600 && r <= 0x26FF:
		return true
	case r >= 0x2700 && r <= 0x27BF:
		return true
	}
	return false
}

func containsEmojiTest(s string) bool {
	for _, r := range s {
		if isEmojiRuneTest(r) {
			return true
		}
	}
	return false
}

func TestZhipuExecutor_Stream_EmojiFiltered(t *testing.T) {
	// Upstream emits emoji-rich content
	content := "Hello 😀🚀🇺🇸👍🏻 end"
	payload := fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","choices":[{"delta":{"content":%q}}]}`, content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
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

	// Parse SSE and confirm no emoji appears in any delta.content
	gotSanitized := false
	parse := func(line string) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			return
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "[DONE]" || payload == "" {
			return
		}
		var obj struct {
			Choices []struct {
				Delta map[string]json.RawMessage `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			return
		}
		if len(obj.Choices) == 0 || obj.Choices[0].Delta == nil {
			return
		}
		if v, ok := obj.Choices[0].Delta["content"]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil {
				if containsEmojiTest(s) {
					t.Fatalf("emoji not stripped in stream: %q", s)
				}
				gotSanitized = true
			}
		}
	}

	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if len(chunk.Payload) == 0 {
			continue
		}
		r := bufio.NewReader(strings.NewReader(string(chunk.Payload)))
		for {
			line, err := r.ReadString('\n')
			if line == "" && err != nil {
				break
			}
			parse(line)
			if err != nil {
				break
			}
		}
	}
	if !gotSanitized {
		t.Fatalf("no sanitized content observed")
	}
}
