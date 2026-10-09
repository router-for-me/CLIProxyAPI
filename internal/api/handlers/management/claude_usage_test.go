package management

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func claudeUsageFixture(t *testing.T) []byte {
	t.Helper()
	data, errRead := os.ReadFile("testdata/claude_oauth_usage.json")
	if errRead != nil {
		t.Fatal(errRead)
	}
	return data
}

func TestParseClaudeUsageFixture(t *testing.T) {
	snapshot, errParse := parseClaudeUsage(claudeUsageFixture(t))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if *snapshot.FiveHour.Utilization != 42.5 || *snapshot.SevenDay.ResetsAt != "2026-10-15T09:00:00Z" {
		t.Fatalf("plan windows = %+v", snapshot)
	}
	extra := snapshot.ExtraUsage
	if !*extra.IsEnabled || *extra.MonthlyLimit != 5000 || *extra.UsedCredits != 1250 ||
		*extra.Utilization != 25 || *extra.Currency != "USD" || extra.DisabledReason != nil ||
		*extra.UserDisabled || *extra.SpendLimitReached {
		t.Fatalf("extra usage = %+v", extra)
	}
	if len(snapshot.DollarWindows) != 2 {
		t.Fatalf("dollar windows = %+v", snapshot.DollarWindows)
	}
	credits := snapshot.DollarWindows["iguana_necktie"]
	if *credits.LimitDollars != 100 || *credits.UsedDollars != 5.043595 ||
		*credits.RemainingDollars != 94.956405 || *credits.Utilization != 5.043595 ||
		*credits.ResetsAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("separate dollar balance = %+v", credits)
	}
	balance := snapshot.DollarWindows["additional_credit_balance"]
	if *balance.UsedDollars != 0 || *balance.Utilization != 0 || *balance.RemainingDollars != 20 || balance.ResetsAt != nil {
		t.Fatalf("zero/null dollar fields = %+v", balance)
	}
	encoded, errMarshal := json.Marshal(snapshot)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if !strings.Contains(string(encoded), `"user_disabled":false`) || !strings.Contains(string(encoded), `"disabled_reason":null`) {
		t.Fatalf("false/null fields were lost: %s", encoded)
	}
}

func TestParseClaudeUsageNullableAndDisabled(t *testing.T) {
	snapshot, errParse := parseClaudeUsage([]byte(`{
		"five_hour":{"utilization":null,"resets_at":null},
		"seven_day":null,
		"extra_usage":{"is_enabled":false,"monthly_limit":0,"used_credits":0,"utilization":null,
			"currency":"EUR","disabled_reason":"user_disabled","user_disabled":true,"spend_limit_reached":true}
	}`))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if snapshot.FiveHour.Utilization != nil || snapshot.SevenDay != nil ||
		*snapshot.ExtraUsage.IsEnabled || *snapshot.ExtraUsage.DisabledReason != "user_disabled" ||
		!*snapshot.ExtraUsage.UserDisabled || !*snapshot.ExtraUsage.SpendLimitReached {
		t.Fatalf("nullable/disabled snapshot = %+v", snapshot)
	}
}

func TestParseClaudeUsageRejectsInvalidPayload(t *testing.T) {
	for _, payload := range []string{
		`invalid`, `null`, `[]`, `{}`, `{"error":"upstream failed"}`,
		`{"five_hour":{"utilization":"wrong","resets_at":null}}`,
		`{"extra_usage":{"is_enabled":"wrong"}}`,
		`{"seven_day_sonnet":{"utilization":"wrong","resets_at":null}}`,
		`{"balance":{"limit_dollars":"wrong","used_dollars":0,"remaining_dollars":0,"utilization":0,"resets_at":null}}`,
	} {
		if _, errParse := parseClaudeUsage([]byte(payload)); errParse == nil {
			t.Errorf("accepted invalid payload %s", payload)
		}
	}
	for _, payload := range []string{
		`{"five_hour":null,"seven_day":null,"extra_usage":null}`,
		`{"limits":[{"kind":"weekly_scoped","percent":10,"resets_at":null}]}`,
		`{"iguana_necktie":{"utilization":41,"resets_at":"2026-11-01T00:00:00Z"}}`,
	} {
		if _, errParse := parseClaudeUsage([]byte(payload)); errParse != nil {
			t.Errorf("valid nullable/scoped response rejected: %v", errParse)
		}
	}
}

func TestClaudeUsageCallScope(t *testing.T) {
	target, _ := url.Parse(claudeOAuthUsageURL)
	auth := &coreauth.Auth{Provider: "claude", Metadata: map[string]any{"access_token": "synthetic-token"}}
	if !isClaudeOAuthUsageCall(http.MethodGet, target, auth) {
		t.Fatal("expected exact Claude OAuth usage call")
	}
	for _, endpoint := range []string{claudeOAuthUsageURL + "?other=1", claudeOAuthUsageURL + "/", "https://example.com/api/oauth/usage"} {
		targetOther, _ := url.Parse(endpoint)
		if isClaudeOAuthUsageCall(http.MethodGet, targetOther, auth) {
			t.Fatalf("intercepted another endpoint: %s", endpoint)
		}
	}
	if isClaudeOAuthUsageCall(http.MethodPost, target, auth) ||
		isClaudeOAuthUsageCall(http.MethodGet, target, &coreauth.Auth{Provider: "claude", Attributes: map[string]string{"api_key": "synthetic-key"}}) {
		t.Fatal("intercepted non-GET or API-key request")
	}
}

func TestClaudeUsageAPICallRefreshCacheAndIsolation(t *testing.T) {
	// This is sequential because the existing direct transport clones the default.
	// Redirect only its TLS dialer to a fixture server; no Anthropic call is made.
	fixture := claudeUsageFixture(t)
	upstreamStatus := http.StatusOK
	upstreamBody := string(fixture)
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method != http.MethodGet || r.Host != "api.anthropic.com" || r.URL.Path != "/api/oauth/usage" ||
			r.Header.Get("Authorization") != "Bearer refreshed-token" ||
			r.Header.Get("Accept") != "application/json" ||
			r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Error("usage request did not have the required method, URL, or OAuth headers")
		}
		w.WriteHeader(upstreamStatus)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer server.Close()
	originalTransport := http.DefaultTransport
	transport := originalTransport.(*http.Transport).Clone()
	transport.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "http://"))
	}
	http.DefaultTransport = transport
	defer func() {
		http.DefaultTransport = originalTransport
		transport.CloseIdleConnections()
	}()

	manager := coreauth.NewManager(nil, nil, nil)
	exec := &refreshRecordExecutor{provider: "claude"}
	manager.RegisterExecutor(exec)
	auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "claude.json", FileName: "claude.json", Provider: "claude",
		Attributes: map[string]string{"runtime_only": "true"},
		Status:     coreauth.StatusError, Unavailable: true,
		LastError:      &coreauth.Error{HTTPStatus: 401, Message: "synthetic unauthorized"},
		NextRetryAfter: time.Now().Add(time.Hour),
		Metadata:       map[string]any{"access_token": "synthetic-expired", "refresh_token": "synthetic-refresh", "expired": "2000-01-01T00:00:00Z"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(ConfigV8ContextKey, true) })
	router.POST("/api-call", h.APICall)
	router.GET("/credentials", h.ListAuthFiles)
	call := func() apiCallResponse {
		t.Helper()
		body := `{"auth_index":"` + auth.Index + `","method":"GET","url":"` + claudeOAuthUsageURL + `"}`
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api-call", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("management status = %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "refreshed-token") || strings.Contains(w.Body.String(), "synthetic-expired") {
			t.Fatal("credential leaked in management response")
		}
		var response apiCallResponse
		if errDecode := json.Unmarshal(w.Body.Bytes(), &response); errDecode != nil {
			t.Fatal(errDecode)
		}
		return response
	}
	good := call()
	if good.StatusCode != 200 || good.ClaudeUsage == nil || good.Stale || good.ClaudeUsage.ObservedAt.IsZero() {
		t.Fatalf("unexpected usage result: %+v", good)
	}
	if exec.refreshCnt.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", exec.refreshCnt.Load())
	}
	current, _ := manager.GetByID(auth.ID)
	if current.Status != auth.Status || current.Unavailable != auth.Unavailable ||
		!reflect.DeepEqual(current.LastError, auth.LastError) || current.NextRetryAfter != auth.NextRetryAfter {
		t.Fatal("usage refresh changed routing/cooldown state")
	}
	for _, failure := range []struct {
		status int
		body   string
	}{{429, `{"error":"refreshed-token"}`}, {200, `{"five_hour":{"utilization":"broken"}}`}, {302, ""}} {
		upstreamStatus, upstreamBody = failure.status, failure.body
		stale := call()
		if !stale.Stale || stale.Error == "" || stale.Body != good.Body || !reflect.DeepEqual(stale.ClaudeUsage, good.ClaudeUsage) {
			t.Fatal("failure discarded the last good usage snapshot")
		}
	}
	if exec.refreshCnt.Load() != 1 || requestCount != 4 {
		t.Fatal("usage reads introduced extra refreshes or requests")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/credentials", nil))
	var files struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(w.Body.Bytes(), &files); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(files.Files) != 1 || files.Files[0]["claude_usage"] == nil || files.Files[0]["claude_usage_stale"] != true {
		t.Fatalf("credential list omitted cached usage: %s", w.Body.String())
	}
	legacy := gin.New()
	legacy.GET("/auth-files", h.ListAuthFiles)
	w = httptest.NewRecorder()
	legacy.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth-files", nil))
	if strings.Contains(w.Body.String(), "claude_usage") {
		t.Fatal("deprecated management contract changed")
	}
	manager.Remove(context.Background(), auth.ID)
	if _, errReplace := manager.Register(context.Background(), auth.Clone()); errReplace != nil {
		t.Fatal(errReplace)
	}
	if h.cachedClaudeUsage(auth) != nil {
		t.Fatal("cached observation survived credential removal/replacement")
	}
	response := call()
	if response.Stale || response.ClaudeUsage != nil || response.Body == good.Body {
		t.Fatal("replacement credential received the previous registration's snapshot")
	}
}

func TestClaudeUsageCacheAccountReplacementIsolation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		replace  func(*coreauth.Auth)
	}{
		{
			name: "different account",
			metadata: map[string]any{
				"account_uuid": "account-a", "organization_uuid": "org-a",
			},
			replace: func(auth *coreauth.Auth) { auth.Metadata["account_uuid"] = "account-b" },
		},
		{
			name: "different organization",
			metadata: map[string]any{
				"account_uuid": "account-a", "organization_uuid": "org-a",
			},
			replace: func(auth *coreauth.Auth) { auth.Metadata["organization_uuid"] = "org-b" },
		},
		{
			name:     "email identity fallback",
			metadata: map[string]any{"email": "account-a@example.test"},
			replace:  func(auth *coreauth.Auth) { auth.Metadata["email"] = "account-b@example.test" },
		},
		{
			name:     "unknown identity secret replacement",
			metadata: map[string]any{},
			replace:  func(auth *coreauth.Auth) { auth.Metadata["access_token"] = "synthetic-replacement" },
		},
		{
			name:     "OAuth replaced by API key",
			metadata: map[string]any{"account_uuid": "account-a"},
			replace: func(auth *coreauth.Auth) {
				auth.Attributes[coreauth.AttributeAPIKey] = "synthetic-key"
				delete(auth.Metadata, "access_token")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			tc.metadata["access_token"] = "synthetic-access"
			auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{
				ID: "reused.json", Provider: "claude", Metadata: tc.metadata,
				Attributes: map[string]string{"runtime_only": "true"},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			snapshot, errParse := parseClaudeUsage(claudeUsageFixture(t))
			if errParse != nil {
				t.Fatal(errParse)
			}
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
			h.claudeUsage = map[string]*claudeUsageCacheEntry{auth.ID: {
				epoch: auth.RegistrationEpoch, identity: claudeUsageIdentityFor(auth),
				snapshot: snapshot, body: string(claudeUsageFixture(t)),
			}}
			replacement := auth.Clone()
			tc.replace(replacement)
			current, errUpdate := manager.Update(context.Background(), replacement)
			if errUpdate != nil {
				t.Fatal(errUpdate)
			}
			if current.RegistrationEpoch != auth.RegistrationEpoch {
				t.Fatal("test must cover replacement within the same registration")
			}
			if h.cachedClaudeUsage(current) != nil || h.cachedClaudeUsage(auth) != nil {
				t.Fatal("replacement account received the previous account's snapshot")
			}
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			h.claudeUsageFailure(ctx, current, http.StatusTooManyRequests, "Claude usage upstream request failed")
			var response apiCallResponse
			if errDecode := json.Unmarshal(w.Body.Bytes(), &response); errDecode != nil {
				t.Fatal(errDecode)
			}
			if response.ClaudeUsage != nil || response.Stale {
				t.Fatal("failed read exposed a snapshot belonging to the previous account")
			}
		})
	}
}

func TestClaudeUsageCacheSurvivesSameAccountTokenRotation(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "claude.json", Provider: "claude",
		Metadata: map[string]any{
			"access_token": "synthetic-access", "account_uuid": "account-a", "organization_uuid": "org-a",
		},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	snapshot, errParse := parseClaudeUsage(claudeUsageFixture(t))
	if errParse != nil {
		t.Fatal(errParse)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	h.claudeUsage = map[string]*claudeUsageCacheEntry{auth.ID: {
		epoch: auth.RegistrationEpoch, identity: claudeUsageIdentityFor(auth),
		snapshot: snapshot, body: string(claudeUsageFixture(t)),
	}}
	rotated := auth.Clone()
	rotated.Metadata["access_token"] = "synthetic-rotated"
	current, errUpdate := manager.UpdateRefreshedAuth(context.Background(), auth, rotated)
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if current.CredentialVersion == auth.CredentialVersion {
		t.Fatal("token rotation did not change the credential version")
	}
	if cached := h.cachedClaudeUsage(current); cached == nil || cached.snapshot != snapshot {
		t.Fatal("token rotation discarded the same account's last good snapshot")
	}
}

func fixtureClaudeUsageTransport(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	originalTransport := http.DefaultTransport
	transport := originalTransport.(*http.Transport).Clone()
	transport.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "http://"))
	}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
		transport.CloseIdleConnections()
		server.Close()
	})
}

func TestClaudeUsageFailureRetainsSnapshotAfterUnidentifiedTokenRefresh(t *testing.T) {
	fixtureClaudeUsageTransport(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer refreshed-token" {
			t.Error("usage read did not use the refreshed token")
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"refreshed-token"}`))
	})
	manager := coreauth.NewManager(nil, nil, nil)
	exec := &refreshRecordExecutor{provider: "claude"}
	manager.RegisterExecutor(exec)
	auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "unknown-account", Provider: "claude",
		Metadata: map[string]any{"access_token": "synthetic-expired", "refresh_token": "synthetic-refresh", "expired": "2000-01-01T00:00:00Z"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	fixture := claudeUsageFixture(t)
	snapshot, errParse := parseClaudeUsage(fixture)
	if errParse != nil {
		t.Fatal(errParse)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	h.claudeUsage = map[string]*claudeUsageCacheEntry{auth.ID: {
		epoch: auth.RegistrationEpoch, identity: claudeUsageIdentityFor(auth), snapshot: snapshot, body: string(fixture),
	}}
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/usage", nil)
	h.callClaudeOAuthUsage(ctx, auth, "direct")
	var response apiCallResponse
	if errDecode := json.Unmarshal(w.Body.Bytes(), &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	current, _ := manager.GetByID(auth.ID)
	if current.CredentialVersion == auth.CredentialVersion || exec.refreshCnt.Load() != 1 {
		t.Fatal("test did not rotate an unidentified credential")
	}
	if !response.Stale || response.StatusCode != http.StatusTooManyRequests || response.Body != string(fixture) ||
		!reflect.DeepEqual(response.ClaudeUsage, snapshot) || h.cachedClaudeUsage(current) == nil {
		t.Fatal("own token refresh discarded the last good snapshot on failure")
	}
	if strings.Contains(w.Body.String(), "refreshed-token") {
		t.Fatal("failed usage response leaked upstream credential material")
	}
}

func TestClaudeUsageRejectsCredentialReplacementBeforeAndDuringRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(*coreauth.Auth)
	}{
		{"provider", func(auth *coreauth.Auth) { auth.Provider = "codex" }},
		{"kind", func(auth *coreauth.Auth) { auth.Attributes[coreauth.AttributeAuthKind] = coreauth.AuthKindAPIKey }},
		{"account", func(auth *coreauth.Auth) { auth.Metadata["account_uuid"] = "account-b" }},
		{"organization", func(auth *coreauth.Auth) { auth.Metadata["organization_uuid"] = "org-b" }},
	} {
		for _, duringRead := range []bool{false, true} {
			stage := "before read"
			if duringRead {
				stage = "during read"
			}
			t.Run(tc.name+"/"+stage, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var requestCount atomic.Int32
				fixture := claudeUsageFixture(t)
				fixtureClaudeUsageTransport(t, func(w http.ResponseWriter, _ *http.Request) {
					requestCount.Add(1)
					close(entered)
					<-release
					_, _ = w.Write(fixture)
				})
				manager := coreauth.NewManager(nil, nil, nil)
				auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{
					ID: "reused", Provider: "claude",
					Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
					Metadata: map[string]any{
						"access_token": "synthetic-valid", "account_uuid": "account-a", "organization_uuid": "org-a",
					},
				})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				snapshot, errParse := parseClaudeUsage(fixture)
				if errParse != nil {
					t.Fatal(errParse)
				}
				h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
				h.claudeUsage = map[string]*claudeUsageCacheEntry{auth.ID: {
					epoch: auth.RegistrationEpoch, identity: claudeUsageIdentityFor(auth), snapshot: snapshot, body: string(fixture),
				}}
				replacement := auth.Clone()
				tc.replace(replacement)
				w := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(w)
				ctx.Request = httptest.NewRequest(http.MethodGet, "/usage", nil)
				if duringRead {
					done := make(chan struct{})
					go func() {
						h.callClaudeOAuthUsage(ctx, auth, "direct")
						close(done)
					}()
					<-entered
					_, errUpdate := manager.Update(context.Background(), replacement)
					close(release)
					<-done
					if errUpdate != nil {
						t.Fatal(errUpdate)
					}
				} else {
					if _, errUpdate := manager.Update(context.Background(), replacement); errUpdate != nil {
						t.Fatal(errUpdate)
					}
					close(release)
					h.callClaudeOAuthUsage(ctx, auth, "direct")
				}
				var response apiCallResponse
				if errDecode := json.Unmarshal(w.Body.Bytes(), &response); errDecode != nil {
					t.Fatal(errDecode)
				}
				if response.ClaudeUsage != nil || response.Stale || response.Body == string(fixture) || h.cachedClaudeUsage(auth) != nil {
					t.Fatal("replacement scope received the previous credential's usage")
				}
				if !duringRead && requestCount.Load() != 0 {
					t.Fatal("stale snapshot sent a bearer request for the replacement credential")
				}
			})
		}
	}
}
