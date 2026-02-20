package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

type rewriteTransport struct {
	target string
	base   http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = "http"
	newReq.URL.Host = strings.TrimPrefix(t.target, "http://")
	return t.base.RoundTrip(newReq)
}

func TestGetCopilotAPIToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := CopilotAPIToken{
			Token:     "copilot-api-token",
			ExpiresAt: 1234567890,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := &http.Client{
		Transport: &rewriteTransport{
			target: ts.URL,
			base:   http.DefaultTransport,
		},
	}

	cfg := &config.Config{}
	auth := NewCopilotAuth(cfg, client)
	resp, err := auth.GetCopilotAPIToken(context.Background(), "gh-access-token")
	if err != nil {
		t.Fatalf("GetCopilotAPIToken failed: %v", err)
	}

	if resp.Token != "copilot-api-token" {
		t.Errorf("got token %q, want copilot-api-token", resp.Token)
	}
}
