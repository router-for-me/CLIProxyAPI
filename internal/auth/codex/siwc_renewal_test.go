package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestSIWCTokenResponseValidatesScope(t *testing.T) {
	for _, tc := range []struct {
		name, scope string
		exchange    bool
		wantError   bool
	}{
		{name: "refresh omits unchanged scope"},
		{name: "refresh explicitly clears scope", scope: `""`, wantError: true},
		{name: "refresh returns null scope", scope: `null`, wantError: true},
		{name: "refresh removes direct permission", scope: `"openid profile"`, wantError: true},
		{name: "refresh returns malformed scope", scope: `123`, wantError: true},
		{name: "refresh grants direct permission", scope: `"openid chatgpt.tokens.use.direct"`},
		{name: "exchange requires explicit scope", exchange: true, wantError: true},
		{name: "exchange grants direct permission", exchange: true, scope: `"openid chatgpt.tokens.use.direct"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(`{"access_token":"access","refresh_token":"refresh","id_token":"id","expires_in":3600}`)
			if tc.scope != "" {
				if errSet := doc.SetRaw("scope", tc.scope); errSet != nil {
					t.Fatal(errSet)
				}
			}
			body := doc.String()
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			var err error
			if tc.exchange {
				_, err = auth.ExchangeSIWCCodeForTokens(context.Background(), "code", "oaiapp_scope", "", "", &PKCECodes{CodeVerifier: "verifier"})
			} else {
				_, err = auth.RefreshSIWCTokens(context.Background(), "refresh", "oaiapp_scope", "")
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError = %v", err, tc.wantError)
			}
		})
	}
}

func TestSIWCRefreshErrorRetainsRequestID(t *testing.T) {
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"X-Request-Id": []string{"req-siwc-test"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`)),
		}, nil
	})}}
	_, errRefresh := auth.RefreshSIWCTokens(context.Background(), "refresh", "oaiapp_request_id", "")
	if errRefresh == nil || !strings.Contains(errRefresh.Error(), "req-siwc-test") || !strings.Contains(errRefresh.Error(), "invalid_grant") {
		t.Fatalf("upstream correlation details missing: %v", errRefresh)
	}
}

func TestGenerateSIWCReauthURLRejectsUnissuedClient(t *testing.T) {
	for _, clientID := range []string{"", " ", SIWCDynamicClientID} {
		if _, errGenerate := (&CodexAuth{}).GenerateSIWCReauthURL(clientID, "state", "nonce", "e61bbe28-07ef-466d-8e5d-a344f94ab305", "", &PKCECodes{CodeChallenge: "challenge"}); errGenerate == nil {
			t.Errorf("accepted an unissued client ID %q", clientID)
		}
	}
}
