package antigravity

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchProjectIDFromLoadCodeAssist(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected request URL: %s", req.URL.String())
		}
		assertLoadCodeAssistHeaders(t, req)
		assertJSONContains(t, req, `"ideType":"ANTIGRAVITY"`)
		return jsonResponse(`{"cloudaicompanionProject":"cogent-snow-4mnnp"}`), nil
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchProjectID error: %v", err)
	}
	if projectID != "cogent-snow-4mnnp" {
		t.Fatalf("projectID = %q", projectID)
	}
}

func TestFetchProjectIDFallsBackToDailyOnboardUser(t *testing.T) {
	var sawOnboard bool
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:loadCodeAssist":
			assertLoadCodeAssistHeaders(t, req)
			return jsonResponse(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`), nil
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			sawOnboard = true
			assertOnboardUserHeaders(t, req)
			assertJSONContains(t, req, `"tier_id":"free-tier"`)
			assertJSONContains(t, req, `"ide_type":"ANTIGRAVITY"`)
			return jsonResponse(`{
				"done": true,
				"response": {
					"cloudaicompanionProject": {
						"id": "cogent-snow-4mnnp",
						"name": "cogent-snow-4mnnp",
						"projectNumber": "22597072101"
					}
				}
			}`), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchProjectID error: %v", err)
	}
	if !sawOnboard {
		t.Fatalf("expected onboardUser fallback")
	}
	if projectID != "cogent-snow-4mnnp" {
		t.Fatalf("projectID = %q", projectID)
	}
}

func assertLoadCodeAssistHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "" {
		t.Fatalf("Accept = %q, want empty", got)
	}
	if got := req.Header.Get("X-Goog-Api-Client"); got != "" {
		t.Fatalf("X-Goog-Api-Client = %q, want empty", got)
	}
	if got := req.Header.Get("User-Agent"); strings.Contains(got, "google-api-nodejs-client/") {
		t.Fatalf("User-Agent = %q", got)
	}
}

func assertOnboardUserHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "" {
		t.Fatalf("Accept = %q, want empty", got)
	}
	if got := req.Header.Get("X-Goog-Api-Client"); got != "gl-node/22.21.1" {
		t.Fatalf("X-Goog-Api-Client = %q", got)
	}
	if got := req.Header.Get("User-Agent"); !strings.Contains(got, "google-api-nodejs-client/10.3.0") {
		t.Fatalf("User-Agent = %q", got)
	}
}

func assertJSONContains(t *testing.T, req *http.Request, want string) {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyText := string(body)
	req.Body = io.NopCloser(strings.NewReader(bodyText))
	if !strings.Contains(bodyText, want) {
		t.Fatalf("body missing %s: %s", want, bodyText)
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRefreshAccessToken(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://oauth2.googleapis.com/token" {
			t.Fatalf("unexpected request URL: %s", req.URL.String())
		}
		if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Fatalf("Content-Type = %q", got)
		}
		ua := req.Header.Get("User-Agent")
		if !strings.HasPrefix(ua, "vscode/1.X.X") || !strings.Contains(ua, "Antigravity/") {
			t.Fatalf("User-Agent = %q, want vscode/1.X.X (Antigravity/...)", ua)
		}
		body, _ := io.ReadAll(req.Body)
		bodyText := string(body)
		if !strings.Contains(bodyText, "grant_type=refresh_token") {
			t.Fatalf("body missing grant_type: %s", bodyText)
		}
		if !strings.Contains(bodyText, "client_id="+ClientID) {
			t.Fatalf("body missing client_id: %s", bodyText)
		}
		return jsonResponse(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"token_type":"Bearer"}`), nil
	})})

	token, err := auth.RefreshAccessToken(context.Background(), "my-refresh-token")
	if err != nil {
		t.Fatalf("RefreshAccessToken error: %v", err)
	}
	if token.AccessToken != "new-access" {
		t.Fatalf("AccessToken = %q, want new-access", token.AccessToken)
	}
	if token.RefreshToken != "new-refresh" {
		t.Fatalf("RefreshToken = %q, want new-refresh", token.RefreshToken)
	}
}

func TestRefreshAccessToken_EmptyRefreshToken(t *testing.T) {
	auth := NewAntigravityAuth(nil, nil)
	_, err := auth.RefreshAccessToken(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty refresh token")
	}
}

func TestResolveSubscriptionTier_PaidTierFirst(t *testing.T) {
	resp := map[string]any{
		"paidTier": map[string]any{
			"name": "Google One AI Premium",
			"id":   "paid-tier-id",
		},
		"currentTier": map[string]any{
			"name": "Free",
		},
	}
	tier := resolveSubscriptionTier(resp)
	if tier != "Google One AI Premium" {
		t.Fatalf("tier = %q, want Google One AI Premium", tier)
	}
}

func TestResolveSubscriptionTier_CurrentTierWhenNotIneligible(t *testing.T) {
	resp := map[string]any{
		"currentTier": map[string]any{
			"name": "Pro",
		},
		"allowedTiers": []any{
			map[string]any{"id": "pro", "isDefault": true},
		},
	}
	tier := resolveSubscriptionTier(resp)
	if tier != "Pro" {
		t.Fatalf("tier = %q, want Pro", tier)
	}
}

func TestResolveSubscriptionTier_CurrentTierSkippedWhenIneligible(t *testing.T) {
	resp := map[string]any{
		"currentTier": map[string]any{
			"name": "Pro",
		},
		"ineligibleTiers": []any{
			map[string]any{"reasonCode": "some-reason"},
		},
		"allowedTiers": []any{
			map[string]any{"name": "Free Tier", "isDefault": true},
		},
	}
	tier := resolveSubscriptionTier(resp)
	if tier != "Free Tier" {
		t.Fatalf("tier = %q, want Free Tier", tier)
	}
}

func TestResolveSubscriptionTier_FallbackToFreeTier(t *testing.T) {
	resp := map[string]any{}
	tier := resolveSubscriptionTier(resp)
	if tier != "free-tier" {
		t.Fatalf("tier = %q, want free-tier", tier)
	}
}

func TestBuildAuthURL_IncludesGrantedScopes(t *testing.T) {
	auth := NewAntigravityAuth(nil, nil)
	url := auth.BuildAuthURL("test-state", "http://localhost:51121/oauth-callback")
	if !strings.Contains(url, "include_granted_scopes=true") {
		t.Fatalf("auth URL missing include_granted_scopes: %s", url)
	}
	if !strings.Contains(url, "openid") {
		t.Fatalf("auth URL missing openid scope: %s", url)
	}
}
