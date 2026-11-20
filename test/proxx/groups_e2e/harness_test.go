// Package groupse2e runs the real proxy binary against a mock OpenAI-compatible upstream to
// exercise proxx model groups end to end. It is skipped unless PROXX_E2E=1:
//
//	PROXX_E2E=1 go test ./test/proxx/groups_e2e/ -run E2E -v -count=1
package groupse2e

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

const (
	clientKey = "test-key"
	mgmtKey   = "mgmt-test-secret"
)

// hit is one request received by the mock upstream.
type hit struct {
	Provider    string
	Model       string
	Stream      bool
	Temperature any
}

// mockUpstream is an OpenAI-compatible server whose behavior is switchable per upstream model.
//
// Behaviors: "ok" (default), "429:<retry-after-seconds>", "500", "400", "400ctx" (context-length
// error), "stream502" (streaming requests fail with 502 before any byte), "streambreak" (one
// chunk, then the connection is cut).
type mockUpstream struct {
	srv      *httptest.Server
	mu       sync.Mutex
	behavior map[string]string
	hits     []hit
}

func newMockUpstream() *mockUpstream {
	m := &mockUpstream{behavior: map[string]string{}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	return m
}

func (m *mockUpstream) set(model, behavior string) {
	m.mu.Lock()
	m.behavior[model] = behavior
	m.mu.Unlock()
}

func (m *mockUpstream) mark() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.hits)
}

func (m *mockUpstream) since(mark int) []hit {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]hit(nil), m.hits[mark:]...)
}

func models(hits []hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Model)
	}
	return out
}

func (m *mockUpstream) serve(w http.ResponseWriter, r *http.Request) {
	provider := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	model, _ := body["model"].(string)
	stream, _ := body["stream"].(bool)
	m.mu.Lock()
	m.hits = append(m.hits, hit{Provider: provider, Model: model, Stream: stream, Temperature: body["temperature"]})
	behavior := m.behavior[model]
	m.mu.Unlock()

	switch {
	case strings.HasPrefix(behavior, "429"):
		retry := strings.TrimPrefix(strings.TrimPrefix(behavior, "429"), ":")
		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		writeJSON(w, http.StatusTooManyRequests, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
		return
	case behavior == "500":
		writeJSON(w, http.StatusInternalServerError, `{"error":{"message":"upstream exploded","type":"server_error"}}`)
		return
	case behavior == "400":
		writeJSON(w, http.StatusBadRequest, `{"error":{"message":"invalid parameter foo","type":"invalid_request_error"}}`)
		return
	case behavior == "400ctx":
		writeJSON(w, http.StatusBadRequest, `{"error":{"message":"This model's maximum context length is 8192 tokens","type":"invalid_request_error","code":"context_length_exceeded"}}`)
		return
	case behavior == "stream502" && stream:
		writeJSON(w, http.StatusBadGateway, `{"error":{"message":"bad gateway","type":"server_error"}}`)
		return
	}
	if !stream {
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"id":"chatcmpl-1","object":"chat.completion","created":1700000000,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"hello from %s"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`, model, model))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	chunk := func(delta, finish string) {
		fin := "null"
		if finish != "" {
			fin = strconv.Quote(finish)
		}
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}]}\n\n", model, delta, fin)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk(fmt.Sprintf(`{"role":"assistant","content":"hello from %s"}`, model), "")
	if behavior == "streambreak" {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
		return
	}
	chunk(`{}`, "stop")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// proxy is a running proxy binary.
type proxy struct {
	t       *testing.T
	base    string
	cfgPath string
	cmd     *exec.Cmd
	logPath string
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func buildBinary(t *testing.T, dir string) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	bin := filepath.Join(dir, "proxx-groups")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/server")
	cmd.Dir = root
	if out, errBuild := cmd.CombinedOutput(); errBuild != nil {
		t.Fatalf("build proxy: %v\n%s", errBuild, out)
	}
	return bin
}

func startProxy(t *testing.T, bin, dir, cfg string) *proxy {
	t.Helper()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	logPath := filepath.Join(dir, "proxy.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("log file: %v", err)
	}
	cmd := exec.Command(bin, "--config", cfgPath, "--local-model", "--no-browser")
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if errStart := cmd.Start(); errStart != nil {
		t.Fatalf("start proxy: %v", errStart)
	}
	p := &proxy{t: t, cfgPath: cfgPath, cmd: cmd, logPath: logPath}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = logFile.Close()
		if t.Failed() {
			p.dumpLog()
		}
	})
	return p
}

func (p *proxy) dumpLog() {
	data, _ := os.ReadFile(p.logPath)
	lines := strings.Split(string(data), "\n")
	if len(lines) > 120 {
		lines = lines[len(lines)-120:]
	}
	p.t.Logf("proxy log tail:\n%s", strings.Join(lines, "\n"))
}

// waitFor polls fn until it returns true or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type result struct {
	Status   int
	Header   http.Header
	Body     []byte
	Elapsed  time.Duration
	JSON     map[string]any
	SSEDatas []string
}

func (p *proxy) do(method, path string, body any, headers map[string]string) result {
	p.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, p.base+path, reader)
	if err != nil {
		p.t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+clientKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return result{Body: []byte(err.Error()), Elapsed: time.Since(start)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	res := result{Status: resp.StatusCode, Header: resp.Header, Body: raw, Elapsed: time.Since(start)}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data:") {
				res.SSEDatas = append(res.SSEDatas, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
	} else {
		_ = json.Unmarshal(raw, &res.JSON)
	}
	return res
}

func (p *proxy) chat(model string, stream bool) result {
	return p.do(http.MethodPost, "/v1/chat/completions", map[string]any{
		"model":    model,
		"stream":   stream,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, nil)
}

func (p *proxy) listModels() []string {
	res := p.do(http.MethodGet, "/v1/models", nil, nil)
	var ids []string
	if data, ok := res.JSON["data"].([]any); ok {
		for _, item := range data {
			if m, okItem := item.(map[string]any); okItem {
				if id, okID := m["id"].(string); okID {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func chatContent(res result) string {
	choices, _ := res.JSON["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	content, _ := message["content"].(string)
	return content
}
