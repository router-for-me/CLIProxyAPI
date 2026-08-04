package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestClaudeExecutorDuplicateMetadataIsRequestScoped(t *testing.T) {
	testCases := []struct {
		name string
		run  func(context.Context, *ClaudeExecutor, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			run: func(ctx context.Context, executor *ClaudeExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errExecute := executor.Execute(ctx, auth, req, opts)
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(ctx context.Context, executor *ClaudeExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errStream := executor.ExecuteStream(ctx, auth, req, opts)
				return errStream
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			upstreamCalled := false
			transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
				upstreamCalled = true
				return nil, errors.New("unexpected upstream request")
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "sk-ant-oat-duplicate-metadata", "auth_kind": "oauth"},
				Metadata: map[string]any{
					"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
					claudeauth.ClaudeDeviceIDsMetadataKey: []string{
						"0000000000000000000000000000000000000000000000000000000000000000",
					},
				},
			}
			req := cliproxyexecutor.Request{
				Model: "claude-opus-5",
				Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}],` +
					`"metadata":{"user_id":"{}"},"metadata":{"user_id":"{}"}}`),
			}
			errRun := testCase.run(ctx, NewClaudeExecutor(&config.Config{}), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if errRun == nil {
				t.Fatal("duplicate metadata error = nil")
			}
			if upstreamCalled {
				t.Fatal("duplicate metadata reached upstream")
			}
			var requestErr cliproxyexecutor.RequestScopedError
			if !errors.As(errRun, &requestErr) || requestErr == nil || !requestErr.IsRequestScoped() {
				t.Fatalf("duplicate metadata error = %T %v, want request-scoped", errRun, errRun)
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errRun, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("duplicate metadata error = %T %v, want HTTP 400", errRun, errRun)
			}
		})
	}
}

func TestClaudeExecutorPrepareRequestAuthPopulatesCredentialIdentity(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(_ context.Context, _ *cliproxyauth.Auth, accessToken string) (*claudeauth.OAuthProfile, error) {
		if accessToken != "sk-ant-oat-prepare" {
			t.Fatalf("access token = %q, want selected credential token", accessToken)
		}
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		profile.Account.Email = "user@example.com"
		profile.Organization.UUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		profile.Organization.Name = "Example Org"
		return profile, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-old-credential",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat-prepare",
		},
		Metadata: map[string]any{"type": "claude"},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing credential identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	deviceIDs := claudeauth.NormalizeDeviceIDPool(prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	if len(deviceIDs) != claudeauth.ClaudeDevicePoolSize {
		t.Fatalf("device pool length = %d, want %d", len(deviceIDs), claudeauth.ClaudeDevicePoolSize)
	}
	if got := prepared.Metadata["account_uuid"]; got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account_uuid = %#v, want upstream profile account", got)
	}
	if got := prepared.Metadata["organization_uuid"]; got != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("organization_uuid = %#v, want upstream profile organization", got)
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuthMigratesFiveDevicesToOne(t *testing.T) {
	legacy := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444444444444444444444444444",
	}
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		t.Fatal("profile lookup should not run when account UUID is already present")
		return nil, nil
	}
	auth := &cliproxyauth.Auth{
		ID:         "claude-five-device-credential",
		Attributes: map[string]string{"api_key": "sk-ant-oat-five-device"},
		Metadata: map[string]any{
			"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			claudeauth.ClaudeDeviceIDsMetadataKey: legacy,
		},
	}
	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for legacy five-device pool")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	deviceIDs, ok := prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey].([]string)
	if !ok || len(deviceIDs) != 1 || deviceIDs[0] != legacy[0] {
		t.Fatalf("prepared device IDs = %#v, want first legacy device only", prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after single-device migration")
	}
}

func TestClaudeExecutorPrepareRequestAuthIgnoresFreshTimestampWithoutIdentity(t *testing.T) {
	calls := 0
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		calls++
		return nil, fmt.Errorf("profile unavailable")
	}
	const previousCheckedAt = "2999-01-01T00:00:00Z"
	auth := &cliproxyauth.Auth{
		ID:         "claude-profile-unavailable",
		Attributes: map[string]string{"api_key": "sk-ant-oat-profile-unavailable"},
		Metadata: map[string]any{
			"type":                                "claude",
			claudeAccountProfileCheckedAtKey:      previousCheckedAt,
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{"0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}

	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare == nil {
		t.Fatal("PrepareRequestAuth() error = nil, want missing account identity failure")
	}
	if prepared != nil {
		t.Fatalf("PrepareRequestAuth() auth = %#v, want nil on missing account identity", prepared)
	}
	if calls != 1 {
		t.Fatalf("profile calls = %d, want 1", calls)
	}
	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false after failed profile lookup; failure must remain retryable")
	}
	if got := claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileCheckedAtKey); got != previousCheckedAt {
		t.Fatalf("profile checked timestamp = %q, want prior value preserved without suppressing retry", got)
	}
}
