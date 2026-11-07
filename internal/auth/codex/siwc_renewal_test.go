package codex

import (
	"context"
	"io"
	"net/http"
	"reflect"
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
			auth.SetRefreshJournal(&memoryRefreshJournal{})
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

func TestSIWCTokenResponseValidatesExpiresIn(t *testing.T) {
	for _, tc := range []struct {
		name         string
		expiresInRaw string
		wantError    bool
	}{
		{name: "accept float json number", expiresInRaw: "3600.0"},
		{name: "accept scientific notation", expiresInRaw: "3.6e3"},
		{name: "reject fractional value", expiresInRaw: "3600.5", wantError: true},
		{name: "reject oversized seconds", expiresInRaw: "1e20", wantError: true},
		{name: "reject infinity string", expiresInRaw: `"Inf"`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(`{"access_token":"access","refresh_token":"refresh","expires_in":3600}`)
			if errSet := doc.SetRaw("expires_in", tc.expiresInRaw); errSet != nil {
				t.Fatal(errSet)
			}
			body := doc.String()
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			auth.SetRefreshJournal(&memoryRefreshJournal{})
			token, errRefresh := auth.RefreshSIWCTokens(context.Background(), "refresh", "oaiapp_expires_in", "")
			if tc.wantError {
				if errRefresh == nil || token != nil {
					t.Fatalf("error = %v, token returned = %t; want error", errRefresh, token != nil)
				}
				return
			}
			if errRefresh != nil || token == nil {
				t.Fatalf("error = %v, token returned = %t; want success", errRefresh, token != nil)
			}
		})
	}
}

func TestSIWCRefreshReceiptPreservesServerWindow(t *testing.T) {
	for _, window := range []string{`"2026-11-07T13:07:09Z"`, `1794056829`, `null`} {
		t.Run(window, func(t *testing.T) {
			doc := ojson.NewDocString(`{"access_token":"access","refresh_token":"rotated","expires_in":3600}`)
			if errSet := doc.SetRaw("earliest_refresh_at", window); errSet != nil {
				t.Fatal(errSet)
			}
			body := doc.String()
			calls := 0
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			auth.SetRefreshJournal(&memoryRefreshJournal{})
			for attempt := 0; attempt < 2; attempt++ {
				token, errRefresh := auth.RefreshSIWCTokens(context.Background(), "receipt-grant", "oaiapp_receipt", "")
				if errRefresh != nil {
					t.Fatal(errRefresh)
				}
				if !reflect.DeepEqual(token.EarliestRefreshAt, ojson.Parse(window).Value()) {
					t.Fatalf("attempt %d: earliest_refresh_at = %v, want %s", attempt, token.EarliestRefreshAt, window)
				}
			}
			if calls != 1 {
				t.Fatalf("receipt was not reused: requests = %d, want 1", calls)
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
