package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type codexDeviceRoundTripper func(*http.Request) (*http.Response, error)

func (f codexDeviceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCodexDeviceLoginReportsCodeBeforePollingAndHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests int
	client := &http.Client{Transport: codexDeviceRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != codexDeviceUserCodeURL {
			t.Fatalf("unexpected device request %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"device_auth_id":"private-device-auth-id","user_code":"ABCD-EFGH","interval":5}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	var got DeviceCodeInfo
	authenticator := &CodexAuthenticator{deviceHTTPClient: client}
	_, err := authenticator.Login(ctx, &config.Config{}, &LoginOptions{
		NoBrowser: true,
		Metadata:  map[string]string{codexLoginModeMetadataKey: codexLoginModeDevice},
		OnDeviceCode: func(info DeviceCodeInfo) {
			got = info
			cancel()
		},
	})
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("Login() error = %v, want context cancellation", err)
	}
	if got.UserCode != "ABCD-EFGH" || got.VerificationURL != codexDeviceVerificationURL {
		t.Fatalf("device code info = %#v", got)
	}
	if got.ExpiresIn != int(codexDeviceTimeout/time.Second) {
		t.Fatalf("expires_in = %d, want %d", got.ExpiresIn, int(codexDeviceTimeout/time.Second))
	}
	if requests != 1 {
		t.Fatalf("device requests = %d, want only the code request before cancellation", requests)
	}
}
