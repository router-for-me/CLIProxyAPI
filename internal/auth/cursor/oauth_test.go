package cursor

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps/cursorproto"
	"google.golang.org/protobuf/proto"
)

func TestGenerateAuthParams(t *testing.T) {
	params, err := GenerateAuthParams()
	if err != nil {
		t.Fatalf("GenerateAuthParams() error = %v", err)
	}
	parsed, errParse := url.Parse(params.LoginURL)
	if errParse != nil {
		t.Fatalf("parse login URL: %v", errParse)
	}
	if parsed.Host != "cursor.com" || parsed.Path != "/loginDeepControl" {
		t.Fatalf("unexpected login URL %q", params.LoginURL)
	}
	if parsed.Query().Get("uuid") != params.UUID || parsed.Query().Get("mode") != "login" || parsed.Query().Get("redirectTarget") != "cli" {
		t.Fatalf("unexpected login query: %v", parsed.Query())
	}
	if parsed.Query().Get("challenge") != params.Challenge || params.Verifier == "" {
		t.Fatal("PKCE values are missing")
	}
}

func TestClientPollPendingThenSuccess(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": jwtForTest(time.Now().Add(time.Hour)), "refreshToken": "refresh"})
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), baseURL: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	tokens, err := client.Poll(ctx, "flow", "verifier")
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if tokens.RefreshToken != "refresh" || tokens.AccessToken == "" {
		t.Fatalf("unexpected tokens: %#v", tokens)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestClientRefreshUsesNewAccessAsRefreshFallback(t *testing.T) {
	access := jwtForTest(time.Now().Add(2 * time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request["client_id"] != CLIClientID || request["refresh_token"] != "old-refresh" {
			t.Fatalf("unexpected request: %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": access})
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), baseURL: server.URL}
	tokens, err := client.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if tokens.AccessToken != access || tokens.RefreshToken != access {
		t.Fatalf("unexpected refreshed tokens: %#v", tokens)
	}
}

func TestClientDiscoverModels(t *testing.T) {
	responseBody, errMarshal := proto.Marshal(&cursorproto.GetUsableModelsResponse{Models: []*cursorproto.ModelDetails{
		{ModelId: "gpt-test", DisplayName: "GPT Test"},
		{ModelId: "gpt-test-high", DisplayName: "GPT Test High", ThinkingDetails: &cursorproto.ThinkingDetails{}},
	}})
	if errMarshal != nil {
		t.Fatalf("marshal response: %v", errMarshal)
	}
	framed := make([]byte, len(responseBody)+5)
	binary.BigEndian.PutUint32(framed[1:5], uint32(len(responseBody)))
	copy(framed[5:], responseBody)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization header missing")
		}
		_, _ = w.Write(framed)
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), baseURL: server.URL}
	models, err := client.DiscoverModels(context.Background(), "token")
	if err != nil {
		t.Fatalf("DiscoverModels() error = %v", err)
	}
	if len(models) != 2 || models[1].ID != "gpt-test-high" || !models[1].Thinking {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestCredentialFileNameUsesJWTIdentity(t *testing.T) {
	first := jwtWithClaimsForTest(map[string]any{"sub": "same-user", "email": "one@example.com", "exp": time.Now().Add(time.Hour).Unix()})
	second := jwtWithClaimsForTest(map[string]any{"sub": "same-user", "email": "two@example.com", "exp": time.Now().Add(2 * time.Hour).Unix()})
	if CredentialFileName(first) != CredentialFileName(second) {
		t.Fatal("filename changed for the same JWT subject")
	}
}

func jwtForTest(expiry time.Time) string {
	return jwtWithClaimsForTest(map[string]any{"sub": "test-user", "exp": expiry.Unix()})
}

func jwtWithClaimsForTest(claims map[string]any) string {
	raw, _ := json.Marshal(claims)
	return strings.Join([]string{"header", base64.RawURLEncoding.EncodeToString(raw), "signature"}, ".")
}
