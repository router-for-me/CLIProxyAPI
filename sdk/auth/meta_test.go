package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type metaTestTransport func(*http.Request) (*http.Response, error)

func (transport metaTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestMetaAuthenticator(t *testing.T) {
	authenticator := NewMetaAuthenticator()
	if authenticator.Provider() != "meta" {
		t.Errorf("expected provider 'meta', got '%s'", authenticator.Provider())
	}
	if lead := authenticator.RefreshLead(); lead != nil {
		t.Errorf("expected nil refresh lead, got %v", lead)
	}
}

func TestMetaLoginPreservesMintSubscriptionMetadata(t *testing.T) {
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	t.Setenv("META_MINT_URL", "https://api.meta.ai/muse-code/key")

	mintCalls := 0
	http.DefaultTransport = metaTestTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/oidc/device/authorization/":
			body = `{"device_code":"device-code","user_code":"USER-CODE","verification_uri":"https://auth.meta.com/device","expires_in":10,"interval":1}`
		case "/oidc/device/token/":
			body = `{"access_token":"dca:test","token_type":"Bearer","expires_in":3600}`
		case "/muse-code/key":
			mintCalls++
			body = `{"api_key":"LLM|minted","subs_tier_name":"Pro","subs_tier_id":"pro","is_subs_active":true,"has_payment_method":false}`
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	record, errLogin := (MetaAuthenticator{}).Login(context.Background(), &config.Config{}, &LoginOptions{NoBrowser: true})
	if errLogin != nil {
		t.Fatalf("Login() error = %v", errLogin)
	}
	if mintCalls != 1 {
		t.Fatalf("mint calls = %d, want 1", mintCalls)
	}
	for key, want := range map[string]any{
		"subs_tier_name":     "Pro",
		"subs_tier_id":       "pro",
		"is_subs_active":     true,
		"has_payment_method": false,
	} {
		got, ok := record.Metadata[key]
		if !ok || got != want {
			t.Errorf("Metadata[%q] = %v (present %t), want %v", key, got, ok, want)
		}
	}
}
