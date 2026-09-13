//go:build linux || darwin

package claudemaster

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestNativeCompatibilitySmoke is deliberately opt-in: normal unit tests never
// find or launch an installed personal Claude binary. CI downloads a disposable
// pinned artifact and verifies its publisher manifest before setting this path.
// This tests native CONNECT, TLS, API-key/OAuth-shaped headers and SSE parsing,
// not a real login or inference provider. Both upstreams are synthetic; other
// CONNECT hosts are refused before the production proxy can dial them.
func TestNativeCompatibilitySmoke(t *testing.T) {
	binary := os.Getenv("CLAUDE_MASTER_NATIVE_TEST_BINARY")
	if binary == "" {
		t.Skip("set CLAUDE_MASTER_NATIVE_TEST_BINARY to an explicitly verified native artifact")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native test binary must be an absolute path")
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		t.Fatal("native test binary must be an existing regular file")
	}
	for _, mode := range []string{"api_key", "oauth"} {
		t.Run(mode, func(t *testing.T) {
			nativeCompatibilitySmoke(t, binary, mode)
		})
	}
}

func nativeCompatibilitySmoke(t *testing.T, binary, mode string) {
	t.Helper()
	const syntheticAPIKey = "sk-ant-api03-SYNTHETIC-NATIVE-COMPATIBILITY-NOT-A-CREDENTIAL"
	const syntheticOAuthToken = "sk-ant-oat01-SYNTHETIC-NATIVE-COMPATIBILITY-NOT-A-CREDENTIAL"
	dir := canonicalTestTempDir(t)
	configDir := filepath.Join(dir, "isolated-config")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, ".claude.json"), []byte(`{"hasCompletedOnboarding":true,"numStartups":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Native Node/Bun validation needs the production CA and separate leaf,
	// rather than the directly trusted leaf used by the Go-only proxy tests.
	certs, err := newProcessCertificate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(certs.dir) })
	root, err := x509.ParseCertificate(certs.leaf.Certificate[len(certs.leaf.Certificate)-1])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)

	var mu sync.Mutex
	nativeHeaders := make(map[string]http.Header)
	var inferenceCalls int
	var measuredHeaders []string
	var measuredBeta []string
	proxy, _, _ := proxyTestStartWithCertificate(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		before := nativeHeaders[r.Header.Get("X-Client-Request-Id")]
		inferenceCalls++
		mu.Unlock()
		if before == nil {
			t.Error("inference lost the native request correlation header")
		} else if want := coreexecutor.NativeClaudeProtocolHeaders(before); !reflect.DeepEqual(r.Header, want) {
			t.Error("inference changed or dropped reviewed native protocol headers")
		}
		marker, ok := coreexecutor.NativeClaudeProtocolHeadersFromContext(r.Context())
		if !ok || !reflect.DeepEqual(marker, coreexecutor.NativeClaudeProtocolHeaders(before)) {
			t.Error("proxy lost the native protocol opt-in context required by the selected executor")
		}
		for _, name := range []string{"Authorization", "X-Api-Key", "Cookie", "X-Claude-Code-Session-Id", "Proxy-Authorization"} {
			if r.Header.Get(name) != "" {
				t.Errorf("inference retained forbidden native identity header %s", name)
			}
		}
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "claude-sonnet-4-6" || !body.Stream {
			t.Error("native inference did not send the expected streamed JSON request")
			http.Error(w, "unexpected synthetic request", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Error("native inference used an unexpected method or route")
			http.Error(w, "unexpected synthetic route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "req_native_compatibility_fixture")
		for _, event := range nativeCompatibilityEvents() {
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event[0], event[1]); err != nil {
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}), proxyTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}), certs.leaf, pool)

	// Observe headers before production filtering. The guarded CONNECT handler
	// below enters ownedHandler (which takes mu) before touching proxy state,
	// and innerListen's channel publishes that state to the inner HTTP server.
	proxy.mu.Lock()
	originalInner := proxy.inner.Handler
	proxy.inner.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages" {
			if mode == "oauth" {
				if r.Header.Get("Authorization") != "Bearer "+syntheticOAuthToken || r.Header.Get("X-Api-Key") != "" {
					t.Error("native OAuth fixture did not use only its synthetic bearer credential")
				}
			} else if r.Header.Get("X-Api-Key") != syntheticAPIKey || r.Header.Get("Authorization") != "" {
				t.Error("native API-key fixture did not use only its synthetic API key")
			}
			classified := coreexecutor.NativeClaudeProtocolHeaders(r.Header)
			for name := range r.Header {
				key := http.CanonicalHeaderKey(name)
				switch key {
				case "Authorization", "X-Api-Key", "Cookie", "X-Claude-Code-Session-Id", "Proxy-Authorization", "Connection", "Content-Length":
					continue // Explicit credential, session or transport exclusions.
				}
				if _, ok := classified[key]; !ok {
					t.Errorf("unclassified native inference header %s requires compatibility review", key)
				}
			}
			mu.Lock()
			nativeHeaders[r.Header.Get("X-Client-Request-Id")] = r.Header.Clone()
			measuredBeta = append([]string(nil), r.Header.Values("Anthropic-Beta")...)
			measuredHeaders = measuredHeaders[:0]
			for name := range r.Header {
				measuredHeaders = append(measuredHeaders, name)
			}
			mu.Unlock()
		}
		originalInner.ServeHTTP(w, r)
	})
	proxy.mu.Unlock()
	guarded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, err := proxyAuthority(r.RequestURI)
		if err != nil || host != masterAPIHost {
			http.Error(w, "native fixture rejects non-Anthropic tunnels", http.StatusBadGateway)
			return
		}
		proxy.ownedHandler(proxy.handleConnect).ServeHTTP(w, r)
	}))
	t.Cleanup(guarded.Close)
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	guardURL, err := url.Parse(guarded.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.Host = guardURL.Host

	// This is a test-process limit, not a production network timeout. Empty Env,
	// an isolated config directory, disabled settings sources/MCP/hooks and no
	// tools keep this fixture independent of personal sessions and credentials.
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--print", "--output-format", "text", "--model", "claude-sonnet-4-6",
		"--tools", "", "--max-turns", "1", "--no-session-persistence", "--setting-sources", "", "--strict-mcp-config",
		"--safe-mode", "Reply with NATIVE_COMPATIBILITY_OK")
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "CLAUDE_CONFIG_DIR=" + configDir,
		"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"HTTPS_PROXY=" + proxyURL.String(), "HTTP_PROXY=" + proxyURL.String(), "NODE_EXTRA_CA_CERTS=" + certs.caPath,
	}
	if mode == "oauth" {
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+syntheticOAuthToken)
	} else {
		cmd.Env = append(cmd.Env, "ANTHROPIC_API_KEY="+syntheticAPIKey)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		// All input and output belong to this synthetic fixture, never a login.
		t.Fatalf("native compatibility fixture failed: %v; output: %s", err, output)
	}
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "NATIVE_COMPATIBILITY_OK") {
		t.Fatal("native client did not parse the synthetic streamed reply")
	}
	mu.Lock()
	calls := inferenceCalls
	headers := append([]string(nil), measuredHeaders...)
	betas := append([]string(nil), measuredBeta...)
	mu.Unlock()
	stats := proxy.Snapshot()
	if calls < 1 || stats.ConnectAccepted < 1 || stats.InferenceRequests < 1 || stats.BlockedRequests != 0 {
		t.Fatalf("unexpected native compatibility counts: inference=%d proxy=%+v", calls, stats)
	}
	sort.Strings(headers)
	t.Logf("native CONNECT/TLS/SSE passed; inference=%d; measured header names=%v; beta flags=%v", calls, headers, betas)
}

func nativeCompatibilityEvents() [][2]string {
	return [][2]string{
		{"message_start", `{"type":"message_start","message":{"id":"msg_native_compatibility_fixture","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"NATIVE_COMPATIBILITY_OK"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
}
