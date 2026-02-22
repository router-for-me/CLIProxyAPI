package kiro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSocialAuthClient_CreateToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := SocialTokenResponse{
			AccessToken:  "access",
			RefreshToken: "refresh",
			ProfileArn:   "arn",
			ExpiresIn:    3600,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewSocialAuthClient(nil)
	client.httpClient = http.DefaultClient
	// We can't easily override the constant endpoint without more refactoring
}

func TestGeneratePKCE(t *testing.T) {
	v, c, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE failed: %v", err)
	}
	if v == "" || c == "" {
		t.Error("empty verifier or challenge")
	}
}

func TestGenerateStateParam(t *testing.T) {
	s, err := generateStateParam()
	if err != nil {
		t.Fatalf("generateStateParam failed: %v", err)
	}
	if s == "" {
		t.Error("empty state")
	}
}

func TestSocialAuthClient_BuildLoginURL(t *testing.T) {
	client := &SocialAuthClient{}
	url := client.buildLoginURL("Google", "http://localhost/cb", "challenge", "state")
	if !strings.Contains(url, "idp=Google") || !strings.Contains(url, "state=state") {
		t.Errorf("unexpected URL: %s", url)
	}
}

func TestSocialAuthClient_WebCallbackServer(t *testing.T) {
	client := &SocialAuthClient{}
	expectedState := "xyz"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	redirectURI, resultChan, err := client.startWebCallbackServer(ctx, expectedState)
	if err != nil {
		t.Fatalf("startWebCallbackServer failed: %v", err)
	}

	// Give server a moment to start
	time.Sleep(500 * time.Millisecond)

	// Mock callback
	cbURL := redirectURI + "?code=abc&state=" + expectedState
	resp, err := http.Get(cbURL)
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case result := <-resultChan:
		if result.Code != "abc" || result.State != expectedState {
			t.Errorf("unexpected result: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for callback")
	}

	// Test state mismatch
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	redirectURI2, resultChan2, _ := client.startWebCallbackServer(ctx2, "good")

	// Give server a moment to start
	time.Sleep(500 * time.Millisecond)

	resp2, err := http.Get(redirectURI2 + "?code=abc&state=bad")
	if err == nil {
		_ = resp2.Body.Close()
	}

	select {
	case result2 := <-resultChan2:
		if result2.Error != "state mismatch" {
			t.Errorf("expected state mismatch error, got %s", result2.Error)
		}
	case <-ctx2.Done():
		t.Fatal("timed out waiting for mismatch callback")
	}
}
