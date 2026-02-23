package kimi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestRequestDeviceCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := DeviceCodeResponse{
			DeviceCode:      "dev-code",
			UserCode:        "user-code",
			VerificationURI: "http://kimi.com/verify",
			ExpiresIn:       600,
			Interval:        5,
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

	dfc := NewDeviceFlowClientWithDeviceID(nil, "test-device", client)
	resp, err := dfc.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatalf("RequestDeviceCode failed: %v", err)
	}

	if resp.DeviceCode != "dev-code" {
		t.Errorf("got device code %q, want dev-code", resp.DeviceCode)
	}
}

func TestCreateTokenStorage(t *testing.T) {
	auth := NewKimiAuth(nil)
	bundle := &KimiAuthBundle{
		TokenData: &KimiTokenData{
			AccessToken:  "access",
			RefreshToken: "refresh",
			ExpiresAt:    1234567890,
		},
		DeviceID: "device",
	}
	ts := auth.CreateTokenStorage(bundle)
	if ts.AccessToken != "access" {
		t.Errorf("got access %q, want access", ts.AccessToken)
	}
	if ts.DeviceID != "device" {
		t.Errorf("got device %q, want device", ts.DeviceID)
	}
}
