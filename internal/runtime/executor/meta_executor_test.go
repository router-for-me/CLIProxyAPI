package executor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestMetaExecutor_Identifier(t *testing.T) {
	exec := NewMetaExecutor(&config.Config{})
	if exec.Identifier() != "meta" {
		t.Fatalf("expected 'meta', got '%s'", exec.Identifier())
	}
}

func TestMetaExecutor_MetaCredsResolution(t *testing.T) {
	t.Run("from attributes", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{
				"api_key":  "attr-key",
				"base_url": "https://custom.meta.com/v1",
			},
		}
		baseURL, token := metaCreds(auth)
		if baseURL != "https://custom.meta.com/v1" || token != "attr-key" {
			t.Errorf("unexpected creds: base=%s, token=%s", baseURL, token)
		}
	})

	t.Run("from metadata", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"access_token": "meta-oauth-token",
			},
		}
		baseURL, token := metaCreds(auth)
		if baseURL != "https://api.meta.ai/v1" || token != "meta-oauth-token" {
			t.Errorf("unexpected creds: base=%s, token=%s", baseURL, token)
		}
	})

	t.Run("no bleed to env or local auth", func(t *testing.T) {
		t.Setenv("META_API_KEY", "env-secret-key")
		tempDir := t.TempDir()
		authPath := filepath.Join(tempDir, "auth.json")
		content := `{"schema_version":1,"providers":{"meta":{"api_key":"local-muse-key","api_base_url":"https://api.meta.ai/v1"}}}`
		_ = os.WriteFile(authPath, []byte(content), 0600)
		t.Setenv("MUSE_AUTH_PATH", authPath)

		// nil auth should NOT bleed to env or local file
		baseURL, token := metaCreds(nil)
		if token != "" {
			t.Errorf("expected empty token for nil auth, got %s", token)
		}
		if baseURL != "https://api.meta.ai/v1" {
			t.Errorf("expected default base URL, got %s", baseURL)
		}

		// empty auth should NOT bleed to env or local file
		emptyAuth := &cliproxyauth.Auth{}
		_, token = metaCreds(emptyAuth)
		if token != "" {
			t.Errorf("expected empty token for empty auth, got %s", token)
		}
	})
}

func TestMetaExecutor_ParseRetryAfter(t *testing.T) {
	now := time.Now()
	futureEpoch := now.Add(45 * time.Minute).Unix()

	body := []byte(mapToJSON(map[string]any{
		"error": map[string]any{
			"code":      "rate_limit_exceeded",
			"message":   "Subscription quota exhausted.",
			"resets_at": futureEpoch,
			"type":      "rate_limit_error",
		},
	}))

	retryAfter := parseMetaRetryAfter(http.StatusTooManyRequests, body, now)
	if retryAfter == nil {
		t.Fatalf("expected non-nil retryAfter")
	}
	if *retryAfter < 40*time.Minute || *retryAfter > 50*time.Minute {
		t.Errorf("unexpected retryAfter duration: %v", *retryAfter)
	}

	// Non-429 status returns nil
	if got := parseMetaRetryAfter(http.StatusOK, body, now); got != nil {
		t.Errorf("expected nil for 200 OK")
	}

	// Past epoch returns nil
	pastBody := []byte(mapToJSON(map[string]any{
		"error": map[string]any{
			"resets_at": now.Add(-10 * time.Minute).Unix(),
		},
	}))
	if got := parseMetaRetryAfter(http.StatusTooManyRequests, pastBody, now); got != nil {
		t.Errorf("expected nil for past reset time")
	}
}

func TestMetaExecutor_ExecuteSuccessAndRateLimit(t *testing.T) {
	now := time.Now()
	resetEpoch := now.Add(2 * time.Hour).Unix()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		if r.URL.Path == "/chat/completions" {
			// Check user agent
			if ua := r.Header.Get("User-Agent"); ua != "muse-code/1.0.2" {
				t.Errorf("expected User-Agent muse-code/1.0.2, got %s", ua)
			}
			// Simulate 429 rate limit
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":      "rate_limit_exceeded",
					"message":   "Subscription quota exhausted. Your usage window resets soon.",
					"resets_at": resetEpoch,
					"type":      "rate_limit_error",
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewMetaExecutor(cfg)

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "valid-token",
			"base_url": server.URL,
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	}

	_, err := exec.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatalf("expected error from 429 response")
	}

	var se statusErr
	if errors.As(err, &se) {
		if se.StatusCode() != http.StatusTooManyRequests {
			t.Errorf("expected 429 status code, got %d", se.StatusCode())
		}
		if se.RetryAfter() == nil {
			t.Fatalf("expected non-nil RetryAfter")
		}
		if *se.RetryAfter() < 1*time.Hour || *se.RetryAfter() > 3*time.Hour {
			t.Errorf("unexpected RetryAfter: %v", *se.RetryAfter())
		}
	} else {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}

	var scoped interface{ IsCredentialScoped() bool }
	if !errors.As(err, &scoped) || !scoped.IsCredentialScoped() {
		t.Fatalf("expected rate limit error to be credential scoped, got %T: %v", err, err)
	}
}

func mapToJSON(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func TestMetaExecutor_Refresh_DCA_MintAndPersist(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "meta-test.json")
	initialContent := `{"type":"meta","auth_kind":"oauth","access_token":"dca:initial-dca","dca_token":"dca:initial-dca","expired":"2020-01-01T00:00:00Z","request-retry":3}`
	if err := os.WriteFile(authFilePath, []byte(initialContent), 0600); err != nil {
		t.Fatalf("failed to write initial auth file: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"api_key":        "LLM|persisted-minted-key",
				"user_email":     "engineer@meta.com",
				"user_full_name": "Meta Engineer",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("META_MINT_URL", server.URL+"/key")

	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			cliproxyauth.AttributePath: authFilePath,
		},
		Metadata: map[string]any{
			"dca_token": "dca:initial-dca",
			"expired":   "2020-01-01T00:00:00Z",
		},
	}

	exec := NewMetaExecutor(&config.Config{})
	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("exec.Refresh error: %v", err)
	}

	if refreshed.Metadata["api_key"] != "LLM|persisted-minted-key" {
		t.Errorf("expected api_key LLM|persisted-minted-key in metadata, got %v", refreshed.Metadata["api_key"])
	}
	if refreshed.Attributes["api_key"] != "LLM|persisted-minted-key" {
		t.Errorf("expected api_key LLM|persisted-minted-key in attributes, got %v", refreshed.Attributes["api_key"])
	}
	if _, hasExpired := refreshed.Metadata["expired"]; hasExpired {
		t.Errorf("expected expired to be removed from metadata after minting")
	}

	// Verify durable file persistence on disk
	diskBytes, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("failed to read persisted file: %v", errRead)
	}
	var diskData map[string]any
	if errJSON := json.Unmarshal(diskBytes, &diskData); errJSON != nil {
		t.Fatalf("failed to parse persisted JSON: %v", errJSON)
	}
	if diskData["api_key"] != "LLM|persisted-minted-key" {
		t.Errorf("expected persisted api_key LLM|persisted-minted-key, got %v", diskData["api_key"])
	}
	if diskData["access_token"] != "LLM|persisted-minted-key" {
		t.Errorf("expected persisted access_token LLM|persisted-minted-key, got %v", diskData["access_token"])
	}
	// Verify existing properties preserved
	if diskData["request-retry"] != float64(3) {
		t.Errorf("expected preserved request-retry: 3, got %v", diskData["request-retry"])
	}
}

func TestMetaExecutor_Refresh_SingleflightAndMultiAccount(t *testing.T) {
	var count1, count2 int64

	serverStarted := make(chan struct{})
	releaseServer := make(chan struct{})
	var startOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key" {
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			dca := req["dca_token"]
			w.Header().Set("Content-Type", "application/json")
			if dca == "dca:acct1" {
				atomic.AddInt64(&count1, 1)
				startOnce.Do(func() { close(serverStarted) })
				<-releaseServer
				_ = json.NewEncoder(w).Encode(map[string]string{
					"api_key":    "LLM|key-acct1",
					"user_email": "acct1@meta.com",
				})
				return
			}
			if dca == "dca:acct2" {
				atomic.AddInt64(&count2, 1)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"api_key":    "LLM|key-acct2",
					"user_email": "acct2@meta.com",
				})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("META_MINT_URL", server.URL+"/key")
	exec := NewMetaExecutor(&config.Config{})

	// 10 concurrent refreshes for account 1
	var wg sync.WaitGroup
	auth1 := &cliproxyauth.Auth{
		Provider: "meta",
		Metadata: map[string]any{"dca_token": "dca:acct1"},
	}

	const goroutines = 10
	var entryWg sync.WaitGroup
	entryWg.Add(goroutines)
	ready := make(chan struct{})
	var inFlight sync.WaitGroup
	inFlight.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entryWg.Done()
			<-ready
			inFlight.Done()
			res, err := exec.Refresh(context.Background(), auth1.Clone())
			if err != nil {
				t.Errorf("Refresh acct1 error: %v", err)
			}
			if res != nil && res.Metadata["api_key"] != "LLM|key-acct1" {
				t.Errorf("expected LLM|key-acct1, got %v", res.Metadata["api_key"])
			}
		}()
	}

	// Release all goroutines simultaneously.
	entryWg.Wait()
	close(ready)

	// Wait until the singleflight mint request has arrived at the server.
	<-serverStarted
	inFlight.Wait()

	// Allow pending goroutines to enter singleflight.Do while the server holds the in-flight request.
	for i := 0; i < 50; i++ {
		runtime.Gosched()
	}

	// Release the HTTP server handler to complete the single in-flight mint.
	close(releaseServer)
	wg.Wait()

	if totalMint1 := atomic.LoadInt64(&count1); totalMint1 != 1 {
		t.Errorf("expected exactly 1 singleflight mint request for acct1, got %d", totalMint1)
	}

	// Account 2 refreshes independently
	auth2 := &cliproxyauth.Auth{
		Provider: "meta",
		Metadata: map[string]any{"dca_token": "dca:acct2"},
	}
	res2, err2 := exec.Refresh(context.Background(), auth2.Clone())
	if err2 != nil {
		t.Fatalf("Refresh acct2 error: %v", err2)
	}
	if res2.Metadata["api_key"] != "LLM|key-acct2" {
		t.Errorf("expected LLM|key-acct2, got %v", res2.Metadata["api_key"])
	}
	if totalMint2 := atomic.LoadInt64(&count2); totalMint2 != 1 {
		t.Errorf("expected 1 mint request for acct2, got %d", totalMint2)
	}
}

func TestMetaExecutor_Execute_DCARecovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"api_key": "LLM|auto-recovered-key",
			})
			return
		}
		if r.URL.Path == "/chat/completions" {
			if r.Header.Get("Authorization") != "Bearer LLM|auto-recovered-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "chatcmpl-123",
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   "muse-spark-1.3",
				"choices": []map[string]any{
					{
						"index":         0,
						"message":       map[string]any{"role": "assistant", "content": "hello world"},
						"finish_reason": "stop",
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("META_MINT_URL", server.URL+"/key")

	cfg := &config.Config{}
	exec := NewMetaExecutor(cfg)

	// Auth has ONLY a DCA token
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Metadata: map[string]any{
			"dca_token": "dca:execute-recover-me",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	}

	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute with DCA-only auth error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Errorf("expected non-empty payload, got empty")
	}
	if auth.Metadata["api_key"] != "LLM|auto-recovered-key" {
		t.Errorf("expected auth to be enriched with minted api_key, got %v", auth.Metadata["api_key"])
	}
}

func TestMetaExecutorManagerRecoversUnauthorizedKey(t *testing.T) {
	var mints atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/key" {
			mints.Add(1)
			_, _ = w.Write([]byte(`{"api_key":"LLM|replacement"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer LLM|replacement" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"key revoked"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL+"/key")
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewMetaExecutor(&config.Config{}))
	auth := &cliproxyauth.Auth{ID: "meta-401-recovery", Provider: "meta", Metadata: map[string]any{"access_token": "LLM|revoked", "api_key": "LLM|revoked", "dca_token": "dca:valid", "auth_kind": "oauth"}, Attributes: map[string]string{"base_url": server.URL}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "meta", []*registry.ModelInfo{{ID: "muse-spark-1.3"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}]}`)}
	_, err := manager.Execute(context.Background(), []string{"meta"}, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("401 recovery failed: %v", err)
	}
	if mints.Load() != 1 {
		t.Fatalf("mint count = %d, want 1", mints.Load())
	}
}

func TestMetaExecutorRefreshUsesMintedBaseURL(t *testing.T) {
	for _, tc := range []struct{ name, minted, want string }{
		{"custom", " https://regional.meta.example/v1 ", "https://regional.meta.example/v1"},
		{"omitted", "", "https://previous.meta.example/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"api_key": "LLM|replacement", "base_url": tc.minted})
			}))
			defer server.Close()
			t.Setenv("META_MINT_URL", server.URL)
			path := filepath.Join(t.TempDir(), "meta.json")
			auth := &cliproxyauth.Auth{Provider: "meta", Metadata: map[string]any{"dca_token": "dca:test", "base_url": "https://previous.meta.example/v1"}, Attributes: map[string]string{"base_url": "https://previous.meta.example/v1", cliproxyauth.AttributePath: path}}
			updated, err := NewMetaExecutor(nil).Refresh(context.Background(), auth)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := metaCreds(updated); got != tc.want {
				t.Errorf("request base URL = %q, want %q", got, tc.want)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved map[string]any
			if err = json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if saved["base_url"] != tc.want {
				t.Errorf("saved base URL = %v, want %q", saved["base_url"], tc.want)
			}
		})
	}
}

func TestMetaExecutor_ExecuteStreamRateLimit(t *testing.T) {
	now := time.Now()
	resetEpoch := now.Add(2 * time.Hour).Unix()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(mapToJSON(map[string]any{
			"error": map[string]any{
				"code":      "rate_limit_exceeded",
				"message":   "Subscription quota exhausted. Please try again later.",
				"resets_at": resetEpoch,
				"type":      "rate_limit_error",
			},
		})))
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}]}`),
	}

	_, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("expected error from 429 streaming response")
	}

	var se statusErr
	if !errors.As(err, &se) {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}
	if se.StatusCode() != http.StatusTooManyRequests {
		t.Errorf("expected 429 status code, got %d", se.StatusCode())
	}
	if se.RetryAfter() == nil {
		t.Fatalf("expected non-nil RetryAfter")
	}

	var scoped interface{ IsCredentialScoped() bool }
	if !errors.As(err, &scoped) || !scoped.IsCredentialScoped() {
		t.Fatalf("expected streaming rate limit error to be credential scoped, got %T: %v", err, err)
	}
}

func TestMetaExecutor_RequestAuthPreparer(t *testing.T) {
	var mints atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/key" {
			mints.Add(1)
			_, _ = w.Write([]byte(`{"api_key":"LLM|prepared-key"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer LLM|prepared-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	defer server.Close()

	t.Setenv("META_MINT_URL", server.URL+"/key")
	exec := NewMetaExecutor(&config.Config{})

	authWithKey := &cliproxyauth.Auth{Provider: "meta", Attributes: map[string]string{"api_key": "exists"}}
	if exec.ShouldPrepareRequestAuth(authWithKey) {
		t.Errorf("ShouldPrepareRequestAuth should be false when api_key is present")
	}

	authNoDCA := &cliproxyauth.Auth{Provider: "meta"}
	if exec.ShouldPrepareRequestAuth(authNoDCA) {
		t.Errorf("ShouldPrepareRequestAuth should be false when dca_token is missing")
	}

	authConfigDCA := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":                    "dca:invalid-for-config",
			cliproxyauth.AttributeSource: "config:meta[0]",
		},
	}
	if exec.ShouldPrepareRequestAuth(authConfigDCA) {
		t.Errorf("ShouldPrepareRequestAuth should be false for config API key auths")
	}

	authDCAOnly := &cliproxyauth.Auth{
		ID:       "meta-prep-test",
		Provider: "meta",
		Metadata: map[string]any{
			"dca_token": "dca:valid",
			"auth_kind": "oauth",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}
	if !exec.ShouldPrepareRequestAuth(authDCAOnly) {
		t.Errorf("ShouldPrepareRequestAuth should be true when only dca_token is present")
	}

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	if _, err := manager.Register(context.Background(), authDCAOnly); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authDCAOnly.ID, "meta", []*registry.ModelInfo{{ID: "muse-spark-1.3"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authDCAOnly.ID) })

	req := cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}]}`),
	}

	resp, err := manager.Execute(context.Background(), []string{"meta"}, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if string(resp.Payload) == "" {
		t.Errorf("expected non-empty response payload")
	}
	if mints.Load() != 1 {
		t.Fatalf("mint count = %d, want 1", mints.Load())
	}

	stored, ok := manager.GetByID(authDCAOnly.ID)
	if !ok || stored == nil {
		t.Fatalf("stored auth not found in manager")
	}
	if key, _ := stored.Metadata["api_key"].(string); key != "LLM|prepared-key" {
		t.Errorf("expected stored api_key 'LLM|prepared-key', got %q", key)
	}

	_, err = manager.Execute(context.Background(), []string{"meta"}, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("second execute failed: %v", err)
	}
	if mints.Load() != 1 {
		t.Fatalf("mint count after second request = %d, want 1", mints.Load())
	}
}
