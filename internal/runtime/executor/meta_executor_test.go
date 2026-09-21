package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
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

		if r.URL.Path == "/responses" {
			// Check user agent
			if ua := r.Header.Get("User-Agent"); ua != metaUserAgent {
				t.Errorf("expected User-Agent %s, got %s", metaUserAgent, ua)
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

func writeMetaResponsesOK(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	payload, _ := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     "resp_1",
			"object": "response",
			"status": "completed",
			"model":  "muse-spark-1.3",
			"output": []map[string]any{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": text},
					},
				},
			},
			"usage": map[string]any{
				"input_tokens":  1,
				"output_tokens": 1,
				"total_tokens":  2,
			},
		},
	})
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte("\n\n"))
}

func TestMetaExecutor_Refresh_RequiresManagerAcceptance(t *testing.T) {
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
		ID:       "meta-test.json",
		FileName: "meta-test.json",
		Provider: "meta",
		Attributes: map[string]string{
			cliproxyauth.AttributePath: authFilePath,
		},
		Metadata: map[string]any{
			"dca_token": "dca:initial-dca",
			"expired":   "2020-01-01T00:00:00Z",
		},
	}

	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(tempDir)
	manager := cliproxyauth.NewManager(store, nil, nil)
	auth.Metadata["request-retry"] = float64(3)
	auth.Storage = &metaauth.MetaTokenStorage{AccessToken: "dca:initial-dca", DCAToken: "dca:initial-dca", Expired: "2020-01-01T00:00:00Z"}
	auth, errRegister := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	exec := NewMetaExecutor(&config.Config{})
	refreshed, err := exec.Refresh(context.Background(), auth.Clone())
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

	unchanged, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if string(unchanged) != initialContent {
		t.Fatal("executor wrote candidate before manager acceptance")
	}
	live, _ := manager.GetByID(auth.ID)
	if live.Storage.(*metaauth.MetaTokenStorage).AccessToken != "dca:initial-dca" {
		t.Fatal("refresh mutated live token storage")
	}
	if _, err := manager.UpdateRefreshedAuth(context.Background(), auth, refreshed); err != nil {
		t.Fatal(err)
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
	if diskData["request_retry"] != float64(3) {
		t.Errorf("expected preserved request-retry: 3, got %v", diskData["request_retry"])
	}
}

func TestMetaExecutor_PrepareConcurrentAccounts(t *testing.T) {
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
		ID:       "meta-acct1",
		Provider: "meta",
		Metadata: map[string]any{"dca_token": "dca:acct1"},
	}

	manager := cliproxyauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatal(err)
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
			res, err := manager.PrepareRequestAuth(context.Background(), exec, auth1.Clone())
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

	// A second account must complete while the first account's mint is blocked.
	auth2 := &cliproxyauth.Auth{ID: "meta-acct2", Provider: "meta", Metadata: map[string]any{"dca_token": "dca:acct2"}}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatal(err)
	}
	res2, err2 := manager.PrepareRequestAuth(context.Background(), exec, auth2.Clone())
	close(releaseServer)
	wg.Wait()

	if totalMint1 := atomic.LoadInt64(&count1); totalMint1 != 1 {
		t.Errorf("expected exactly 1 singleflight mint request for acct1, got %d", totalMint1)
	}

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
		if r.URL.Path == "/responses" {
			if r.Header.Get("Authorization") != "Bearer LLM|auto-recovered-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeMetaResponsesOK(w, "hello world")
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
		writeMetaResponsesOK(w, "recovered")
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
			if updated.Metadata["base_url"] != tc.want {
				t.Errorf("candidate base URL = %v, want %q", updated.Metadata["base_url"], tc.want)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("refresh created a file: %v", err)
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
		writeMetaResponsesOK(w, "hello")
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
		ID:       "meta-prep-test.json",
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

	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(t.TempDir())
	manager := cliproxyauth.NewManager(store, nil, nil)
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

	reloaded, err := store.List(context.Background())
	if err != nil || len(reloaded) != 1 {
		t.Fatalf("reload auth: %v, records=%d", err, len(reloaded))
	}
	if reloaded[0].Metadata["api_key"] != "LLM|prepared-key" {
		t.Fatal("minted key was not persisted for restart")
	}

	_, err = manager.Execute(context.Background(), []string{"meta"}, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("second execute failed: %v", err)
	}
	if mints.Load() != 1 {
		t.Fatalf("mint count after second request = %d, want 1", mints.Load())
	}
}

func TestMetaMintRejectsRemovedOrReloadedCredential(t *testing.T) {
	for _, prepare := range []bool{false, true} {
		for _, action := range []string{"remove", "reload"} {
			name := action + "/refresh"
			if prepare {
				name = action + "/prepare"
			}
			t.Run(name, func(t *testing.T) {
				started := make(chan struct{})
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(started)
					<-release
					_, _ = w.Write([]byte(`{"api_key":"LLM|obsolete"}`))
				}))
				defer server.Close()
				var releaseOnce sync.Once
				defer releaseOnce.Do(func() { close(release) })
				t.Setenv("META_MINT_URL", server.URL)
				dir := t.TempDir()
				store := sdkauth.NewFileTokenStore()
				store.SetBaseDir(dir)
				manager := cliproxyauth.NewManager(store, nil, nil)
				storage := &metaauth.MetaTokenStorage{AccessToken: "dca:initial", DCAToken: "dca:initial"}
				auth, err := manager.Register(context.Background(), &cliproxyauth.Auth{ID: "meta-lifecycle.json", Provider: "meta", Storage: storage, Metadata: map[string]any{"type": "meta", "access_token": "dca:initial", "dca_token": "dca:initial"}})
				if err != nil {
					t.Fatal(err)
				}
				exec := NewMetaExecutor(nil)
				done := make(chan error, 1)
				go func() {
					if prepare {
						_, err := manager.PrepareRequestAuth(context.Background(), exec, auth.Clone())
						done <- err
						return
					}
					updated, err := exec.Refresh(context.Background(), auth.Clone())
					if err == nil {
						_, err = manager.UpdateRefreshedAuth(context.Background(), auth, updated)
					}
					done <- err
				}()
				<-started
				path := filepath.Join(dir, auth.ID)
				if action == "remove" {
					manager.Remove(context.Background(), auth.ID)
					if err := store.Delete(context.Background(), auth.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					_, err := manager.Register(context.Background(), &cliproxyauth.Auth{ID: auth.ID, Provider: "meta", Metadata: map[string]any{"type": "meta", "api_key": "LLM|reloaded", "access_token": "LLM|reloaded", "dca_token": "dca:reloaded"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				releaseOnce.Do(func() { close(release) })
				err = <-done
				if (prepare || action == "reload") && err == nil {
					t.Fatal("obsolete mint was accepted")
				}
				if storage.AccessToken != "dca:initial" {
					t.Fatal("obsolete mint changed shared login storage")
				}
				raw, errRead := os.ReadFile(path)
				if action == "remove" {
					if !os.IsNotExist(errRead) {
						t.Fatalf("removed auth was recreated: %s, %v", raw, errRead)
					}
					if _, ok := manager.GetByID(auth.ID); ok {
						t.Fatal("removed auth was reinstalled")
					}
				} else {
					if errRead != nil {
						t.Fatal(errRead)
					}
					var saved map[string]any
					if err := json.Unmarshal(raw, &saved); err != nil {
						t.Fatal(err)
					}
					if saved["api_key"] != "LLM|reloaded" {
						t.Fatalf("obsolete mint overwrote reload: %s", raw)
					}
				}
			})
		}
	}
}

func TestMetaExecutor_ExecuteShapesResponsesRequest(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotUA string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		writeMetaResponsesOK(w, "ok")
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/responses" {
		t.Fatalf("path = %q, want /responses", gotPath)
	}
	if gotAuth != "Bearer meta-token" {
		t.Fatalf("Authorization = %q, want Bearer meta-token", gotAuth)
	}
	if gotUA != metaUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUA, metaUserAgent)
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("request still uses chat completions messages: %s", gotBody)
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("request missing responses input: %s", gotBody)
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("stream = false, want true")
	}
	if gjson.GetBytes(gotBody, "model").String() != "muse-spark-1.3" {
		t.Fatalf("model = %q, want muse-spark-1.3", gjson.GetBytes(gotBody, "model").String())
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty translated payload")
	}
}

func TestMetaExecutor_ExecuteStreamUsesResponses(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeMetaResponsesOK(w, "streamed")
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}

	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if gotPath != "/responses" {
		t.Fatalf("path = %q, want /responses", gotPath)
	}
	if result == nil {
		t.Fatal("expected stream result")
	}
	gotChunk := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if len(chunk.Payload) > 0 {
			gotChunk = true
		}
	}
	if !gotChunk {
		t.Fatal("expected at least one stream payload chunk")
	}
}

func TestMetaExecutor_PreservesPreviousResponseID(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		writeMetaResponsesOK(w, "ok")
	}))
	defer server.Close()

	_, err := NewMetaExecutor(&config.Config{}).Execute(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "meta-token", "base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","input":"hello","previous_response_id":"resp_prev"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(gotBody, "previous_response_id").String(); got != "resp_prev" {
		t.Fatalf("previous_response_id = %q, want resp_prev", got)
	}
}

func TestMetaExecutor_CompactNotSupported(t *testing.T) {
	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "meta-token", "base_url": "https://api.meta.ai/v1"}}
	req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3"}`)}
	opts := cliproxyexecutor.Options{Alt: "responses/compact", SourceFormat: sdktranslator.FromString("openai-response")}

	_, err := exec.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("expected compact execute error")
	}
	var se statusErr
	if !errors.As(err, &se) || se.StatusCode() != http.StatusNotImplemented {
		t.Fatalf("Execute compact error = %v, want 501 statusErr", err)
	}

	_, err = exec.ExecuteStream(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("expected compact stream error")
	}
	if !errors.As(err, &se) || se.StatusCode() != http.StatusNotImplemented {
		t.Fatalf("ExecuteStream compact error = %v, want 501 statusErr", err)
	}
}

func TestMetaExecutor_ExecuteNonStreamMultiEventSSE_RecordsModelAndWarnsOnSubstitution(t *testing.T) {
	multiEventSSE := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"item_0\",\"type\":\"message\",\"role\":\"assistant\"}}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"item_0\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"substituted-meta-model\",\"usage\":{\"total_tokens\":10}}}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(multiEventSSE))
	}))
	defer server.Close()

	const alias = "meta-multi-event-sse-test"
	capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
	})

	hook := new(logtest.Hook)
	log.StandardLogger().AddHook(hook)
	t.Cleanup(func() {
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	})

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}

	ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
	resp, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty translated payload")
	}

	record := capture.await(t)
	if record.Model != "muse-spark-1.3" {
		t.Fatalf("record.Model = %q, want muse-spark-1.3", record.Model)
	}
	if record.ResponseModel != "substituted-meta-model" {
		t.Fatalf("record.ResponseModel = %q, want substituted-meta-model", record.ResponseModel)
	}

	var foundWarning bool
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream served model") && strings.Contains(entry.Message, "substituted-meta-model") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected substitution warning in logs for substituted-meta-model")
	}
}
