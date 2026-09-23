package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDeviceFlowStartDoesNotExchange(t *testing.T) {
	t.Run("xai", func(t *testing.T) {
		var polls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/oauth2/token" {
				polls.Add(1)
			}
			writeXAIDeviceServer(w, r, 0)
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		flow, err := (XAIAuthenticator{httpClient: rewriteClient(server)}).StartDeviceFlow(ctx, &config.Config{})
		if err != nil {
			t.Fatalf("StartDeviceFlow: %v", err)
		}
		if flow.UserCode != "ABCD-1234" || flow.DeviceCode != "device-abc" || flow.VerificationURIComplete == "" {
			t.Fatalf("flow = %+v", flow)
		}
		if polls.Load() != 0 {
			t.Fatalf("start exchanged the device code (%d token calls)", polls.Load())
		}
	})

	t.Run("meta", func(t *testing.T) {
		var polls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/token") || strings.Contains(r.URL.Path, "/muse-code/") {
				polls.Add(1)
			}
			writeMetaDeviceServer(w, r, 0)
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		flow, err := (MetaAuthenticator{httpClient: rewriteClient(server)}).StartDeviceFlow(ctx, &config.Config{})
		if err != nil {
			t.Fatalf("StartDeviceFlow: %v", err)
		}
		if flow.UserCode != "TEST-1234" || flow.DeviceCode != "meta-device" {
			t.Fatalf("flow = %+v", flow)
		}
		if polls.Load() != 0 {
			t.Fatalf("start exchanged the device code (%d calls)", polls.Load())
		}
	})

	t.Run("codex", func(t *testing.T) {
		var polls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/accounts/deviceauth/usercode" {
				polls.Add(1)
			}
			writeCodexDeviceServer(w, r, 0)
		}))
		defer server.Close()

		authn := NewCodexAuthenticator()
		authn.httpClient = rewriteClient(server)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		flow, err := authn.StartDeviceFlow(ctx, &config.Config{})
		if err != nil {
			t.Fatalf("StartDeviceFlow: %v", err)
		}
		if flow.UserCode != "CODE-1234" || flow.DeviceAuthID != "auth-id-1" || flow.VerificationURI == "" {
			t.Fatalf("flow = %+v", flow)
		}
		if polls.Load() != 0 {
			t.Fatalf("start exchanged the device code (%d calls)", polls.Load())
		}
	})
}

func TestDeviceFlowPollPendingDoesNotBlock(t *testing.T) {
	t.Run("xai", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeXAIDeviceServer(w, r, 1)
		}))
		defer server.Close()
		authn := XAIAuthenticator{httpClient: rewriteClient(server)}
		flow, err := authn.StartDeviceFlow(context.Background(), &config.Config{})
		if err != nil {
			t.Fatalf("StartDeviceFlow: %v", err)
		}
		started := time.Now()
		polled, err := authn.PollDeviceFlow(context.Background(), &config.Config{}, flow)
		if err != nil {
			t.Fatalf("PollDeviceFlow: %v", err)
		}
		if polled == nil || !polled.Pending || polled.Auth != nil {
			t.Fatalf("poll = %+v, want pending", polled)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatalf("pending poll blocked for %s", time.Since(started))
		}
	})

	t.Run("meta", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeMetaDeviceServer(w, r, 1)
		}))
		defer server.Close()
		authn := MetaAuthenticator{httpClient: rewriteClient(server)}
		flow, err := authn.StartDeviceFlow(context.Background(), &config.Config{})
		if err != nil {
			t.Fatalf("StartDeviceFlow: %v", err)
		}
		started := time.Now()
		polled, err := authn.PollDeviceFlow(context.Background(), &config.Config{}, flow)
		if err != nil {
			t.Fatalf("PollDeviceFlow: %v", err)
		}
		if polled == nil || !polled.Pending || polled.Auth != nil {
			t.Fatalf("poll = %+v, want pending", polled)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatalf("pending poll blocked for %s", time.Since(started))
		}
	})

	t.Run("codex", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeCodexDeviceServer(w, r, 1)
		}))
		defer server.Close()
		authn := NewCodexAuthenticator()
		authn.httpClient = rewriteClient(server)
		flow, err := authn.StartDeviceFlow(context.Background(), &config.Config{})
		if err != nil {
			t.Fatalf("StartDeviceFlow: %v", err)
		}
		started := time.Now()
		polled, err := authn.PollDeviceFlow(context.Background(), &config.Config{}, flow)
		if err != nil {
			t.Fatalf("PollDeviceFlow: %v", err)
		}
		if polled == nil || !polled.Pending || polled.Auth != nil {
			t.Fatalf("poll = %+v, want pending", polled)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatalf("pending poll blocked for %s", time.Since(started))
		}
	})
}

func TestDeviceFlowLoginUsesStartAndComplete(t *testing.T) {
	t.Run("xai", func(t *testing.T) {
		var polls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pendingPolls := 0
			if r.URL.Path == "/oauth2/token" {
				pendingPolls = int(polls.Add(1))
			}
			writeXAIDeviceServer(w, r, pendingPolls)
		}))
		defer server.Close()

		authn := XAIAuthenticator{httpClient: rewriteClient(server), pollIntervalOverride: time.Millisecond}
		record, err := authn.Login(context.Background(), &config.Config{}, &LoginOptions{NoBrowser: true})
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		if record.Metadata["access_token"] != "access-1" || record.Metadata["refresh_token"] != "refresh-1" {
			t.Fatalf("metadata = %#v", record.Metadata)
		}
		if record.Provider != "xai" {
			t.Fatalf("provider = %q", record.Provider)
		}
		if polls.Load() < 2 {
			t.Fatalf("token polls = %d, want pending then exchange", polls.Load())
		}
	})

	t.Run("meta", func(t *testing.T) {
		var polls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pendingPolls := 0
			if strings.Contains(r.URL.Path, "/oidc/device/token") {
				pendingPolls = int(polls.Add(1))
			}
			writeMetaDeviceServer(w, r, pendingPolls)
		}))
		defer server.Close()

		authn := MetaAuthenticator{httpClient: rewriteClient(server), pollIntervalOverride: time.Millisecond}
		record, err := authn.Login(context.Background(), &config.Config{}, &LoginOptions{NoBrowser: true})
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		if record.Metadata["api_key"] != "LLM|minted" || record.Metadata["dca_token"] != "dca-access" {
			t.Fatalf("metadata = %#v", record.Metadata)
		}
		if record.Metadata["email"] != "user@meta.com" {
			t.Fatalf("email = %#v", record.Metadata["email"])
		}
		if polls.Load() < 2 {
			t.Fatalf("token polls = %d, want pending then exchange", polls.Load())
		}
	})

	t.Run("codex", func(t *testing.T) {
		var polls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pendingPolls := 0
			if r.URL.Path == "/api/accounts/deviceauth/token" {
				pendingPolls = int(polls.Add(1))
			}
			writeCodexDeviceServer(w, r, pendingPolls)
		}))
		defer server.Close()

		authn := NewCodexAuthenticator()
		authn.httpClient = rewriteClient(server)
		authn.pollIntervalOverride = time.Millisecond
		record, err := authn.Login(context.Background(), &config.Config{}, &LoginOptions{
			NoBrowser: true,
			Metadata:  map[string]string{codexLoginModeMetadataKey: codexLoginModeDevice},
		})
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		storage, ok := record.Storage.(*codexauth.CodexTokenStorage)
		if !ok || storage.AccessToken != "access-1" || storage.Email != "user@openai.com" {
			t.Fatalf("storage = %#v", record.Storage)
		}
		if polls.Load() < 2 {
			t.Fatalf("token polls = %d, want pending then exchange", polls.Load())
		}
	})
}

func TestPollDeviceFlowExchangesApprovedCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeXAIDeviceServer(w, r, 2)
	}))
	defer server.Close()
	authn := XAIAuthenticator{httpClient: rewriteClient(server)}
	flow, err := authn.StartDeviceFlow(context.Background(), &config.Config{})
	if err != nil {
		t.Fatalf("StartDeviceFlow: %v", err)
	}
	polled, err := authn.PollDeviceFlow(context.Background(), &config.Config{}, flow)
	if err != nil {
		t.Fatalf("PollDeviceFlow: %v", err)
	}
	if polled == nil || polled.Pending || polled.Auth == nil || polled.Auth.Metadata["access_token"] != "access-1" {
		t.Fatalf("poll = %+v", polled)
	}

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
			return
		}
		writeXAIDeviceServer(w, r, 0)
	}))
	defer denied.Close()
	authn.httpClient = rewriteClient(denied)
	flow, err = authn.StartDeviceFlow(context.Background(), &config.Config{})
	if err != nil {
		t.Fatalf("StartDeviceFlow: %v", err)
	}
	polled, err = authn.PollDeviceFlow(context.Background(), &config.Config{}, flow)
	if err == nil || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("PollDeviceFlow error = %v, want authorization denied", err)
	}
	if polled != nil && polled.Auth != nil {
		t.Fatalf("denied poll returned auth %+v", polled.Auth)
	}
}

func TestCompleteDeviceFlowExchangesCodexCode(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pendingPolls := 0
		if r.URL.Path == "/api/accounts/deviceauth/token" {
			pendingPolls = int(polls.Add(1))
		}
		writeCodexDeviceServer(w, r, pendingPolls)
	}))
	defer server.Close()

	authn := NewCodexAuthenticator()
	authn.httpClient = rewriteClient(server)
	authn.pollIntervalOverride = time.Millisecond
	flow, err := authn.StartDeviceFlow(context.Background(), &config.Config{})
	if err != nil {
		t.Fatalf("StartDeviceFlow: %v", err)
	}
	if polls.Load() != 0 {
		t.Fatalf("start polled %d times", polls.Load())
	}
	record, err := authn.CompleteDeviceFlow(context.Background(), &config.Config{}, flow)
	if err != nil {
		t.Fatalf("CompleteDeviceFlow: %v", err)
	}
	storage, ok := record.Storage.(*codexauth.CodexTokenStorage)
	if !ok || storage.AccessToken != "access-1" {
		t.Fatalf("storage = %#v", record.Storage)
	}
	if polls.Load() < 2 {
		t.Fatalf("token polls = %d, want pending then exchange", polls.Load())
	}
}

func writeXAIDeviceServer(w http.ResponseWriter, r *http.Request, pollNumber int) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": "https://auth.x.ai/oauth2/device/code",
			"token_endpoint":                "https://auth.x.ai/oauth2/token",
		})
	case "/oauth2/device/code":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device-abc",
			"user_code":                 "ABCD-1234",
			"verification_uri":          "https://accounts.x.ai/oauth2/device",
			"verification_uri_complete": "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234",
			"expires_in":                1800,
			"interval":                  5,
		})
	case "/oauth2/token":
		if pollNumber < 2 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"id_token":      testJWT(`{"email":"user@x.ai","sub":"sub-1"}`),
		})
	default:
		http.NotFound(w, r)
	}
}

func writeMetaDeviceServer(w http.ResponseWriter, r *http.Request, pollNumber int) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(r.URL.Path, "/oidc/device/authorization"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "meta-device",
			"user_code":                 "TEST-1234",
			"verification_uri":          "https://auth.meta.com/device",
			"verification_uri_complete": "https://auth.meta.com/device?user_code=TEST-1234",
			"expires_in":                900,
			"interval":                  5,
		})
	case strings.Contains(r.URL.Path, "/oidc/device/token"):
		if pollNumber < 2 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "dca-access",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	case strings.Contains(r.URL.Path, "/muse-code/key"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_key":        "LLM|minted",
			"user_email":     "user@meta.com",
			"user_full_name": "Meta User",
		})
	default:
		http.NotFound(w, r)
	}
}

func writeCodexDeviceServer(w http.ResponseWriter, r *http.Request, pollNumber int) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/accounts/deviceauth/usercode":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_auth_id": "auth-id-1",
			"user_code":      "CODE-1234",
			"interval":       5,
		})
	case "/api/accounts/deviceauth/token":
		if pollNumber < 2 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_code": "auth-code",
			"code_verifier":      "verifier",
			"code_challenge":     "challenge",
		})
	case "/oauth/token":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"id_token":      testJWT(`{"email":"user@openai.com","https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_plan_type":"plus"}}`),
			"expires_in":    3600,
		})
	default:
		http.NotFound(w, r)
	}
}

func testJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return header + "." + body + ".sig"
}

func rewriteClient(server *httptest.Server) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = "http"
			clone.URL.Host = server.Listener.Addr().String()
			clone.Host = clone.URL.Host
			return server.Client().Transport.RoundTrip(clone)
		}),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
