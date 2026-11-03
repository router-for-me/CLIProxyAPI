package executor

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	publicationAccessToken  = "committed-access"
	publicationRefreshToken = "committed-refresh"
	mockAccessToken         = "new-mock-access-token"
	mockRefreshToken        = "new-mock-refresh-token"
)

func TestCodexExecutorRefreshRepairsCommittedPublicationBeforeReadingDisk(t *testing.T) {
	proxyURL, refreshTokens, teardown := startCodexPublicationOAuthServers(t, makeTestCodexRefreshJWT("plus", "acc-publication"))
	defer teardown()
	authDir := t.TempDir()
	path := filepath.Join(authDir, "codex.json")
	oldMirror := []byte(`{"type":"codex","access_token":"stale-mirror-access","refresh_token":"stale-mirror-refresh","expired":"2099-01-01T00:00:00Z"}`)
	committed := []byte(`{"type":"codex","access_token":"committed-access","refresh_token":"committed-refresh","expired":"2099-01-01T00:00:00Z"}`)
	if err := os.WriteFile(path, oldMirror, 0o600); err != nil {
		t.Fatal(err)
	}
	prepareCommittedCodexPublication(t, path, committed)

	refreshed, err := NewCodexExecutor(&config.Config{AuthDir: authDir}).Refresh(t.Context(), codexPublicationRefreshAuth(proxyURL))
	if err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}
	assertCodexPublicationRefreshRequest(t, refreshTokens, publicationRefreshToken)
	assertCodexPublicationTokens(t, refreshed.Metadata, mockAccessToken, mockRefreshToken)
	if authpublication.HasPending(path) {
		t.Fatal("Refresh() left a confirmed publication overlay instead of repairing the mirror")
	}
	assertCodexPublicationDiskTokens(t, path, mockAccessToken, mockRefreshToken)
}

func TestCodexExecutorRefreshFindsCommittedPublicationWithoutFirstMirror(t *testing.T) {
	proxyURL, refreshTokens, teardown := startCodexPublicationOAuthServers(t, makeTestCodexRefreshJWT("plus", "acc-publication"))
	defer teardown()
	authDir := t.TempDir()
	path := filepath.Join(authDir, "codex.json")
	committed := []byte(`{"type":"codex","access_token":"committed-access","refresh_token":"committed-refresh","expired":"2099-01-01T00:00:00Z"}`)
	prepareCommittedCodexPublication(t, path, committed)

	refreshed, err := NewCodexExecutor(&config.Config{AuthDir: authDir}).Refresh(t.Context(), codexPublicationRefreshAuth(proxyURL))
	if err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}
	assertCodexPublicationRefreshRequest(t, refreshTokens, publicationRefreshToken)
	assertCodexPublicationTokens(t, refreshed.Metadata, mockAccessToken, mockRefreshToken)
	if authpublication.HasPending(path) {
		t.Fatal("Refresh() left a confirmed publication overlay instead of repairing the first mirror")
	}
	assertCodexPublicationDiskTokens(t, path, mockAccessToken, mockRefreshToken)
}

func TestCodexExecutorRefreshFailsClosedForUnrepairablePublication(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt bool
	}{
		{name: "unconfirmed"},
		{name: "corrupt", corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyURL, refreshTokens, teardown := startCodexPublicationOAuthServers(t, makeTestCodexRefreshJWT("plus", "acc-publication"))
			defer teardown()
			authDir := t.TempDir()
			path := filepath.Join(authDir, "codex.json")
			stale := []byte(`{"type":"codex","access_token":"stale-mirror-access","refresh_token":"stale-mirror-refresh","expired":"2099-01-01T00:00:00Z"}`)
			if err := os.WriteFile(path, stale, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := authpublication.Prepare(path, []byte(`{"type":"codex","access_token":"committed-access","refresh_token":"committed-refresh"}`))
			if err != nil {
				t.Fatalf("prepare publication: %v", err)
			}
			if tc.corrupt {
				entries, err := os.ReadDir(filepath.Join(authDir, authpublication.DirectoryName))
				if err != nil || len(entries) != 1 {
					t.Fatalf("publication records = %v, %v", entries, err)
				}
				marker := filepath.Join(authDir, authpublication.DirectoryName, entries[0].Name())
				if err := os.WriteFile(marker, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			refreshed, err := NewCodexExecutor(&config.Config{AuthDir: authDir}).Refresh(t.Context(), codexPublicationRefreshAuth(proxyURL))
			if err == nil || !strings.Contains(err.Error(), "repair credential publication") {
				t.Fatalf("Refresh() error = %v, want publication repair failure", err)
			}
			if refreshed != nil && refreshed.Metadata["access_token"] == "stale-mirror-access" {
				t.Fatal("Refresh() adopted the stale mirror after publication repair failed")
			}
			assertNoCodexPublicationRefreshRequest(t, refreshTokens)
			saved, err := os.ReadFile(path)
			if err != nil || string(saved) != string(stale) {
				t.Fatalf("unrepairable publication changed stale mirror: %v", err)
			}
		})
	}
}

func TestCodexExecutorRefreshFailsWhenCommittedPublicationMirrorCannotBeWritten(t *testing.T) {
	proxyURL, refreshTokens, teardown := startCodexPublicationOAuthServers(t, makeTestCodexRefreshJWT("plus", "acc-publication"))
	defer teardown()
	authDir := t.TempDir()
	path := filepath.Join(authDir, "codex.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"stale-mirror-access","refresh_token":"stale-mirror-refresh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prepareCommittedCodexPublication(t, path, []byte(`{"type":"codex","access_token":"committed-access","refresh_token":"committed-refresh"}`))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	refreshed, err := NewCodexExecutor(&config.Config{AuthDir: authDir}).Refresh(t.Context(), codexPublicationRefreshAuth(proxyURL))
	if err == nil || !strings.Contains(err.Error(), "repair credential publication") {
		t.Fatalf("Refresh() error = %v, want mirror repair failure", err)
	}
	if refreshed != nil {
		t.Fatalf("Refresh() returned credentials after mirror repair failed")
	}
	assertNoCodexPublicationRefreshRequest(t, refreshTokens)
	if !authpublication.HasPending(path) {
		t.Fatal("failed mirror repair discarded the committed publication record")
	}
}

func prepareCommittedCodexPublication(t *testing.T, path string, data []byte) {
	t.Helper()
	pending, err := authpublication.Prepare(path, data)
	if err != nil {
		t.Fatalf("prepare committed publication: %v", err)
	}
	if err := pending.Commit(); err != nil {
		t.Fatalf("commit publication: %v", err)
	}
}

func codexPublicationRefreshAuth(proxyURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "codex.json",
		FileName: "codex.json",
		Provider: "codex",
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  publicationAccessToken,
			"refresh_token": publicationRefreshToken,
			"expired":       "2099-01-01T00:00:00Z",
		},
	}
}

func assertCodexPublicationRefreshRequest(t *testing.T, refreshTokens <-chan string, want string) {
	t.Helper()
	select {
	case got := <-refreshTokens:
		if got != want {
			t.Fatalf("OAuth refresh token = %q, want committed token", got)
		}
	default:
		t.Fatal("Refresh() did not call OAuth with the committed credential")
	}
	select {
	case got := <-refreshTokens:
		t.Fatalf("Refresh() made an unexpected additional OAuth request with token %q", got)
	default:
	}
}

func assertNoCodexPublicationRefreshRequest(t *testing.T, refreshTokens <-chan string) {
	t.Helper()
	select {
	case got := <-refreshTokens:
		t.Fatalf("Refresh() called OAuth with token %q after publication repair failed", got)
	default:
	}
}

func assertCodexPublicationTokens(t *testing.T, metadata map[string]any, accessToken, refreshToken string) {
	t.Helper()
	if got := metadata["access_token"]; got != accessToken {
		t.Fatalf("runtime access_token = %v, want current grant", got)
	}
	if got := metadata["refresh_token"]; got != refreshToken {
		t.Fatalf("runtime refresh_token = %v, want current grant", got)
	}
}

func assertCodexPublicationDiskTokens(t *testing.T, path, accessToken, refreshToken string) {
	t.Helper()
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ojson.ValidBytes(saved) {
		t.Fatal("credential mirror is invalid JSON")
	}
	metadata := ojson.ParseBytes(saved)
	if got := metadata.Get("access_token").String(); got != accessToken {
		t.Fatalf("disk access_token = %q, want current grant", got)
	}
	if got := metadata.Get("refresh_token").String(); got != refreshToken {
		t.Fatalf("disk refresh_token = %q, want current grant", got)
	}
}

func startCodexPublicationOAuthServers(t *testing.T, idToken string) (proxyURL string, refreshTokens <-chan string, teardown func()) {
	t.Helper()
	seenTokens := make(chan string, 8)
	mockAuthServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		seenTokens <- r.Form.Get("refresh_token")
		w.Header().Set("Content-Type", "application/json")
		response := ojson.NewDocString(`{}`)
		for _, field := range []struct {
			name  string
			value any
		}{
			{name: "access_token", value: mockAccessToken},
			{name: "refresh_token", value: mockRefreshToken},
			{name: "id_token", value: idToken},
			{name: "token_type", value: "Bearer"},
			{name: "expires_in", value: 3600},
		} {
			if err := response.Set(field.name, field.value); err != nil {
				t.Errorf("set OAuth response field %s: %v", field.name, err)
				return
			}
		}
		if _, err := w.Write(response.Bytes()); err != nil {
			t.Errorf("write OAuth response: %v", err)
		}
	}))

	mockProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "proxy only supports CONNECT", http.StatusBadRequest)
			return
		}
		destConn, errDial := net.Dial("tcp", mockAuthServer.Listener.Addr().String())
		if errDial != nil {
			http.Error(w, errDial.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			_ = destConn.Close()
			http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
			return
		}
		clientConn, _, errHijack := hijacker.Hijack()
		if errHijack != nil {
			_ = destConn.Close()
			return
		}
		go func() {
			defer func() { _ = destConn.Close() }()
			defer func() { _ = clientConn.Close() }()
			_, _ = io.Copy(destConn, clientConn)
		}()
		go func() {
			defer func() { _ = destConn.Close() }()
			defer func() { _ = clientConn.Close() }()
			_, _ = io.Copy(clientConn, destConn)
		}()
	}))

	origTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	teardown = func() {
		http.DefaultTransport = origTransport
		mockProxy.Close()
		mockAuthServer.Close()
	}
	return mockProxy.URL, seenTokens, teardown
}
