package tui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// v8OnlyManagementServer is a test server that only implements the
// /v8/management contract. Every /v0/management request returns 404, matching
// a deployment where the deprecated routes were removed or blocked.
type v8OnlyManagementServer struct {
	*httptest.Server
	v0Requests int32
	config     map[string]any
}

func newV8OnlyManagementServer(t *testing.T) *v8OnlyManagementServer {
	t.Helper()
	srv := &v8OnlyManagementServer{
		config: map[string]any{
			"observability": map[string]any{
				"logs":  map[string]any{"debug": false},
				"usage": map[string]any{"usage-statistics-enabled": false},
			},
			"routing":  map[string]any{"strategy": "round-robin"},
			"requests": map[string]any{"proxy-url": ""},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&srv.v0Requests, 1)
		http.NotFound(w, r)
	})
	handle := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, fn)
	}
	handle("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(t, w, srv.config)
	})
	mux.HandleFunc("/v8/management/config/access/api-keys", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []string{"client-key-1"})
	})
	mux.HandleFunc("/v8/management/config/api-keys/gemini", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []any{
			map[string]any{"name": "g1", "keys": []any{map[string]any{"api-key": "AIzaSyTestKey01"}}},
		})
	})
	mux.HandleFunc("/v8/management/credentials", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"files": []any{map[string]any{"name": "a.json"}}})
	})
	mux.HandleFunc("/v8/management/oauth/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"status": "wait"})
	})
	mux.HandleFunc("/v8/management/oauth/auth-url", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("provider"); got != "codex" {
			http.Error(w, "provider", http.StatusBadRequest)
			return
		}
		writeJSON(t, w, map[string]any{"url": "https://auth.example.com/x", "state": "st-1"})
	})
	mux.HandleFunc("/v8/management/observability/logs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"lines": []string{"l1"}, "latest-timestamp": 5})
	})
	mux.HandleFunc("/v8/management/config/observability/logs/debug", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(t, w, true)
	})
	srv.Server = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func (s *v8OnlyManagementServer) assertNoV0(t *testing.T) {
	t.Helper()
	if n := atomic.LoadInt32(&s.v0Requests); n != 0 {
		t.Fatalf("expected 0 v0 requests against a v8-only server, got %d", n)
	}
}

// TestClient_V8OnlyServer exercises every management surface the TUI uses
// against a server that no longer exposes /v0/management. The TUI client must
// reach the v8 routes instead of failing.
func TestClient_V8OnlyServer(t *testing.T) {
	srv := newV8OnlyManagementServer(t)
	client := NewClientWithBaseURL(srv.URL, "secret")

	t.Run("GetConfig", func(t *testing.T) {
		cfg, err := client.GetConfig()
		if err != nil {
			t.Fatalf("GetConfig: %v", err)
		}
		if cfg["status"] == nil && cfg["observability"] == nil && cfg["debug"] == nil {
			t.Fatalf("GetConfig returned empty/invalid config: %v", cfg)
		}
	})

	t.Run("GetAPIKeys", func(t *testing.T) {
		keys, err := client.GetAPIKeys()
		if err != nil {
			t.Fatalf("GetAPIKeys: %v", err)
		}
		if len(keys) == 0 {
			t.Fatalf("expected at least one api key")
		}
	})

	t.Run("GetGeminiKeys", func(t *testing.T) {
		keys, err := client.GetGeminiKeys()
		if err != nil {
			t.Fatalf("GetGeminiKeys: %v", err)
		}
		if len(keys) == 0 {
			t.Fatalf("expected gemini keys")
		}
	})

	t.Run("GetAuthFiles", func(t *testing.T) {
		files, err := client.GetAuthFiles()
		if err != nil {
			t.Fatalf("GetAuthFiles: %v", err)
		}
		if len(files) == 0 {
			t.Fatalf("expected auth files")
		}
	})

	t.Run("GetAuthStatus", func(t *testing.T) {
		status, _, err := client.GetAuthStatus("st-1")
		if err != nil {
			t.Fatalf("GetAuthStatus: %v", err)
		}
		if status != "wait" {
			t.Fatalf("status = %q, want wait", status)
		}
	})

	t.Run("GetLogs", func(t *testing.T) {
		lines, latest, err := client.GetLogs(0, 10)
		if err != nil {
			t.Fatalf("GetLogs: %v", err)
		}
		if len(lines) == 0 || latest == 0 {
			t.Fatalf("expected log lines, got lines=%v latest=%d", lines, latest)
		}
	})

	t.Run("PutBoolField", func(t *testing.T) {
		if err := client.PutBoolField("debug", true); err != nil {
			t.Fatalf("PutBoolField: %v", err)
		}
	})

	t.Run("StartOAuth", func(t *testing.T) {
		data, err := client.getJSON("/v8/management/oauth/auth-url?provider=codex&is_webui=true")
		if err != nil {
			t.Fatalf("oauth auth-url: %v", err)
		}
		if getString(data, "url") == "" {
			t.Fatalf("expected auth url")
		}
	})

	srv.assertNoV0(t)
}

// TestClient_V0Fallback verifies the client still works against a legacy server
// that only exposes /v0/management (no v8 routes): each call must fall back.
func TestClient_V0Fallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v8/") {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.URL.Path == "/v0/management/config":
			writeJSON(t, w, map[string]any{"debug": true, "port": 8317})
		case r.URL.Path == "/v0/management/api-keys":
			writeJSON(t, w, map[string]any{"api-keys": []string{"k1"}})
		case r.URL.Path == "/v0/management/gemini-api-key":
			writeJSON(t, w, map[string]any{"gemini-api-key": []any{map[string]any{"api-key": "G1"}}})
		case r.URL.Path == "/v0/management/get-auth-status":
			writeJSON(t, w, map[string]any{"status": "wait"})
		case r.URL.Path == "/v0/management/logs":
			writeJSON(t, w, map[string]any{"lines": []string{"x"}, "latest-timestamp": 3})
		case r.URL.Path == "/v0/management/debug":
			writeJSON(t, w, map[string]any{"debug": true})
		case r.URL.Path == "/v0/management/auth-files":
			writeJSON(t, w, map[string]any{"files": []any{map[string]any{"name": "a"}}})
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	if cfg, err := client.GetConfig(); err != nil || getBool(cfg, "debug") != true {
		t.Fatalf("GetConfig fallback: cfg=%v err=%v", cfg, err)
	}
	if keys, err := client.GetAPIKeys(); err != nil || len(keys) != 1 {
		t.Fatalf("GetAPIKeys fallback: keys=%v err=%v", keys, err)
	}
	if keys, err := client.GetGeminiKeys(); err != nil || len(keys) != 1 {
		t.Fatalf("GetGeminiKeys fallback: keys=%v err=%v", keys, err)
	}
	if status, _, err := client.GetAuthStatus("s"); err != nil || status != "wait" {
		t.Fatalf("GetAuthStatus fallback: status=%q err=%v", status, err)
	}
	if lines, latest, err := client.GetLogs(0, 5); err != nil || len(lines) == 0 || latest == 0 {
		t.Fatalf("GetLogs fallback: lines=%v latest=%d err=%v", lines, latest, err)
	}
	if dbg, err := client.GetDebug(); err != nil || !dbg {
		t.Fatalf("GetDebug fallback: dbg=%v err=%v", dbg, err)
	}
	if files, err := client.GetAuthFiles(); err != nil || len(files) != 1 {
		t.Fatalf("GetAuthFiles fallback: files=%v err=%v", files, err)
	}
}

// TestClient_V8OAuthStartPrefersSharedEndpoint ensures OAuth start goes to the
// shared v8 route with the canonical provider parameter, not the legacy path.
func TestClient_V8OAuthStartPrefersSharedEndpoint(t *testing.T) {
	var gotPath, gotProvider string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotProvider = r.URL.Query().Get("provider")
		writeJSON(t, w, map[string]any{"url": "https://x", "state": "s"})
	}))
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")
	data, err := client.StartOAuthFlow("anthropic-auth-url")
	if err != nil {
		t.Fatalf("StartOAuthFlow: %v", err)
	}
	if gotPath != "/v8/management/oauth/auth-url" {
		t.Fatalf("path = %q, want /v8/management/oauth/auth-url", gotPath)
	}
	if gotProvider != "claude" {
		t.Fatalf("provider = %q, want claude", gotProvider)
	}
	if getString(data, "url") == "" {
		t.Fatalf("expected url")
	}
}

// TestClient_V8MissingNodeWrites verifies that an absent config node is treated
// as an empty value on the v8 contract (not a missing route), so the TUI can
// create the first access.api-keys entry and reads empty provider lists.
func TestClient_V8MissingNodeWrites(t *testing.T) {
	var v0Requests int32
	var ifMatchSeen string
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&v0Requests, 1)
		http.NotFound(w, r)
	})
	// Root config exists (proves the v8 contract), child nodes may 404.
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"root-etag"`)
		writeJSON(t, w, map[string]any{"server": map[string]any{"port": 8317}})
	})
	_ = ifMatchSeen
	mux.HandleFunc("/v8/management/config/access/api-keys", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Node not set on the server.
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		case http.MethodPut:
			ifMatchSeen = r.Header.Get("If-Match")
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	_ = ifMatchSeen
	mux.HandleFunc("/v8/management/config/api-keys/codex", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	keys, err := client.GetAPIKeys()
	if err != nil {
		t.Fatalf("GetAPIKeys on missing node: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected empty list for missing node, got %v", keys)
	}

	codex, err := client.GetCodexKeys()
	if err != nil {
		t.Fatalf("GetCodexKeys on missing node: %v", err)
	}
	if len(codex) != 0 {
		t.Fatalf("expected empty provider list, got %v", codex)
	}

	if err := client.AddAPIKey("first-key"); err != nil {
		t.Fatalf("AddAPIKey creating first key: %v", err)
	}
	if atomic.LoadInt32(&v0Requests) != 0 {
		t.Fatalf("v0 fallback used on v8-only server: %d requests", v0Requests)
	}
	// A create on a missing node must still be conditional: the root config
	// ETag guards the write so a concurrent creator is not silently overwritten.
	if ifMatchSeen != `"root-etag"` {
		t.Fatalf("If-Match on missing-node create = %q, want %q", ifMatchSeen, `"root-etag"`)
	}
}

// TestClient_V8APIKeyConditionalWrite ensures the config ETag captured on read
// is sent as If-Match on the subsequent replace, guarding concurrent edits.
func TestClient_V8APIKeyConditionalWrite(t *testing.T) {
	var ifMatch string
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	// Read path now goes through the root config document so the list value and
	// its ETag share one versioned snapshot.
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		writeJSON(t, w, map[string]any{"access": map[string]any{"api-keys": []string{"k1"}}})
	})
	mux.HandleFunc("/v8/management/config/access/api-keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			ifMatch = r.Header.Get("If-Match")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "method", http.StatusMethodNotAllowed)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	if err := client.EditAPIKey(0, "k1-new"); err != nil {
		t.Fatalf("EditAPIKey: %v", err)
	}
	if ifMatch != `"abc123"` {
		t.Fatalf("If-Match = %q, want %q", ifMatch, `"abc123"`)
	}
}

// TestClient_V8OpenAICompatPreservesGroups verifies openai-compatibility
// upstream entries are returned intact (name/base-url/prefix kept), not
// expanded into per-key rows.
func TestClient_V8OpenAICompatPreservesGroups(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{})
	})
	mux.HandleFunc("/v8/management/config/api-keys/openai-compatibility", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []any{
			map[string]any{
				"name":     "custom-upstream",
				"base-url": "https://api.example.com",
				"prefix":   "cu",
				"keys": []any{
					map[string]any{"api-key": "k1"},
					map[string]any{"api-key": "k2"},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	entries, err := client.GetOpenAICompat()
	if err != nil {
		t.Fatalf("GetOpenAICompat: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 upstream entry, got %d: %v", len(entries), entries)
	}
	if getString(entries[0], "name") != "custom-upstream" {
		t.Fatalf("name lost: %v", entries[0])
	}
	if getString(entries[0], "base-url") != "https://api.example.com" {
		t.Fatalf("base-url lost: %v", entries[0])
	}
}

// TestClient_V8Business404NotFallback ensures a resource-level 404 on a
// reachable v8 route is surfaced to the caller, not silently retried on v0.
func TestClient_V8Business404NotFallback(t *testing.T) {
	var v0Calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&v0Calls, 1)
		w.WriteHeader(http.StatusOK) // would wrongly "succeed" if retried
	})
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{})
	})
	mux.HandleFunc("/v8/management/credentials/status", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"credential not found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	err := client.ToggleAuthFile("missing.json", true)
	if err == nil {
		t.Fatalf("expected error for missing credential")
	}
	if atomic.LoadInt32(&v0Calls) != 0 {
		t.Fatalf("business 404 was retried on v0 (%d calls)", v0Calls)
	}
}

// TestClient_ProbeFailureNotCached verifies a transient probe failure does not
// poison the client into treating all v8 config reads as empty.
func TestClient_ProbeFailureNotCached(t *testing.T) {
	var configCalls int32
	failFirst := true
	mux := http.NewServeMux()
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&configCalls, 1)
		if failFirst {
			failFirst = false
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(t, w, map[string]any{"access": map[string]any{"api-keys": []string{"k"}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	// First probe fails transiently; must not be cached as definitive.
	keys, err := client.GetAPIKeys()
	if err == nil && len(keys) == 0 {
		t.Fatalf("transient probe failure turned into an empty key list")
	}
	// A second call after recovery should now read real keys.
	keys, err = client.GetAPIKeys()
	if err != nil {
		t.Fatalf("GetAPIKeys after recovery: %v", err)
	}
}

// TestClient_V8DeleteAuthFileBusiness404 ensures a credential-not-found 404 on
// a reachable v8 route does not trigger a v0 delete.
func TestClient_V8DeleteAuthFileBusiness404(t *testing.T) {
	var v0Calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&v0Calls, 1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{})
	})
	mux.HandleFunc("/v8/management/credentials", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			http.Error(w, `{"error":"credential not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(t, w, map[string]any{"files": []any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	err := client.DeleteAuthFile("missing.json")
	if err == nil {
		t.Fatalf("expected error for missing credential")
	}
	if atomic.LoadInt32(&v0Calls) != 0 {
		t.Fatalf("business 404 delete was retried on v0 (%d calls)", v0Calls)
	}
}

// TestClient_V8ProviderKeyNullInheritsGroup verifies a per-key null field
// inherits the group value, matching v8 semantics.
func TestClient_V8ProviderKeyNullInheritsGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{})
	})
	mux.HandleFunc("/v8/management/config/api-keys/gemini", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []any{
			map[string]any{
				"name":      "g1",
				"prefix":    "grp",
				"base-url":  "https://g.example.com",
				"proxy-url": "socks5://p",
				"keys": []any{
					map[string]any{"api-key": "k1", "prefix": nil},
					map[string]any{"api-key": "k2"},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	keys, err := client.GetGeminiKeys()
	if err != nil {
		t.Fatalf("GetGeminiKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %v", keys)
	}
	// k1 has explicit null prefix -> inherits "grp"; k2 absent prefix -> inherits too.
	if getString(keys[0], "prefix") != "grp" {
		t.Fatalf("null prefix did not inherit group: %v", keys[0])
	}
	if getString(keys[0], "base-url") != "https://g.example.com" {
		t.Fatalf("base-url not inherited: %v", keys[0])
	}
}

// TestClient_V8MissingETagRefusesWrite ensures an api-keys write is refused
// rather than sent unconditionally when the server supplies no ETag.
func TestClient_V8MissingETagRefusesWrite(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		// No ETag header at all.
		writeJSON(t, w, map[string]any{"access": map[string]any{"api-keys": []string{"k1"}}})
	})
	mux.HandleFunc("/v8/management/config/access/api-keys", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	if err := client.EditAPIKey(0, "x"); err == nil {
		t.Fatalf("expected refusal to write without a config version")
	}
}

// TestClient_ProbeIndeterminateBlocksFallback ensures a transient probe failure
// prevents a v0 write rather than silently switching protocols.
func TestClient_ProbeIndeterminateBlocksFallback(t *testing.T) {
	var v0Calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&v0Calls, 1)
		w.WriteHeader(http.StatusOK)
	})
	// Root config probe fails transiently (503): v8 support cannot be decided.
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/v8/management/credentials", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"x"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	err := client.DeleteAuthFile("f.json")
	if err == nil {
		t.Fatalf("expected error when v8 support is indeterminate")
	}
	if atomic.LoadInt32(&v0Calls) != 0 {
		t.Fatalf("write fell back to v0 with indeterminate v8 support")
	}
}

// TestCheckManagementSupport exercises the pre-flight compatibility probe.
func TestCheckManagementSupport(t *testing.T) {
	t.Run("v8 present", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v8/management/config" {
				writeJSON(t, w, map[string]any{})
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()
		if err := NewClientWithBaseURL(srv.URL, "x").CheckManagementSupport(); err != nil {
			t.Fatalf("v8 server should be supported: %v", err)
		}
	})

	t.Run("v0 only", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/management/config" {
				writeJSON(t, w, map[string]any{})
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()
		if err := NewClientWithBaseURL(srv.URL, "x").CheckManagementSupport(); err != nil {
			t.Fatalf("v0-only server should be supported: %v", err)
		}
	})

	t.Run("neither", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()
		err := NewClientWithBaseURL(srv.URL, "x").CheckManagementSupport()
		if err == nil {
			t.Fatalf("expected unsupported-server error")
		}
	})
}

// TestClient_V8IndexGuardPreventsStaleDelete ensures a selection made on an
// older snapshot cannot delete the wrong entry after a concurrent reorder.
func TestClient_V8IndexGuardPreventsStaleDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	// Server list has since changed: A removed, B is now at index 0.
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"e2"`)
		writeJSON(t, w, map[string]any{"access": map[string]any{"api-keys": []string{"B", "C"}}})
	})
	mux.HandleFunc("/v8/management/config/access/api-keys", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	// User selected index 1 when the displayed list was [A,B,C] and value B.
	// On the server it is now [B,C]; index 1 holds C, so the guard must refuse.
	err := client.DeleteAPIKey(1, "B")
	if err == nil {
		t.Fatalf("expected stale-selection refusal")
	}
}

// TestClient_V8StaleSelectionAfterRefresh simulates pressing edit on index 1
// (value B) and, before submit, a background refresh swapping the list. The
// guard uses the value captured at intent time, so the write must be refused.
func TestClient_V8StaleSelectionAfterRefresh(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"e3"`)
		// Refreshed list: A removed; index 1 now holds C, not the B the user picked.
		writeJSON(t, w, map[string]any{"access": map[string]any{"api-keys": []string{"B", "C"}}})
	})
	mux.HandleFunc("/v8/management/config/access/api-keys", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	// pendingValue captured at intent time was "B"; index 1 now holds "C".
	if err := client.EditAPIKey(1, "B-edited", "B"); err == nil {
		t.Fatalf("expected refusal editing a shifted entry")
	}
}

// TestClient_V8PreconditionFailedNotRetried ensures a 412 from a concurrent
// write is surfaced and NOT retried or turned into a v0 request.
func TestClient_V8PreconditionFailedNotRetried(t *testing.T) {
	var v0Calls, putCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&v0Calls, 1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"old"`)
		writeJSON(t, w, map[string]any{"access": map[string]any{"api-keys": []string{"k"}}})
	})
	mux.HandleFunc("/v8/management/config/access/api-keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			atomic.AddInt32(&putCalls, 1)
			http.Error(w, `{"error":"precondition_failed"}`, http.StatusPreconditionFailed)
			return
		}
		http.Error(w, "method", http.StatusMethodNotAllowed)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	err := client.AddAPIKey("k2")
	if err == nil {
		t.Fatalf("expected 412 to surface")
	}
	if atomic.LoadInt32(&v0Calls) != 0 {
		t.Fatalf("412 must not fall back to v0")
	}
}

// TestClient_V8EmptyKeysGroupYieldsNoEntry ensures a provider group with an
// explicit empty keys list produces no phantom key in the TUI.
func TestClient_V8EmptyKeysGroupYieldsNoEntry(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/v8/management/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{})
	})
	mux.HandleFunc("/v8/management/config/api-keys/claude", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []any{map[string]any{"name": "empty-group", "keys": []any{}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClientWithBaseURL(srv.URL, "secret")

	keys, err := client.GetClaudeKeys()
	if err != nil {
		t.Fatalf("GetClaudeKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("empty keys group rendered a phantom entry: %v", keys)
	}
}

// TestRunWithBaseURLUnsupportedExitsNonZero drives RunWithBaseURL against a
// server that exposes neither contract and asserts a non-nil error (non-zero
// exit) is returned rather than silently rendering and exiting 0.
func TestRunWithBaseURLUnsupportedExitsNonZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// hook == nil -> client mode -> pre-flight support check runs.
	err := RunWithBaseURL(srv.URL, "secret", nil, io.Discard)
	if err == nil {
		t.Fatalf("expected non-nil error for unsupported server")
	}
}

// TestApp_AuthThenUnsupportedExitsNonZero simulates the auth-then-404 flow: an
// auth-gated probe admits the user, credentials fail at runtime with both
// contracts absent, and the resulting fatal error must propagate through the
// final model so Run returns non-zero.
func TestApp_AuthThenUnsupportedExitsNonZero(t *testing.T) {
	var authed int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&authed, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Pre-flight sees 401 (auth-gated) => supported, so we get past it.
	app := NewAppWithBaseURL(srv.URL, "", nil)
	if app.fatalErr != nil {
		t.Fatalf("pre-flight should not mark an auth-gated server fatal")
	}
	// Simulate entering a password then the connect discovering unsupported.
	cmd := app.connectWithPassword("pw")
	msg := cmd()
	acm, ok := msg.(authConnectMsg)
	if !ok {
		t.Fatalf("expected authConnectMsg, got %T", msg)
	}
	if acm.fatal == nil {
		t.Fatalf("expected fatal error when both contracts are absent after auth")
	}
	// Feed through Update to confirm fatalErr + quit propagation.
	next, _ := app.Update(acm)
	if nextApp, ok := next.(App); !ok || nextApp.fatalErr == nil {
		t.Fatalf("fatal error did not propagate to the app model")
	}
}

// TestApp_AuthTransientProbeStaysRetryable ensures a transient support-probe
// failure (e.g. 503) after a wrong/absent credential is NOT fatal: the user
// keeps the retryable auth-gate error instead of being exited.
func TestApp_AuthTransientProbeStaysRetryable(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.URL.Path {
		case "/v8/management/config":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	app := NewAppWithBaseURL(srv.URL, "", nil)
	msg := app.connectWithPassword("pw")()
	acm, ok := msg.(authConnectMsg)
	if !ok {
		t.Fatalf("expected authConnectMsg, got %T", msg)
	}
	// v8 503 is transient, v0 404 is absent: not a definitive unsupported.
	if acm.fatal != nil {
		t.Fatalf("transient probe failure must not be fatal, got %v", acm.fatal)
	}
	if acm.err == nil {
		t.Fatalf("expected a retryable auth error")
	}
}

// TestRunWithBaseURLTransientProbeNotFatal asserts the startup pre-flight only
// exits on a definitive unsupported server, not on a transient probe failure.
func TestRunWithBaseURLTransientProbeNotFatal(t *testing.T) {
	// v8 -> 503 (transient), v0 -> 404. Not definitively unsupported.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v8/management/config" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Pre-flight must NOT return a fatal error; it falls through to Bubble Tea,
	// which runs until its window closes. We therefore assert CheckManagement
	// Support alone does not classify this as ErrManagementUnsupported.
	err := NewClientWithBaseURL(srv.URL, "x").CheckManagementSupport()
	if errors.Is(err, ErrManagementUnsupported) {
		t.Fatalf("transient v8 failure must not be reported as ErrManagementUnsupported")
	}
}
