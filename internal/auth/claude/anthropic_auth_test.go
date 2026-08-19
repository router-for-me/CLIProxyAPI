package claude

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewAnthropicHttpClientDoesNotSetRequestTimeout(t *testing.T) {
	if got := NewAnthropicHttpClient(nil).Timeout; got != 0 {
		t.Fatalf("HTTP client timeout = %s, want zero", got)
	}
}

func TestRefreshTokens_UsesIndependentTimeout(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	var requestDeadline time.Time
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var ok bool
				requestDeadline, ok = req.Context().Deadline()
				if !ok {
					t.Fatal("refresh request has no deadline")
				}
				if errContext := req.Context().Err(); errContext != nil {
					t.Fatalf("refresh request context is already done: %v", errContext)
				}
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":"probe"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokens(callerCtx, "independent-timeout-token")
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if requestDeadline.IsZero() || !requestDeadline.After(time.Now()) {
		t.Fatalf("refresh deadline = %v, want a future deadline", requestDeadline)
	}
}

// jsonResponse builds a canned control-plane response for the fake transport.
func jsonResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}
}

func TestExchangeCodeForTokensPersistsUpstreamAccountAndDevicePool(t *testing.T) {
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case TokenURL:
					if req.Method != http.MethodPost {
						t.Fatalf("token request = %s %s, want POST %s", req.Method, req.URL, TokenURL)
					}
					return jsonResponse(req, `{
						"access_token":"access",
						"refresh_token":"refresh",
						"token_type":"Bearer",
						"expires_in":3600,
						"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email_address":"user@example.com"},
						"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}
					}`), nil
				case ProfileURL:
					return jsonResponse(req, `{
						"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"user@example.com"},
						"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}
					}`), nil
				case RolesURL:
					return jsonResponse(req, `{"roles":[]}`), nil
				default:
					t.Fatalf("unexpected OAuth request URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}

	bundle, errExchange := auth.ExchangeCodeForTokens(context.Background(), "code", "state", &PKCECodes{CodeVerifier: "verifier"})
	if errExchange != nil {
		t.Fatalf("ExchangeCodeForTokens() error = %v", errExchange)
	}
	if bundle.TokenData.AccountUUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account UUID = %q, want OAuth response account", bundle.TokenData.AccountUUID)
	}
	if bundle.TokenData.OrganizationUUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || bundle.TokenData.OrganizationName != "Example Org" {
		t.Fatalf("organization = %q/%q, want OAuth response organization", bundle.TokenData.OrganizationUUID, bundle.TokenData.OrganizationName)
	}
	if len(bundle.DeviceIDs) != ClaudeDevicePoolSize {
		t.Fatalf("device pool length = %d, want %d", len(bundle.DeviceIDs), ClaudeDevicePoolSize)
	}
	storage := auth.CreateTokenStorage(bundle)
	if storage.AccountUUID != bundle.TokenData.AccountUUID || storage.OrganizationUUID != bundle.TokenData.OrganizationUUID {
		t.Fatalf("storage account identity = %#v, want bundle identity", storage)
	}
	if len(storage.DeviceIDs) != ClaudeDevicePoolSize {
		t.Fatalf("storage device pool length = %d, want %d", len(storage.DeviceIDs), ClaudeDevicePoolSize)
	}
}

func TestExchangeCodeForTokensUsesNative220ControlPlaneShape(t *testing.T) {
	var order []string
	headers := make(map[string]http.Header)
	var tokenBody []byte

	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				order = append(order, req.URL.String())
				headers[req.URL.String()] = req.Header.Clone()
				if !req.Close {
					t.Fatalf("%s request Close = false, want true", req.URL)
				}
				switch req.URL.String() {
				case TokenURL:
					if req.URL.Host != "platform.claude.com" {
						t.Fatalf("exchange host = %q, want platform.claude.com", req.URL.Host)
					}
					body, errRead := io.ReadAll(req.Body)
					if errRead != nil {
						t.Fatal(errRead)
					}
					tokenBody = body
					return jsonResponse(req, `{"access_token":"access","refresh_token":"refresh","expires_in":28800}`), nil
				case ProfileURL, RolesURL:
					if req.Method != http.MethodGet {
						t.Fatalf("%s method = %s, want GET", req.URL, req.Method)
					}
					if req.URL.String() == ProfileURL {
						return jsonResponse(req, `{
							"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"user@example.com"},
							"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}
						}`), nil
					}
					return jsonResponse(req, `{"roles":["claude_code_user"]}`), nil
				default:
					t.Fatalf("unexpected OAuth request URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}

	bundle, errExchange := auth.ExchangeCodeForTokens(t.Context(), "auth-code", "state-value", &PKCECodes{CodeVerifier: "verifier"})
	if errExchange != nil {
		t.Fatalf("ExchangeCodeForTokens() error = %v", errExchange)
	}

	wantOrder := []string{TokenURL, ProfileURL, RolesURL}
	if len(order) != len(wantOrder) {
		t.Fatalf("request order = %v, want %v", order, wantOrder)
	}
	for i, want := range wantOrder {
		if order[i] != want {
			t.Fatalf("request order = %v, want %v", order, wantOrder)
		}
	}

	// Key order mirrors the captured native exchange body.
	wantBody := `{"grant_type":"authorization_code","code":"auth-code","redirect_uri":"` + RedirectURI + `","client_id":"` + ClientID + `","code_verifier":"verifier","state":"state-value"}`
	if got := string(tokenBody); got != wantBody {
		t.Fatalf("exchange body = %q, want %q", got, wantBody)
	}

	wantAxios := map[string]string{
		"Accept":          "application/json, text/plain, */*",
		"Content-Type":    "application/json",
		"User-Agent":      "axios/1.15.2",
		"Accept-Encoding": "gzip, compress, deflate, br",
		"Connection":      "close",
	}
	for _, endpoint := range wantOrder {
		for name, want := range wantAxios {
			if got := headers[endpoint].Get(name); got != want {
				t.Fatalf("%s %s = %q, want %q", endpoint, name, got, want)
			}
		}
	}
	if got := headers[TokenURL].Get("Authorization"); got != "" {
		t.Fatalf("exchange Authorization = %q, want unset", got)
	}
	for _, endpoint := range []string{ProfileURL, RolesURL} {
		if got := headers[endpoint].Get("Authorization"); got != "Bearer access" {
			t.Fatalf("%s Authorization = %q, want the freshly exchanged bearer token", endpoint, got)
		}
		if got := headers[endpoint].Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s Cache-Control = %q, want no-cache", endpoint, got)
		}
	}

	if bundle.TokenData.AccountUUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account UUID = %q, want the companion profile account", bundle.TokenData.AccountUUID)
	}
	if bundle.TokenData.OrganizationUUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || bundle.TokenData.OrganizationName != "Example Org" {
		t.Fatalf("organization = %q/%q, want the companion profile organization", bundle.TokenData.OrganizationUUID, bundle.TokenData.OrganizationName)
	}
	if bundle.TokenData.Email != "user@example.com" {
		t.Fatalf("email = %q, want the companion profile email", bundle.TokenData.Email)
	}
}

func TestExchangeCodeForTokensSurvivesCompanionLookupFailure(t *testing.T) {
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() == TokenURL {
					return jsonResponse(req, `{
						"access_token":"access",
						"refresh_token":"refresh",
						"expires_in":28800,
						"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email_address":"token@example.com"},
						"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Token Org"}
					}`), nil
				}
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(strings.NewReader(`{"error":"unavailable"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	bundle, errExchange := auth.ExchangeCodeForTokens(t.Context(), "code", "state", &PKCECodes{CodeVerifier: "verifier"})
	if errExchange != nil {
		t.Fatalf("companion lookup failure must not fail login, got %v", errExchange)
	}
	if bundle.TokenData.AccountUUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || bundle.TokenData.Email != "token@example.com" {
		t.Fatalf("token-response identity must survive companion failure, got %#v", bundle.TokenData)
	}
	if bundle.TokenData.OrganizationName != "Token Org" {
		t.Fatalf("organization = %q, want token-response organization", bundle.TokenData.OrganizationName)
	}
}

func TestRefreshTokensWithRetry_429BlocksImmediateReplay(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	var calls int32
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(`{"error":"rate_limited"}`)),
					Header:     http.Header{"Retry-After": []string{"60"}},
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected 429 refresh error")
	}
	if !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("expected status 429 in error, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 refresh attempt after 429, got %d", got)
	}

	_, err = auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected immediate blocked refresh error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected blocked retry to avoid a second refresh call, got %d attempts", got)
	}
	if blockedUntil := claudeRefreshBlockedUntil("dummy_refresh_token"); !blockedUntil.After(time.Now()) {
		t.Fatalf("expected blocked-until timestamp to be set, got %v", blockedUntil)
	}
}

func TestRefreshTokens_DeduplicatesConcurrentRefresh(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	var tokenCalls int32
	var profileCalls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case RefreshTokenURL:
					atomic.AddInt32(&tokenCalls, 1)
					once.Do(func() { close(started) })
					<-release
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(`{
							"access_token":"new-access",
							"refresh_token":"new-refresh",
							"token_type":"Bearer",
							"expires_in":3600,
							"scope":"user:profile user:inference"
						}`)),
						Header:  make(http.Header),
						Request: req,
					}, nil
				case ProfileURL:
					atomic.AddInt32(&profileCalls, 1)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(`{
							"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"shared@example.com"},
							"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Shared Org"}
						}`)),
						Header:  make(http.Header),
						Request: req,
					}, nil
				default:
					t.Fatalf("unexpected OAuth request URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}

	results := make(chan *ClaudeTokenData, 2)
	errs := make(chan error, 2)
	runRefresh := func() {
		td, err := auth.RefreshTokens(context.Background(), "shared-refresh-token")
		results <- td
		errs <- err
	}

	go runRefresh()
	go runRefresh()

	<-started
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("expected concurrent refresh to share a single upstream call, got %d", got)
	}
	close(release)

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("expected refresh to succeed, got %v", err)
		}
		td := <-results
		if td == nil || td.AccessToken != "new-access" {
			t.Fatalf("expected refreshed access token, got %#v", td)
		}
		if td.AccountUUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
			t.Fatalf("account UUID = %q, want OAuth response account", td.AccountUUID)
		}
		if td.OrganizationUUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || td.OrganizationName != "Shared Org" {
			t.Fatalf("organization = %q/%q, want OAuth response organization", td.OrganizationUUID, td.OrganizationName)
		}
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("expected exactly 1 upstream refresh call, got %d", got)
	}
	if got := atomic.LoadInt32(&profileCalls); got != 1 {
		t.Fatalf("expected exactly 1 OAuth profile call, got %d", got)
	}
}

func TestRefreshTokensUsesNative220ControlPlaneShape(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	const refreshToken = "placeholder-refresh"
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case RefreshTokenURL:
					if req.Method != http.MethodPost {
						t.Fatalf("refresh method = %s, want POST", req.Method)
					}
					body, errRead := io.ReadAll(req.Body)
					if errRead != nil {
						t.Fatal(errRead)
					}
					wantBody := `{"client_id":"` + ClientID + `","grant_type":"refresh_token","refresh_token":"` + refreshToken + `","scope":"` + ClaudeOAuthScope + `"}`
					if got := string(body); got != wantBody {
						t.Fatalf("refresh body = %q, want %q", got, wantBody)
					}
					wantHeaders := map[string]string{
						"Accept":          "application/json, text/plain, */*",
						"Content-Type":    "application/json",
						"User-Agent":      "axios/1.15.2",
						"Accept-Encoding": "gzip, compress, deflate, br",
						"Connection":      "close",
					}
					for name, want := range wantHeaders {
						if got := req.Header.Get(name); got != want {
							t.Fatalf("%s = %q, want %q", name, got, want)
						}
					}
					if !req.Close {
						t.Fatal("refresh request Close = false, want true")
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"access_token":"new-access","expires_in":3600}`)),
						Header:     make(http.Header),
						Request:    req,
					}, nil
				case ProfileURL:
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(`{
							"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"shared@example.com"},
							"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Shared Org"}
						}`)),
						Header:  make(http.Header),
						Request: req,
					}, nil
				default:
					t.Fatalf("unexpected OAuth request URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}

	tokenData, errRefresh := auth.RefreshTokens(t.Context(), refreshToken)
	if errRefresh != nil {
		t.Fatalf("RefreshTokens() error = %v", errRefresh)
	}
	if tokenData.RefreshToken != refreshToken {
		t.Fatalf("refresh token fallback = %q, want original placeholder", tokenData.RefreshToken)
	}
	if tokenData.AccountUUID == "" || tokenData.Email == "" || tokenData.OrganizationUUID == "" {
		t.Fatalf("profile identity was not populated: %#v", tokenData)
	}
}

func TestFetchOAuthProfile(t *testing.T) {
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.String() != ProfileURL {
					t.Fatalf("profile request = %s %s, want GET %s", req.Method, req.URL, ProfileURL)
				}
				if got := req.Header.Get("Authorization"); got != "Bearer test-access" {
					t.Fatalf("Authorization = %q, want bearer token", got)
				}
				wantHeaders := map[string]string{
					"Accept":          "application/json, text/plain, */*",
					"Content-Type":    "application/json",
					"Cache-Control":   "no-cache",
					"User-Agent":      "axios/1.15.2",
					"Accept-Encoding": "gzip, compress, deflate, br",
					"Connection":      "close",
				}
				for name, want := range wantHeaders {
					if got := req.Header.Get(name); got != want {
						t.Fatalf("%s = %q, want %q", name, got, want)
					}
				}
				if !req.Close {
					t.Fatal("profile request Close = false, want true")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"user@example.com"},
						"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}
					}`)),
					Header:  make(http.Header),
					Request: req,
				}, nil
			}),
		},
	}

	profile, errProfile := auth.FetchOAuthProfile(context.Background(), "test-access")
	if errProfile != nil {
		t.Fatalf("FetchOAuthProfile() error = %v", errProfile)
	}
	if profile.Account.UUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || profile.Account.Email != "user@example.com" {
		t.Fatalf("account = %#v, want upstream profile account", profile.Account)
	}
	if profile.Organization.UUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || profile.Organization.Name != "Example Org" {
		t.Fatalf("organization = %#v, want upstream profile organization", profile.Organization)
	}
}

func TestUpdateTokenStoragePreservesAccountWhenRefreshOmitsIt(t *testing.T) {
	storage := &ClaudeTokenStorage{
		Email:            "user@example.com",
		AccountUUID:      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		OrganizationUUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		OrganizationName: "Example Org",
	}
	(&ClaudeAuth{}).UpdateTokenStorage(storage, &ClaudeTokenData{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		Expire:       "2099-01-01T00:00:00Z",
	})

	if storage.Email != "user@example.com" {
		t.Fatalf("email = %q, want preserved", storage.Email)
	}
	if storage.AccountUUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account UUID = %q, want preserved", storage.AccountUUID)
	}
	if storage.OrganizationUUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || storage.OrganizationName != "Example Org" {
		t.Fatalf("organization = %q/%q, want preserved", storage.OrganizationUUID, storage.OrganizationName)
	}
}
