package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestStatusFromHomeErrorCodeMapsAuthenticationErrorToUnauthorized(t *testing.T) {
	if got := statusFromHomeErrorCode("authentication_error"); got != http.StatusUnauthorized {
		t.Fatalf("statusFromHomeErrorCode(authentication_error) = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := statusFromHomeErrorCode("unauthorized"); got != http.StatusUnauthorized {
		t.Fatalf("statusFromHomeErrorCode(unauthorized) = %d, want %d", got, http.StatusUnauthorized)
	}
	for _, code := range []string{"auth_not_found", "auth_unavailable", "refresh_temporarily_unavailable", "refresh_unsupported"} {
		if got := statusFromHomeErrorCode(code); got != http.StatusServiceUnavailable {
			t.Fatalf("statusFromHomeErrorCode(%s) = %d, want %d", code, got, http.StatusServiceUnavailable)
		}
	}
}

type fakeHomeRefreshClient struct {
	calls           atomic.Int32
	authIndex       string
	accessTokenHash string
	raw             []byte
	err             error
}

func (c *fakeHomeRefreshClient) HeartbeatOK() bool {
	return true
}

func (c *fakeHomeRefreshClient) GetRefreshAuth(_ context.Context, authIndex string, accessTokenHash string) ([]byte, error) {
	c.calls.Add(1)
	c.authIndex = authIndex
	c.accessTokenHash = accessTokenHash
	return c.raw, c.err
}

func TestRefreshAuthViaHomePreservesContextErrors(t *testing.T) {
	client := &fakeHomeRefreshClient{err: context.DeadlineExceeded}
	oldCurrentHomeRefreshClient := currentHomeRefreshClient
	currentHomeRefreshClient = func() homeRefreshClient { return client }
	t.Cleanup(func() { currentHomeRefreshClient = oldCurrentHomeRefreshClient })

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	auth := &cliproxyauth.Auth{ID: "home-auth", Index: "home-auth", Provider: "codex"}
	_, handled, errRefresh := RefreshAuthViaHome(context.Background(), cfg, auth)
	if !handled || !errors.Is(errRefresh, context.DeadlineExceeded) {
		t.Fatalf("RefreshAuthViaHome() = handled %v err %v, want true/context.DeadlineExceeded", handled, errRefresh)
	}
}

func TestHomeStatusErrLogDiagnosticSanitizesUpstreamFallback(t *testing.T) {
	errRefresh := homeStatusErr{
		code:     http.StatusBadGateway,
		msg:      "upstream EOF access_token=provider-secret",
		upstream: true,
	}
	diagnostic := errRefresh.LogDiagnostic()
	if diagnostic != "Home refresh upstream response: status=502" || strings.Contains(diagnostic, "provider-secret") {
		t.Fatalf("LogDiagnostic() = %q, want safe upstream fallback", diagnostic)
	}
	if errRefresh.Error() != "upstream EOF access_token=provider-secret" {
		t.Fatalf("Error() = %q, want exact upstream response", errRefresh.Error())
	}
}

func TestRefreshAuthViaHomeMapsTransportFailureToGeneric503(t *testing.T) {
	client := &fakeHomeRefreshClient{err: errors.New("dial failed with provider-secret")}
	oldCurrentHomeRefreshClient := currentHomeRefreshClient
	currentHomeRefreshClient = func() homeRefreshClient { return client }
	t.Cleanup(func() { currentHomeRefreshClient = oldCurrentHomeRefreshClient })

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	auth := &cliproxyauth.Auth{ID: "home-auth", Index: "home-auth", Provider: "codex"}
	_, handled, errRefresh := RefreshAuthViaHome(context.Background(), cfg, auth)
	statusErr, okStatus := errRefresh.(interface{ StatusCode() int })
	if !handled || !okStatus || statusErr.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("RefreshAuthViaHome() = handled %v err %v, want generic 503", handled, errRefresh)
	}
	if strings.Contains(errRefresh.Error(), "provider-secret") {
		t.Fatalf("refresh error included transport detail: %v", errRefresh)
	}
	direct, okDirect := errRefresh.(interface {
		DirectResponse() bool
		ResponseBody() []byte
	})
	if !okDirect || direct.DirectResponse() {
		t.Fatalf("transport error direct response = %v/%v, want false", okDirect, direct)
	}
	diagnosticErr, okDiagnostic := errRefresh.(interface{ LogDiagnostic() string })
	if !okDiagnostic || !strings.Contains(diagnosticErr.LogDiagnostic(), "dial_failed") {
		t.Fatalf("transport log diagnostic = %T/%v, want allowlisted transport cause", errRefresh, errRefresh)
	}
	if strings.Contains(diagnosticErr.LogDiagnostic(), "provider-secret") {
		t.Fatalf("transport log diagnostic leaked provider detail: %q", diagnosticErr.LogDiagnostic())
	}
}

func TestRefreshAuthViaHomeUsesGenericMessageForLegacyErrorEnvelope(t *testing.T) {
	client := &fakeHomeRefreshClient{raw: []byte(`{"error":{"type":"error","message":"provider response: refresh_token=provider-secret"}}`)}
	oldCurrentHomeRefreshClient := currentHomeRefreshClient
	currentHomeRefreshClient = func() homeRefreshClient { return client }
	t.Cleanup(func() { currentHomeRefreshClient = oldCurrentHomeRefreshClient })

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	auth := &cliproxyauth.Auth{ID: "home-auth", Index: "home-auth", Provider: "codex"}
	_, handled, errRefresh := RefreshAuthViaHome(context.Background(), cfg, auth)
	statusErr, okStatus := errRefresh.(interface{ StatusCode() int })
	if !handled || !okStatus || statusErr.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("RefreshAuthViaHome() = handled %v err %v, want generic 503", handled, errRefresh)
	}
	if strings.Contains(errRefresh.Error(), "provider-secret") {
		t.Fatalf("refresh error included legacy Home detail: %v", errRefresh)
	}
	if diagnosticErr, ok := errRefresh.(interface{ LogDiagnostic() string }); !ok || diagnosticErr.LogDiagnostic() != "Home refresh failed: type=error" {
		t.Fatalf("legacy Home error type log diagnostic = %T/%v", errRefresh, errRefresh)
	}
}

func TestRefreshAuthViaHomeUsesDedicatedDiagnosticOnlyForLogs(t *testing.T) {
	const diagnostic = "antigravity refresh failed: stage=transport err=EOF"
	client := &fakeHomeRefreshClient{raw: []byte(`{"error":{"type":"refresh_temporarily_unavailable","message":"untrusted provider-secret","diagnostic":"` + diagnostic + `"}}`)}
	oldCurrentHomeRefreshClient := currentHomeRefreshClient
	currentHomeRefreshClient = func() homeRefreshClient { return client }
	t.Cleanup(func() { currentHomeRefreshClient = oldCurrentHomeRefreshClient })

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	auth := &cliproxyauth.Auth{ID: "home-auth", Index: "home-auth", Provider: "antigravity"}
	_, handled, errRefresh := RefreshAuthViaHome(context.Background(), cfg, auth)
	if !handled || errRefresh == nil || errRefresh.Error() != "credential refresh temporarily unavailable" {
		t.Fatalf("RefreshAuthViaHome() = handled %v err %v, want generic client error", handled, errRefresh)
	}
	diagnosticErr, ok := errRefresh.(interface{ LogDiagnostic() string })
	if !ok || diagnosticErr.LogDiagnostic() != diagnostic {
		t.Fatalf("log diagnostic = %T/%v, want %q", errRefresh, errRefresh, diagnostic)
	}
	if strings.Contains(errRefresh.Error(), diagnostic) || strings.Contains(errRefresh.Error(), "provider-secret") {
		t.Fatalf("client error exposed internal detail: %v", errRefresh)
	}
}

func TestRefreshAuthViaHomePreservesUpstreamStatusAndBodyExactly(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
	}{
		{name: "json", status: http.StatusBadRequest, body: []byte(`{"error":"invalid_request"}`)},
		{name: "access token expired", status: http.StatusUnauthorized, body: []byte(`{"error":{"message":"access token expired"}}`)},
		{name: "text", status: http.StatusBadGateway, body: []byte("provider unavailable")},
		{name: "multiline", status: http.StatusTooManyRequests, body: []byte("first line\r\nsecond line\n")},
		{name: "empty", status: http.StatusUnauthorized, body: []byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, errMarshal := json.Marshal(homeErrorEnvelope{Error: &homeErrorDetail{
				Type:       "refresh_temporarily_unavailable",
				Message:    "credential refresh temporarily unavailable",
				Diagnostic: "antigravity refresh failed: stage=upstream_response status=400",
				Upstream: &homeUpstreamResponse{
					Status: tt.status,
					Body:   tt.body,
				},
			}})
			if errMarshal != nil {
				t.Fatalf("marshal Home error envelope: %v", errMarshal)
			}
			client := &fakeHomeRefreshClient{raw: raw}
			oldCurrentHomeRefreshClient := currentHomeRefreshClient
			currentHomeRefreshClient = func() homeRefreshClient { return client }
			t.Cleanup(func() { currentHomeRefreshClient = oldCurrentHomeRefreshClient })

			cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
			auth := &cliproxyauth.Auth{ID: "home-auth", Index: "home-auth", Provider: "codex"}
			_, handled, errRefresh := RefreshAuthViaHome(context.Background(), cfg, auth)
			statusErr, okStatus := errRefresh.(interface{ StatusCode() int })
			if !handled || !okStatus || statusErr.StatusCode() != tt.status {
				t.Fatalf("RefreshAuthViaHome() = handled %v err %v status %v, want true/%d", handled, errRefresh, okStatus, tt.status)
			}
			if got := []byte(errRefresh.Error()); !bytes.Equal(got, tt.body) {
				t.Fatalf("refresh body = %q, want exact body %q", got, tt.body)
			}
			direct, okDirect := errRefresh.(interface {
				DirectResponse() bool
				ResponseBody() []byte
			})
			if !okDirect || !direct.DirectResponse() {
				t.Fatalf("upstream error direct response = %v/%v, want true", okDirect, direct)
			}
			if got := direct.ResponseBody(); !bytes.Equal(got, tt.body) {
				t.Fatalf("direct response body = %q, want exact body %q", got, tt.body)
			}
			diagnosticErr, okDiagnostic := errRefresh.(interface{ LogDiagnostic() string })
			if !okDiagnostic || !strings.Contains(diagnosticErr.LogDiagnostic(), "stage=upstream_response") {
				t.Fatalf("upstream log diagnostic = %T/%v", errRefresh, errRefresh)
			}
		})
	}
}

func TestRefreshAuthViaHomeUsesGenericMessageForLegacyProviderDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		raw        []byte
		wantStatus int
		wantError  string
		wantLog    string
	}{
		{
			name:       "transient transport diagnostic",
			provider:   "antigravity",
			raw:        []byte(`{"error":{"type":"refresh_temporarily_unavailable","message":"antigravity refresh: Post https://oauth.example/token?access_token=provider-secret: connection refused"}}`),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "credential refresh temporarily unavailable",
			wantLog:    "Home refresh failed: type=refresh_temporarily_unavailable",
		},
		{
			name:       "terminal legacy diagnostic",
			provider:   "codex",
			raw:        []byte(`{"error":{"type":"authentication_error","message":"codex refresh: invalid_grant refresh_token=provider-secret"}}`),
			wantStatus: http.StatusUnauthorized,
			wantError:  "credential unauthorized",
			wantLog:    "Home refresh failed: type=authentication_error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeHomeRefreshClient{raw: tt.raw}
			oldCurrentHomeRefreshClient := currentHomeRefreshClient
			currentHomeRefreshClient = func() homeRefreshClient { return client }
			t.Cleanup(func() { currentHomeRefreshClient = oldCurrentHomeRefreshClient })

			cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
			auth := &cliproxyauth.Auth{ID: "home-auth", Index: "home-auth", Provider: tt.provider}
			_, handled, errRefresh := RefreshAuthViaHome(context.Background(), cfg, auth)
			statusErr, okStatus := errRefresh.(interface{ StatusCode() int })
			if !handled || !okStatus || statusErr.StatusCode() != tt.wantStatus {
				t.Fatalf("RefreshAuthViaHome() = handled %v err %v, want status %d", handled, errRefresh, tt.wantStatus)
			}
			if got := errRefresh.Error(); got != tt.wantError {
				t.Fatalf("client refresh error = %q, want %q", got, tt.wantError)
			}
			if strings.Contains(errRefresh.Error(), "provider-secret") {
				t.Fatalf("legacy refresh error leaked provider detail: %v", errRefresh)
			}
			diagnosticErr, okDiagnostic := errRefresh.(interface{ LogDiagnostic() string })
			if !okDiagnostic {
				t.Fatalf("legacy refresh log diagnostic type = %T, want LogDiagnostic", errRefresh)
			}
			if got := diagnosticErr.LogDiagnostic(); got != tt.wantLog {
				t.Fatalf("legacy refresh log diagnostic = %q, want %q", got, tt.wantLog)
			}
			if strings.Contains(diagnosticErr.LogDiagnostic(), "provider-secret") {
				t.Fatalf("legacy refresh log diagnostic leaked provider detail: %q", diagnosticErr.LogDiagnostic())
			}
		})
	}
}

func TestRefreshAuthViaHomeRejectsUnmarkedRefreshMessage(t *testing.T) {
	client := &fakeHomeRefreshClient{raw: []byte(`{"error":{"type":"refresh_temporarily_unavailable","message":"database unavailable: provider-secret"}}`)}
	oldCurrentHomeRefreshClient := currentHomeRefreshClient
	currentHomeRefreshClient = func() homeRefreshClient { return client }
	t.Cleanup(func() { currentHomeRefreshClient = oldCurrentHomeRefreshClient })

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	auth := &cliproxyauth.Auth{ID: "home-auth", Index: "home-auth", Provider: "antigravity"}
	_, _, errRefresh := RefreshAuthViaHome(context.Background(), cfg, auth)
	if got, want := errRefresh.Error(), "credential refresh temporarily unavailable"; got != want {
		t.Fatalf("refresh error = %q, want %q", got, want)
	}
}

func TestAuthAccessTokenSHA256SupportsKnownMetadataShapes(t *testing.T) {
	want := authAccessTokenSHA256(&cliproxyauth.Auth{Metadata: map[string]any{"access_token": "same-token"}})
	cases := map[string]*cliproxyauth.Auth{
		"camel case":        {Metadata: map[string]any{"accessToken": "same-token"}},
		"nested any map":    {Metadata: map[string]any{"token": map[string]any{"access_token": "same-token"}}},
		"nested string map": {Metadata: map[string]any{"Token": map[string]string{"accessToken": "same-token"}}},
	}
	for name, auth := range cases {
		t.Run(name, func(t *testing.T) {
			if got := authAccessTokenSHA256(auth); got == "" || got != want {
				t.Fatalf("token hash = %q, want %q", got, want)
			}
		})
	}
}

func TestRefreshAuthViaHomeAcceptsAuthEnvelope(t *testing.T) {
	raw, errMarshal := json.Marshal(struct {
		Auth      cliproxyauth.Auth `json:"auth"`
		AuthIndex string            `json:"auth_index"`
	}{
		Auth: cliproxyauth.Auth{
			ID:       "home-auth-1",
			Provider: "antigravity",
			Metadata: map[string]any{
				"access_token": "new-access-token",
			},
		},
		AuthIndex: "home-index-1",
	})
	if errMarshal != nil {
		t.Fatalf("marshal home envelope: %v", errMarshal)
	}

	client := &fakeHomeRefreshClient{raw: raw}
	oldCurrentHomeRefreshClient := currentHomeRefreshClient
	currentHomeRefreshClient = func() homeRefreshClient {
		return client
	}
	t.Cleanup(func() {
		currentHomeRefreshClient = oldCurrentHomeRefreshClient
	})

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	auth := &cliproxyauth.Auth{
		ID:       "home-auth-1",
		Provider: "antigravity",
		Index:    "home-index-1",
		Metadata: map[string]any{
			"access_token":  "old-access-token",
			"refresh_token": "refresh-token",
		},
	}

	updated, handled, err := RefreshAuthViaHome(context.Background(), cfg, auth)
	if err != nil {
		t.Fatalf("RefreshAuthViaHome error: %v", err)
	}
	if !handled {
		t.Fatal("RefreshAuthViaHome handled = false, want true")
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("home refresh calls = %d, want 1", got)
	}
	if client.authIndex != "home-index-1" {
		t.Fatalf("home refresh auth_index = %q, want home-index-1", client.authIndex)
	}
	if client.accessTokenHash != authAccessTokenSHA256(auth) {
		t.Fatalf("home refresh access token hash = %q, want %q", client.accessTokenHash, authAccessTokenSHA256(auth))
	}
	if updated == nil {
		t.Fatal("updated auth = nil")
	}
	if got := updated.Metadata["access_token"]; got != "new-access-token" {
		t.Fatalf("updated access_token = %q, want new-access-token", got)
	}
	if updated.Index != "home-index-1" {
		t.Fatalf("updated auth_index = %q, want home-index-1", updated.Index)
	}
}
