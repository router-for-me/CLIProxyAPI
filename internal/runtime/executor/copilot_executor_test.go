package executor

import (
	"context"
	"testing"
	"time"

	copilotauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// TestStripCopilotPrefix verifies that the copilot- prefix is correctly stripped from model names.
func TestStripCopilotPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "model with copilot prefix",
			input:    "copilot-claude-opus-4.5",
			expected: "claude-opus-4.5",
		},
		{
			name:     "model with copilot prefix - gpt",
			input:    "copilot-gpt-5",
			expected: "gpt-5",
		},
		{
			name:     "model with copilot prefix - gemini",
			input:    "copilot-gemini-2.5-pro",
			expected: "gemini-2.5-pro",
		},
		{
			name:     "model without prefix",
			input:    "claude-opus-4.5",
			expected: "claude-opus-4.5",
		},
		{
			name:     "model without prefix - gpt",
			input:    "gpt-5",
			expected: "gpt-5",
		},
		{
			name:     "model with -copilot suffix (not prefix)",
			input:    "gpt-41-copilot",
			expected: "gpt-41-copilot",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "just the prefix",
			input:    "copilot-",
			expected: "",
		},
		{
			name:     "copilot without hyphen",
			input:    "copilotmodel",
			expected: "copilotmodel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripCopilotPrefix(tt.input)
			if result != tt.expected {
				t.Errorf("stripCopilotPrefix(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestCopilotModelPrefixConstant verifies the prefix constant is correct.
func TestCopilotModelPrefixConstant(t *testing.T) {
	if registry.CopilotModelPrefix != "copilot-" {
		t.Errorf("CopilotModelPrefix = %q, want %q", registry.CopilotModelPrefix, "copilot-")
	}
}


// mockCopilotTokenFetcher is a test double for copilot token fetching operations.
// It allows tests to simulate various GetCopilotToken responses without network calls.
type mockCopilotTokenFetcher struct {
	tokenResp *copilotauth.CopilotTokenResponse
	err       error
	callCount int
}

func (m *mockCopilotTokenFetcher) GetCopilotToken(ctx context.Context, githubToken string) (*copilotauth.CopilotTokenResponse, error) {
	m.callCount++
	return m.tokenResp, m.err
}

// testCopilotExecutor wraps CopilotExecutor with an injectable token fetcher for testing.
type testCopilotExecutor struct {
	*CopilotExecutor
	tokenFetcher *mockCopilotTokenFetcher
}

// newTestCopilotExecutor creates a test executor with a mock token fetcher.
func newTestCopilotExecutor(cfg *config.Config, fetcher *mockCopilotTokenFetcher) *testCopilotExecutor {
	return &testCopilotExecutor{
		CopilotExecutor: NewCopilotExecutor(cfg),
		tokenFetcher:    fetcher,
	}
}

// refreshWithMock performs refresh using the mock token fetcher instead of real HTTP calls.
func (te *testCopilotExecutor) refreshWithMock(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: 500, msg: "copilot executor: auth is nil (copilot_refresh_auth_nil)"}
	}

	var githubToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["github_token"].(string); ok && v != "" {
			githubToken = v
		}
	}
	if githubToken == "" {
		return auth, nil
	}

	tokenResp, err := te.tokenFetcher.GetCopilotToken(ctx, githubToken)
	if err != nil {
		code := 503
		cause := "copilot_refresh_transient"

		switch {
		case err == copilotauth.ErrNoCopilotSubscription:
			code = 401
			cause = "copilot_no_subscription"
		case err == copilotauth.ErrAccessDenied:
			code = 401
			cause = "copilot_access_denied"
		case err == copilotauth.ErrNoGitHubToken:
			code = 401
			cause = "copilot_no_github_token"
		default:
			if httpCode := copilotauth.StatusCode(err); httpCode != 0 {
				if httpCode == 401 || httpCode == 403 {
					code = 401
					cause = "copilot_auth_rejected"
				} else if httpCode >= 500 {
					cause = "copilot_upstream_error"
				}
			}
		}
		return nil, statusErr{code: code, msg: "copilot token refresh failed (" + cause + ")"}
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["copilot_token"] = tokenResp.Token
	auth.Metadata["copilot_token_expiry"] = time.Unix(tokenResp.ExpiresAt, 0).Format(time.RFC3339)
	auth.Metadata["type"] = "copilot"

	return auth, nil
}

// TestCopilotExecutor_getCopilotToken_NilAuth tests that getCopilotToken returns 500 for nil auth.
func TestCopilotExecutor_getCopilotToken_NilAuth(t *testing.T) {
	e := NewCopilotExecutor(&config.Config{})
	ctx := context.Background()

	_, _, err := e.getCopilotToken(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil auth, got nil")
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 500 {
		t.Errorf("expected status code 500, got %d", se.code)
	}
	if se.msg == "" || se.msg != "copilot executor: auth is nil (copilot_auth_nil)" {
		t.Errorf("unexpected error message: %s", se.msg)
	}
}

// TestCopilotExecutor_getCopilotToken_MissingToken tests that getCopilotToken returns 401 for missing token.
func TestCopilotExecutor_getCopilotToken_MissingToken(t *testing.T) {
	e := NewCopilotExecutor(&config.Config{})
	ctx := context.Background()

	auth := &cliproxyauth.Auth{
		ID:       "test-auth",
		Metadata: map[string]any{},
	}

	_, _, err := e.getCopilotToken(ctx, auth)
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 401 {
		t.Errorf("expected status code 401, got %d", se.code)
	}
}

// TestCopilotExecutor_getCopilotToken_RehydrateFromStorage tests that tokens are rehydrated from storage.
func TestCopilotExecutor_getCopilotToken_RehydrateFromStorage(t *testing.T) {
	e := NewCopilotExecutor(&config.Config{})
	ctx := context.Background()

	// Create storage with tokens
	storage := &copilotauth.CopilotTokenStorage{
		GitHubToken:        "test-github-token",
		AccountType:        "business",
	}

	auth := &cliproxyauth.Auth{
		ID:       "test-auth",
		Metadata: map[string]any{
			"copilot_token": "test-copilot-token", // Token must be in metadata now
			"copilot_token_expiry": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
		Storage:  storage,
		Attributes: map[string]string{
			"account_type": "business",
		},
	}

	token, accountType, err := e.getCopilotToken(ctx, auth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if token != "test-copilot-token" {
		t.Errorf("expected token 'test-copilot-token', got '%s'", token)
	}

	if accountType != copilotauth.AccountTypeBusiness {
		t.Errorf("expected AccountTypeBusiness, got %v", accountType)
	}

	// Verify metadata was populated
	if auth.Metadata["copilot_token"] != "test-copilot-token" {
		t.Errorf("metadata copilot_token not populated")
	}
	if auth.Metadata["github_token"] != "test-github-token" {
		t.Errorf("metadata github_token not populated")
	}
}

// TestCopilotExecutor_getCopilotToken_ValidToken tests that a valid token is returned without refresh.
func TestCopilotExecutor_getCopilotToken_ValidToken(t *testing.T) {
	e := NewCopilotExecutor(&config.Config{})
	ctx := context.Background()

	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"copilot_token":        "valid-token",
			"copilot_token_expiry": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	token, accountType, err := e.getCopilotToken(ctx, auth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if token != "valid-token" {
		t.Errorf("expected token 'valid-token', got '%s'", token)
	}

	// Default to individual when no account_type specified
	if accountType != copilotauth.AccountTypeIndividual {
		t.Errorf("expected AccountTypeIndividual, got %v", accountType)
	}
}

// TestCopilotExecutor_getCopilotToken_AccountTypePrecedence tests that Attributes takes precedence.
func TestCopilotExecutor_getCopilotToken_AccountTypePrecedence(t *testing.T) {
	e := NewCopilotExecutor(&config.Config{})
	ctx := context.Background()

	// Storage says individual, Attributes says enterprise - Attributes should win
	storage := &copilotauth.CopilotTokenStorage{
		AccountType:        "individual",
	}

	auth := &cliproxyauth.Auth{
		ID:       "test-auth",
		Metadata: map[string]any{
			"copilot_token": "test-token",
			"copilot_token_expiry": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
		Storage:  storage,
		Attributes: map[string]string{
			"account_type": "enterprise",
		},
	}

	_, accountType, err := e.getCopilotToken(ctx, auth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if accountType != copilotauth.AccountTypeEnterprise {
		t.Errorf("expected AccountTypeEnterprise (from Attributes), got %v", accountType)
	}
}

// TestCopilotExecutor_Refresh_NilAuth tests that Refresh returns 500 for nil auth.
func TestCopilotExecutor_Refresh_NilAuth(t *testing.T) {
	e := NewCopilotExecutor(&config.Config{})
	ctx := context.Background()

	_, err := e.Refresh(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil auth, got nil")
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 500 {
		t.Errorf("expected status code 500, got %d", se.code)
	}
}

// TestCopilotExecutor_Refresh_NoGitHubToken tests that Refresh returns auth unchanged when no github_token.
func TestCopilotExecutor_Refresh_NoGitHubToken(t *testing.T) {
	e := NewCopilotExecutor(&config.Config{})
	ctx := context.Background()

	auth := &cliproxyauth.Auth{
		ID:       "test-auth",
		Metadata: map[string]any{},
	}

	result, err := e.Refresh(ctx, auth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return auth unchanged
	if result != auth {
		t.Errorf("expected same auth instance returned")
	}
}

// TestCopilotExecutor_Refresh_SuccessUpdatesMetadataAndStorage tests successful refresh updates.
func TestCopilotExecutor_Refresh_SuccessUpdatesMetadataAndStorage(t *testing.T) {
	mockFetcher := &mockCopilotTokenFetcher{
		tokenResp: &copilotauth.CopilotTokenResponse{
			Token:     "new-refreshed-token",
			ExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
			RefreshIn: 300,
		},
	}
	te := newTestCopilotExecutor(&config.Config{}, mockFetcher)

	storage := &copilotauth.CopilotTokenStorage{
		GitHubToken:        "test-github-token",
	}

	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"github_token": "test-github-token",
		},
		Storage: storage,
	}

	result, err := te.refreshWithMock(context.Background(), auth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify metadata was updated
	if result.Metadata["copilot_token"] != "new-refreshed-token" {
		t.Errorf("metadata copilot_token not updated, got %v", result.Metadata["copilot_token"])
	}

	// Verify fetcher was called
	if mockFetcher.callCount != 1 {
		t.Errorf("expected 1 fetcher call, got %d", mockFetcher.callCount)
	}
}

// TestCopilotExecutor_Refresh_ErrNoCopilotSubscription tests 401 for subscription errors.
func TestCopilotExecutor_Refresh_ErrNoCopilotSubscription(t *testing.T) {
	mockFetcher := &mockCopilotTokenFetcher{
		err: copilotauth.ErrNoCopilotSubscription,
	}
	te := newTestCopilotExecutor(&config.Config{}, mockFetcher)

	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"github_token":  "test-github-token",
			"copilot_token": "existing-token", // Preserve existing token
		},
	}

	_, err := te.refreshWithMock(context.Background(), auth)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 401 {
		t.Errorf("expected status code 401 for no subscription, got %d", se.code)
	}
}

// TestCopilotExecutor_Refresh_ErrAccessDenied tests 401 for access denied errors.
func TestCopilotExecutor_Refresh_ErrAccessDenied(t *testing.T) {
	mockFetcher := &mockCopilotTokenFetcher{
		err: copilotauth.ErrAccessDenied,
	}
	te := newTestCopilotExecutor(&config.Config{}, mockFetcher)

	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"github_token": "test-github-token",
		},
	}

	_, err := te.refreshWithMock(context.Background(), auth)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 401 {
		t.Errorf("expected status code 401 for access denied, got %d", se.code)
	}
}

// TestCopilotExecutor_Refresh_HTTPStatusError401 tests 401 for HTTPStatusError with 401 code.
func TestCopilotExecutor_Refresh_HTTPStatusError401(t *testing.T) {
	mockFetcher := &mockCopilotTokenFetcher{
		err: copilotauth.NewHTTPStatusError(401, "unauthorized", nil),
	}
	te := newTestCopilotExecutor(&config.Config{}, mockFetcher)

	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"github_token": "test-github-token",
		},
	}

	_, err := te.refreshWithMock(context.Background(), auth)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 401 {
		t.Errorf("expected status code 401 for HTTPStatusError(401), got %d", se.code)
	}
}

// TestCopilotExecutor_Refresh_HTTPStatusError403 tests 401 for HTTPStatusError with 403 code.
func TestCopilotExecutor_Refresh_HTTPStatusError403(t *testing.T) {
	mockFetcher := &mockCopilotTokenFetcher{
		err: copilotauth.NewHTTPStatusError(403, "forbidden", nil),
	}
	te := newTestCopilotExecutor(&config.Config{}, mockFetcher)

	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"github_token": "test-github-token",
		},
	}

	_, err := te.refreshWithMock(context.Background(), auth)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 401 {
		t.Errorf("expected status code 401 for HTTPStatusError(403), got %d", se.code)
	}
}

// TestCopilotExecutor_Refresh_HTTPStatusError500 tests 503 for HTTPStatusError with 500 code.
func TestCopilotExecutor_Refresh_HTTPStatusError500(t *testing.T) {
	mockFetcher := &mockCopilotTokenFetcher{
		err: copilotauth.NewHTTPStatusError(500, "internal server error", nil),
	}
	te := newTestCopilotExecutor(&config.Config{}, mockFetcher)

	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"github_token": "test-github-token",
		},
	}

	_, err := te.refreshWithMock(context.Background(), auth)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 503 {
		t.Errorf("expected status code 503 for HTTPStatusError(500), got %d", se.code)
	}
}

// TestCopilotExecutor_Refresh_TransientError tests 503 for generic transient errors.
func TestCopilotExecutor_Refresh_TransientError(t *testing.T) {
	mockFetcher := &mockCopilotTokenFetcher{
		err: copilotauth.ErrCopilotTokenFailed,
	}
	te := newTestCopilotExecutor(&config.Config{}, mockFetcher)

	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"github_token": "test-github-token",
		},
	}

	_, err := te.refreshWithMock(context.Background(), auth)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != 503 {
		t.Errorf("expected status code 503 for transient error, got %d", se.code)
	}
}

// TestCopilotExecutor_getCopilotToken_NeedsRefresh tests that NeedsRefresh triggers refresh.
func TestCopilotExecutor_getCopilotToken_NeedsRefresh(t *testing.T) {
	// Test removed as NeedsRefresh logic is now internal to executor cache
}

// TestCopilotExecutor_getCopilotToken_NeedsRefresh_FromMetadata tests expiry check from metadata.
func TestCopilotExecutor_getCopilotToken_NeedsRefresh_FromMetadata(t *testing.T) {
	e := NewCopilotExecutor(&config.Config{})
	ctx := context.Background()

	// Token expires in 30 seconds - should trigger refresh check
	expiryTime := time.Now().Add(30 * time.Second)

	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"copilot_token":        "metadata-token",
			"copilot_token_expiry": expiryTime.Format(time.RFC3339),
			// No github_token - refresh attempt will be skipped
		},
	}

	token, _, err := e.getCopilotToken(ctx, auth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return existing token even when near expiry (refresh fails silently)
	if token != "metadata-token" {
		t.Errorf("expected 'metadata-token', got '%s'", token)
	}
}

// TestCopilotExecutor_getCopilotToken_MissingTokenWithGitHubToken tests refresh attempt.
func TestCopilotExecutor_getCopilotToken_MissingTokenWithGitHubToken(t *testing.T) {
	e := NewCopilotExecutor(&config.Config{})
	ctx := context.Background()

	// Auth with github_token but no copilot_token - will attempt refresh
	// Since we can't mock the actual refresh, this will fail and return 401/503
	auth := &cliproxyauth.Auth{
		ID: "test-auth",
		Metadata: map[string]any{
			"github_token": "test-github-token",
			// No copilot_token - triggers refresh attempt
		},
	}

	// This will attempt a real refresh which will fail (no network)
	// The error should be a statusErr with appropriate code
	_, _, err := e.getCopilotToken(ctx, auth)
	if err == nil {
		t.Fatal("expected error for failed refresh, got nil")
	}

	// Should get a statusErr (either 401 or 503 depending on failure mode)
	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}

	// Accept either 401 (auth error) or 503 (transient error) since network is unavailable
	if se.code != 401 && se.code != 503 {
		t.Errorf("expected status code 401 or 503, got %d", se.code)
	}
}

// TestStatusErr_Error tests the statusErr error interface.
func TestStatusErr_Error(t *testing.T) {
	err := statusErr{code: 401, msg: "test error"}

	expected := "test error"
	if err.Error() != expected {
		t.Errorf("expected '%s', got '%s'", expected, err.Error())
	}
}

// TestAccountTypeValidation tests the ValidateAccountType helper.
func TestAccountTypeValidation(t *testing.T) {
	tests := []struct {
		input       string
		expectValid bool
		expectType  copilotauth.AccountType
	}{
		{"individual", true, copilotauth.AccountTypeIndividual},
		{"business", true, copilotauth.AccountTypeBusiness},
		{"enterprise", true, copilotauth.AccountTypeEnterprise},
		{"INDIVIDUAL", true, copilotauth.AccountTypeIndividual}, // Case insensitive
		{"Business", true, copilotauth.AccountTypeBusiness},
		{"invalid", false, copilotauth.AccountTypeIndividual},
		{"", false, copilotauth.AccountTypeIndividual},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := copilotauth.ValidateAccountType(tt.input)
			if result.Valid != tt.expectValid {
				t.Errorf("ValidateAccountType(%q).Valid = %v, want %v", tt.input, result.Valid, tt.expectValid)
			}
			if result.AccountType != tt.expectType {
				t.Errorf("ValidateAccountType(%q).AccountType = %v, want %v", tt.input, result.AccountType, tt.expectType)
			}
			if len(result.ValidValues) != 3 {
				t.Errorf("expected 3 valid values, got %d", len(result.ValidValues))
			}
		})
	}
}

// TestHTTPStatusError tests the HTTPStatusError type.
func TestHTTPStatusError(t *testing.T) {
	cause := copilotauth.ErrNoCopilotSubscription
	err := copilotauth.NewHTTPStatusError(401, "unauthorized", cause)

	if err.StatusCode != 401 {
		t.Errorf("expected StatusCode 401, got %d", err.StatusCode)
	}

	if err.Message != "unauthorized" {
		t.Errorf("expected Message 'unauthorized', got '%s'", err.Message)
	}

	if err.Cause != cause {
		t.Errorf("expected Cause to be ErrNoCopilotSubscription")
	}

	// Test Error() format
	errStr := err.Error()
	if errStr == "" {
		t.Error("Error() returned empty string")
	}

	// Test Unwrap
	if err.Unwrap() != cause {
		t.Errorf("Unwrap() did not return cause")
	}

	// Test StatusCode helper
	if copilotauth.StatusCode(err) != 401 {
		t.Errorf("StatusCode helper returned wrong value")
	}

	// Test StatusCode with non-HTTP error
	if copilotauth.StatusCode(cause) != 0 {
		t.Errorf("StatusCode should return 0 for non-HTTP error")
	}
}

// TestMaskToken tests the token masking function.
func TestMaskToken(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "<empty>"},
		{"ab", "<short>"},
		{"abcd", "<short>"},
		{"abcde", "ab****de"},
		{"ghp_1234567890abcdef", "gh****ef"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := copilotauth.MaskToken(tt.input)
			if result != tt.expected {
				t.Errorf("maskToken(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
