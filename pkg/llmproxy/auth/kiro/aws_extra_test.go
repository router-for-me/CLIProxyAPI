package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestNewKiroAuth(t *testing.T) {
	cfg := &config.Config{}
	auth := NewKiroAuth(cfg)
	if auth.httpClient == nil {
		t.Error("expected httpClient to be set")
	}
}

func TestKiroAuth_LoadTokenFromFile(t *testing.T) {
	tempDir := t.TempDir()
	tokenPath := filepath.Join(tempDir, "token.json")
	
	tokenData := KiroTokenData{AccessToken: "abc"}
	data, _ := json.Marshal(tokenData)
	os.WriteFile(tokenPath, data, 0600)

	auth := &KiroAuth{}
	loaded, err := auth.LoadTokenFromFile(tokenPath)
	if err != nil || loaded.AccessToken != "abc" {
		t.Errorf("LoadTokenFromFile failed: %v", err)
	}

	// Test ~ expansion
	_, err = auth.LoadTokenFromFile("~/non-existent-path-12345")
	if err == nil {
		t.Error("expected error for non-existent home path")
	}
}

func TestKiroAuth_IsTokenExpired(t *testing.T) {
	auth := &KiroAuth{}
	
	if !auth.IsTokenExpired(&KiroTokenData{ExpiresAt: ""}) {
		t.Error("empty ExpiresAt should be expired")
	}

	past := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	if !auth.IsTokenExpired(&KiroTokenData{ExpiresAt: past}) {
		t.Error("past ExpiresAt should be expired")
	}

	future := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	if auth.IsTokenExpired(&KiroTokenData{ExpiresAt: future}) {
		t.Error("future ExpiresAt should not be expired")
	}

	// Test alternate format
	altFormat := "2099-01-01T12:00:00.000Z"
	if auth.IsTokenExpired(&KiroTokenData{ExpiresAt: altFormat}) {
		t.Error("future alt format should not be expired")
	}
}

func TestKiroAuth_GetUsageLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{
			"subscriptionInfo": {"subscriptionTitle": "Plus"},
			"usageBreakdownList": [{"currentUsageWithPrecision": 10.5, "usageLimitWithPrecision": 100.0}],
			"nextDateReset": 123456789
		}`
		fmt.Fprint(w, resp)
	}))
	defer server.Close()

	auth := &KiroAuth{
		httpClient: http.DefaultClient,
		endpoint:   server.URL,
	}

	usage, err := auth.GetUsageLimits(context.Background(), &KiroTokenData{AccessToken: "token", ProfileArn: "arn"})
	if err != nil {
		t.Fatalf("GetUsageLimits failed: %v", err)
	}

	if usage.SubscriptionTitle != "Plus" || usage.CurrentUsage != 10.5 || usage.UsageLimit != 100.0 {
		t.Errorf("unexpected usage info: %+v", usage)
	}
}

func TestKiroAuth_ListAvailableModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{
			"models": [
				{
					"modelId": "m1",
					"modelName": "Model 1",
					"description": "desc",
					"tokenLimits": {"maxInputTokens": 4096}
				}
			]
		}`
		fmt.Fprint(w, resp)
	}))
	defer server.Close()

	auth := &KiroAuth{
		httpClient: http.DefaultClient,
		endpoint:   server.URL,
	}

	models, err := auth.ListAvailableModels(context.Background(), &KiroTokenData{})
	if err != nil {
		t.Fatalf("ListAvailableModels failed: %v", err)
	}

	if len(models) != 1 || models[0].ModelID != "m1" || models[0].MaxInputTokens != 4096 {
		t.Errorf("unexpected models: %+v", models)
	}
}

func TestKiroAuth_CreateAndUpdateTokenStorage(t *testing.T) {
	auth := &KiroAuth{}
	td := &KiroTokenData{
		AccessToken: "access",
		Email:       "test@example.com",
	}

	ts := auth.CreateTokenStorage(td)
	if ts.AccessToken != "access" || ts.Email != "test@example.com" {
		t.Errorf("CreateTokenStorage failed: %+v", ts)
	}

	td2 := &KiroTokenData{
		AccessToken: "new-access",
	}
	auth.UpdateTokenStorage(ts, td2)
	if ts.AccessToken != "new-access" {
		t.Errorf("UpdateTokenStorage failed: %+v", ts)
	}
}
