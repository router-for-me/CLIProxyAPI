package kiro

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestKiroDeviceFlowAndRefresh(t *testing.T) {
	var grants []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/client/register":
			if body["clientType"] != "public" {
				t.Errorf("registration: %v", body)
			}
			_, _ = io.WriteString(w, `{"clientId":"test-client","clientSecret":"test-secret","clientSecretExpiresAt":2000000000}`)
		case "/device_authorization":
			if body["startUrl"] != "https://view.awsapps.com/start" || body["clientId"] != "test-client" {
				t.Errorf("device authorization: %v", body)
			}
			_, _ = io.WriteString(w, `{"deviceCode":"device","userCode":"USER","verificationUri":"https://example.com/activate","expiresIn":600,"interval":1}`)
		case "/token":
			grants = append(grants, body["grantType"].(string))
			if body["clientId"] != "test-client" || body["clientSecret"] != "test-secret" {
				t.Errorf("token: %v", body)
			}
			_, _ = io.WriteString(w, `{"accessToken":"new-access","refreshToken":"rotated-refresh","expiresIn":3600}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
		}
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), OIDCURL: server.URL}
	device, err := client.StartDeviceFlow(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.PollToken(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	metadata := TokenMetadata(token, device)
	if String(metadata, "expired") == "" || String(metadata, "client_secret") != "test-secret" {
		t.Fatalf("missing refresh metadata")
	}
	token, err = client.Refresh(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	ApplyToken(metadata, token)
	if len(grants) != 2 || grants[1] != "refresh_token" || String(metadata, "refresh_token") != "rotated-refresh" {
		t.Fatalf("refresh grant was not used")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = client.WaitForAuthorization(ctx, device); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestKiroNativeImportAndSocialRefresh(t *testing.T) {
	metadata := map[string]any{"type": "kiro", "accessToken": "native", "refreshToken": "refresh", "expiresAt": "2030-01-01T00:00:00Z", "profileArn": "arn:aws:codewhisperer:eu-central-1:123:profile/test", "region": "us-east-1", "authMethod": "social"}
	NormalizeMetadata(metadata)
	if String(metadata, "access_token") != "native" || metadata["accessToken"] != nil || APIRegion(metadata) != "eu-central-1" {
		t.Fatal("native metadata was not normalized")
	}
	client := &Client{HTTP: &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken" {
			t.Errorf("wrong social endpoint: %s", r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"refreshToken":"refresh"}` {
			t.Errorf("social payload: %s", body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"accessToken":"updated","expiresIn":3600}`)), Header: make(http.Header)}, nil
	})}}
	token, err := client.Refresh(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	ApplyToken(metadata, token)
	if String(metadata, "refresh_token") != "refresh" || String(metadata, "access_token") != "updated" {
		t.Fatal("refresh token was lost")
	}
	metadata["auth_method"] = "IdC"
	if _, err = client.Refresh(context.Background(), metadata); err == nil {
		t.Fatal("OIDC refresh without a client must fail")
	}
}

func TestKiroAccountManagerImportShapes(t *testing.T) {
	social := map[string]any{"email": "user@example.com", "refreshToken": "social-refresh", "provider": "Github"}
	NormalizeMetadata(social)
	if String(social, "type") != "kiro" || String(social, "auth_method") != "social" || String(social, "refresh_token") != "social-refresh" {
		t.Fatalf("social export was not imported: %+v", social)
	}
	idc := map[string]any{"refreshToken": "idc-refresh", "provider": "BuilderId", "clientId": "cid", "clientSecret": "csec", "region": "eu-central-1"}
	NormalizeMetadata(idc)
	if String(idc, "auth_method") != "idc" || String(idc, "client_id") != "cid" || String(idc, "client_secret") != "csec" || String(idc, "region") != "eu-central-1" {
		t.Fatalf("BuilderId export was not imported: %+v", idc)
	}
	external := map[string]any{"refreshToken": "ms-refresh", "provider": "external_idp"}
	NormalizeMetadata(external)
	if String(external, "type") != "kiro" || String(external, "auth_method") != "external_idp" {
		t.Fatalf("external IdP export was not typed: %+v", external)
	}
	unknown := map[string]any{"refreshToken": "refresh", "provider": "SomethingElse"}
	NormalizeMetadata(unknown)
	if _, hijacked := unknown["type"]; hijacked {
		t.Fatal("unknown provider must not be claimed as Kiro")
	}
}

func TestKiroControlPlaneModelDiscovery(t *testing.T) {
	var profileCalls, modelCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("x-amz-target") == "" || r.Header.Get("Content-Type") != "application/x-amz-json-1.0" {
			t.Errorf("invalid control-plane request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("x-amz-target") {
		case "KiroControlPlaneBearerService.ListAvailableProfiles":
			profileCalls++
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("missing bearer token")
			}
			_, _ = io.WriteString(w, `{"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:123:profile/resolved"}]}`)
		case "KiroControlPlaneBearerService.ListAvailableModels":
			modelCalls++
			if body["profileArn"] != "arn:aws:codewhisperer:us-east-1:123:profile/resolved" {
				t.Errorf("models request did not carry the resolved profile: %v", body)
			}
			_, _ = io.WriteString(w, `{"models":[{"modelId":"account/model-one","modelName":"Model One","contextWindow":200000,"capabilities":["TEXT","IMAGE","OTHER"]}]}`)
		default:
			t.Errorf("unexpected target: %s", r.Header.Get("x-amz-target"))
		}
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client()}
	metadata := map[string]any{"control_url": server.URL, "access_token": "test-token"}
	arn, err := client.ResolveProfileARN(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	modelMetadata := map[string]any{"control_url": server.URL, "access_token": "test-token", "profile_arn": arn}
	models, err := client.ListModels(context.Background(), modelMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if profileCalls != 1 || modelCalls != 1 || len(models) != 1 || models[0].ContextLength != 200000 {
		t.Fatalf("discovery: profiles=%d models=%d %+v", profileCalls, modelCalls, models)
	}
	if len(models[0].SupportedInputModalities) != 2 || models[0].SupportedInputModalities[0] != "text" {
		t.Fatalf("capabilities not mapped: %+v", models[0].SupportedInputModalities)
	}
	if String(metadata, "profile_arn") != "" {
		t.Fatal("ListModels must not mutate credential metadata")
	}
	arn, err = client.ResolveProfileARN(context.Background(), metadata)
	if err != nil || arn != "arn:aws:codewhisperer:us-east-1:123:profile/resolved" {
		t.Fatalf("ResolveProfileARN: %q %v", arn, err)
	}
}

func TestKiroModelCatalogPaginationAndCapabilities(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("x-amz-target") != "KiroControlPlaneBearerService.ListAvailableModels" || body["origin"] != "AI_EDITOR" {
			t.Errorf("invalid discovery request: %v", body)
		}
		if body["nextToken"] == "second-page" {
			_, _ = io.WriteString(w, `{"models":[{"modelId":"account/model-two","modelName":null,"supportedInputTypes":["TEXT","IMAGE","TEXT"],"tokenLimits":{"maxInputTokens":1000000,"maxOutputTokens":64000}}]}`)
		} else {
			_, _ = io.WriteString(w, `{"models":[{"modelId":"account/model-one","contextWindow":200000}],"nextToken":"second-page"}`)
		}
	}))
	defer server.Close()
	models, err := (&Client{HTTP: server.Client()}).ListModels(context.Background(), map[string]any{"access_token": "test-token", "control_url": server.URL})
	if err != nil || requests != 2 || len(models) != 2 {
		t.Fatalf("discovery: requests=%d models=%d error=%v", requests, len(models), err)
	}
	if models[1].ContextLength != 1000000 || models[1].MaxCompletionTokens != 64000 || len(models[1].SupportedInputModalities) != 2 {
		t.Fatalf("capabilities: %+v", models[1])
	}
}

func TestKiroTokenRefreshDecisions(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, access, refresh, expiry string
		want                          bool
	}{
		{"refresh-only", "", "refresh", "", true},
		{"unknown-expiry", "access", "refresh", "", true},
		{"expired", "access", "refresh", now.Add(-time.Hour).Format(time.RFC3339), true},
		{"expiring", "access", "refresh", now.Add(time.Minute).Format(time.RFC3339), true},
		{"valid", "access", "refresh", now.Add(time.Hour).Format(time.RFC3339), false},
		{"access-only", "access", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TokenNeedsRefresh(map[string]any{"access_token": tc.access, "refresh_token": tc.refresh, "expired": tc.expiry}, now); got != tc.want {
				t.Fatalf("needs refresh = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKiroImportDoesNotOverrideExplicitFields(t *testing.T) {
	for _, metadata := range []map[string]any{
		{"type": "codex", "provider": "Github", "refreshToken": "refresh"},
		{"type": "openai-compatible", "provider": "Google", "refreshToken": "refresh"},
	} {
		want := metadata["type"]
		NormalizeMetadata(metadata)
		if metadata["type"] != want {
			t.Fatal("explicit credential type changed")
		}
	}
	metadata := map[string]any{"provider": "Github", "refresh_token": "canonical", "refreshToken": "native", "authMethod": "IdC", "clientId": "client", "clientSecret": "secret"}
	NormalizeMetadata(metadata)
	if String(metadata, "type") != "kiro" || String(metadata, "refresh_token") != "canonical" || String(metadata, "auth_method") != "IdC" {
		t.Fatalf("canonical fields or explicit auth method changed: %+v", metadata)
	}
}
