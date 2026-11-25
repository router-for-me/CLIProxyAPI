package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	reviewGroup = "gemini-review"
	reviewHigh  = reviewGroup + "-high"
	reviewLow   = reviewGroup + "-low"
)

func reviewThinkingGroupAuth(t *testing.T) *cliproxyauth.Auth {
	t.Helper()
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	auth := newIssue7865TestAuth(t.Name())
	setReviewThinkingGroupSnapshot(t, client, auth, strings.Repeat("d", 64), nil)
	return auth
}

func setReviewThinkingGroupSnapshot(t *testing.T, client *fakeAntigravityKVClient, auth *cliproxyauth.Auth, scope string, webSearch map[string]bool) {
	t.Helper()
	auth.Attributes = map[string]string{cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scope}
	key := "antigravity:model-enums:v1:" + kvstore.HashKeyPart(auth.ID) + ":" + scope
	client.values[key] = mustAntigravityJSON(t, map[string]any{"scope": scope, "models": map[string]string{reviewHigh: "HIGH_ENUM", reviewLow: "LOW_ENUM"}, "thinkingGroups": map[string]map[string]int{reviewGroup: {"low": 50000, "high": 60000}}, "thinkingWebSearch": webSearch})
}

func TestAntigravityThinkingGroupsReviewExclusions(t *testing.T) {
	auth := reviewThinkingGroupAuth(t)
	cfg := &config.Config{OAuthExcludedModels: map[string][]string{"antigravity": {" GEMINI-*-LO* "}}}
	const payload = `{"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`
	check := func(model, enum string, limit int64) {
		t.Helper()
		got := captureAntigravityThinkingGroupRequest(t, cfg, auth, reviewHigh, sdktranslator.FormatOpenAI, payload, antigravityGeneratePath)
		level := "LOW"
		if limit == 0 {
			level = ""
			if got.Get("request.generationConfig.thinkingConfig.thinkingBudget").Int() != 1024 {
				t.Errorf("legacy thinking budget was not preserved: %s", got.Raw)
			}
		}
		assertAntigravityThinkingGroupRequest(t, got, "model", model, enum, level, limit)
	}
	check(reviewHigh, "HIGH_ENUM", 0)
	auth.Attributes["excluded_models"] = "another-*"
	check(reviewLow, "LOW_ENUM", 50000)
	auth.Attributes["excluded_models"] = "gemini-*low"
	cfg.OAuthExcludedModels = nil
	check(reviewHigh, "HIGH_ENUM", 0)
	delete(auth.Attributes, "excluded_models")
	check(reviewLow, "LOW_ENUM", 50000)
}

func TestAntigravityThinkingGroupsReviewCooldownUsesTarget(t *testing.T) {
	for _, path := range []string{antigravityGeneratePath, antigravityStreamPath, antigravityCountTokensPath} {
		t.Run(path, func(t *testing.T) {
			auth := reviewThinkingGroupAuth(t)
			now := time.Now()
			if errMark := markAntigravityShortCooldownRequired(t.Context(), auth, reviewHigh, now, time.Minute); errMark != nil {
				t.Fatal(errMark)
			}
			calls := 0
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != path {
					t.Errorf("path = %s, want %s", r.URL.Path, path)
				}
				return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"45"}}, Body: io.NopCloser(strings.NewReader(antigravityRateLimitNoRetryInfoBody))}, nil
			}))
			e := NewAntigravityExecutor(&config.Config{})
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
			call := func(model, body string) {
				t.Helper()
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(body)}
				var err error
				switch path {
				case antigravityCountTokensPath:
					_, err = e.CountTokens(ctx, auth, req, opts)
				case antigravityStreamPath:
					_, err = e.ExecuteStream(ctx, auth, req, opts)
				default:
					_, err = e.Execute(ctx, auth, req, opts)
				}
				var status statusErr
				if !errors.As(err, &status) || status.StatusCode() != http.StatusTooManyRequests {
					t.Fatalf("expected 429, got %v", err)
				}
			}
			call(reviewHigh, `{"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`)
			if calls != 1 {
				t.Fatalf("source cooldown blocked the target: %d upstream calls", calls)
			}
			call(reviewLow, `{"messages":[{"role":"user","content":"hi"}]}`)
			if calls != 1 {
				t.Fatalf("direct target call bypassed the cooldown: %d upstream calls", calls)
			}
		})
	}
}

func TestAntigravityThinkingGroupsReviewLocalManagerCooldown(t *testing.T) {
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	auth := newIssue7865TestAuth(t.Name())
	cfg := &config.Config{}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(NewAntigravityExecutor(cfg))
	registered, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	if !manager.StoreAntigravityModelCatalog(t.Context(), registered, map[string]string{reviewHigh: "HIGH_ENUM", reviewLow: "LOW_ENUM"}, map[string]map[string]int{reviewGroup: {"low": 50000, "high": 60000}}, nil) {
		t.Fatal("failed to publish local catalog")
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: reviewHigh}, {ID: reviewLow}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	var calls int
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			return nil, errRead
		}
		if got := ojson.GetBytes(body, "model").Str; got == reviewLow {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"45"}}, Body: io.NopCloser(strings.NewReader(antigravityRateLimitNoRetryInfoBody))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`))}, nil
	}))
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	req := cliproxyexecutor.Request{Model: reviewHigh, Payload: []byte(`{"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`)}
	if _, errExecute := manager.Execute(ctx, []string{"antigravity"}, req, opts); errExecute == nil || calls != 1 {
		t.Fatalf("routed request did not exercise target cooldown: calls=%d err=%v", calls, errExecute)
	}
	req.Payload = []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	if _, errExecute := manager.Execute(ctx, []string{"antigravity"}, req, opts); errExecute != nil {
		t.Fatalf("source model was blocked by the target cooldown: %v", errExecute)
	}
	req.Model = reviewLow
	if _, errExecute := manager.Execute(ctx, []string{"antigravity"}, req, opts); errExecute == nil || calls != 2 {
		t.Fatalf("direct target escaped cooldown: calls=%d err=%v", calls, errExecute)
	}
}

func TestAntigravityThinkingGroupsReviewClaudeSearchUsesTarget(t *testing.T) {
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	auth := newIssue7865TestAuth(t.Name())
	reg := registry.GetGlobalRegistry()
	otherClient := auth.ID + "-other"
	for _, id := range []string{auth.ID, otherClient} {
		reg.RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: reviewHigh, SupportsWebSearch: true}, {ID: reviewLow, SupportsWebSearch: true}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	auth.Metadata[sdkauth.AntigravityPersistedHintsMetaKey] = sdkauth.BuildPersistedAntigravityCapabilityHints("project-1", sdkauth.AntigravityModelBaseURLDaily, sdkauth.AntigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{reviewLow: {}}, WebSearchModelIDs: map[string]struct{}{reviewLow: {}}, ExpiresAt: time.Now().Add(time.Hour),
	})
	const payload = `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"web_search","type":"web_search_20250305"}]}`
	withSearch := strings.Repeat("a", 64)
	withoutSearch := strings.Repeat("b", 64)
	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	check := func(credential *cliproxyauth.Auth, wantSearch bool) {
		t.Helper()
		got := captureAntigravityThinkingGroupRequest(t, cfg, credential, reviewHigh, sdktranslator.FormatClaude, payload, antigravityGeneratePath)
		if got.Get("model").Str != reviewLow || (got.Get("requestType").Str == "web_search") != wantSearch || got.Get("request.tools.0.googleSearch").Exists() != wantSearch {
			t.Errorf("web search should be %v for selected low: %s", wantSearch, got.Raw)
		}
	}
	setReviewThinkingGroupSnapshot(t, client, auth, withSearch, map[string]bool{reviewLow: true})
	check(auth, true)
	replacement := auth.Clone()
	setReviewThinkingGroupSnapshot(t, client, replacement, withoutSearch, nil)
	check(replacement, false)
	check(auth, true)
	setReviewThinkingGroupSnapshot(t, client, replacement, withoutSearch, map[string]bool{reviewHigh: true, reviewLow: false})
	check(replacement, false)
}

func TestAntigravityThinkingGroupsReviewLocalSearchCredentialReplacement(t *testing.T) {
	manager := cliproxyauth.NewManager(nil, nil, nil)
	auth := newIssue7865TestAuth(t.Name())
	first, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	models := map[string]string{reviewHigh: "HIGH", reviewLow: "LOW"}
	groups := map[string]map[string]int{reviewGroup: {"low": 50000, "high": 60000}}
	if !manager.StoreAntigravityModelCatalog(t.Context(), first, models, groups, map[string]bool{reviewLow: true}) {
		t.Fatal("failed to publish first catalog")
	}
	check := func(auth *cliproxyauth.Auth, model string, wantSearch bool) {
		t.Helper()
		got := cliproxyauth.SelectAntigravityModel(t.Context(), auth, reviewHigh, "low", nil)
		if got.ID != model || got.WebSearch != wantSearch {
			t.Fatalf("selection = %+v, want %s with search %v", got, model, wantSearch)
		}
	}
	check(first, reviewLow, true)
	auth.Metadata["refresh_token"] = "replacement-grant"
	replacement, errReplace := manager.Register(t.Context(), auth)
	if errReplace != nil {
		t.Fatal(errReplace)
	}
	check(replacement, reviewHigh, false)
	if !manager.StoreAntigravityModelCatalog(t.Context(), replacement, models, groups, nil) {
		t.Fatal("failed to publish replacement catalog")
	}
	check(replacement, reviewLow, false)
	check(first, reviewHigh, false)
}

func TestAntigravityThinkingGroupsReviewGenericFeedback(t *testing.T) {
	for _, mode := range []string{"generate", "claude", "stream", "stream-in-band", "no-groups"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{}
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 1)
			manager.RegisterExecutor(NewAntigravityExecutor(cfg))
			auth, errRegister := manager.Register(t.Context(), newIssue7865TestAuth(t.Name()))
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			high, low := reviewHigh, reviewLow
			if mode == "claude" {
				high, low = "claude-review-high", "claude-review-low"
			}
			groups := map[string]map[string]int{strings.TrimSuffix(high, "-high"): {"low": 50000, "high": 60000}}
			wantModel := low
			if mode == "no-groups" {
				groups, wantModel = nil, high
			}
			if !manager.StoreAntigravityModelCatalog(t.Context(), auth, map[string]string{high: "HIGH_ENUM", low: "LOW_ENUM"}, groups, nil) {
				t.Fatal("failed to publish local catalog")
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: high}, {ID: low}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			calls := 0
			var readyForSuccess bool
			var highCooldown time.Time
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					return nil, errRead
				}
				if got := ojson.GetBytes(body, "model").Str; got != wantModel {
					t.Errorf("upstream model = %q, want %q", got, wantModel)
				}
				if readyForSuccess {
					// Simulate an independent high attempt failing while low is in flight.
					manager.MarkResult(t.Context(), cliproxyauth.Result{AuthID: auth.ID, Provider: "antigravity", Model: high, CredentialVersion: auth.CredentialVersion, Error: &cliproxyauth.Error{HTTPStatus: http.StatusServiceUnavailable}})
					current, _ := manager.GetByID(auth.ID)
					highCooldown = current.ModelStates[high].NextRetryAfter
					const success = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`
					out := success
					if r.URL.Path == antigravityStreamPath {
						out = "data: " + success + "\n\n"
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(out))}, nil
				}
				if mode == "stream-in-band" {
					const failure = `{"error":{"code":503,"message":"temporarily unavailable"},"response":{"modelVersion":"untrusted-model"}}`
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("data: " + failure + "\n\n"))}, nil
				}
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily unavailable"},"modelVersion":"untrusted-model"}`))}, nil
			}))
			req := cliproxyexecutor.Request{Model: high, Payload: []byte(`{"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "public-alias"}}
			if mode == "claude" {
				req.Payload = []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}]}`)
				opts.SourceFormat = sdktranslator.FormatClaude
			}
			call := func() error {
				if strings.HasPrefix(mode, "stream") {
					result, errStream := manager.ExecuteStream(ctx, []string{"antigravity"}, req, opts)
					if errStream == nil && result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								errStream = chunk.Err
							}
						}
					}
					return errStream
				}
				_, errExecute := manager.Execute(ctx, []string{"antigravity"}, req, opts)
				return errExecute
			}
			capture := &antigravityUsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			if errCall := call(); errCall == nil || calls != 1 {
				t.Fatalf("upstream failure: calls=%d err=%v", calls, errCall)
			}
			records := drainThinkingGroupUsage(t, capture)
			if len(records) != 1 || !records[0].Failed || records[0].Model != wantModel || records[0].Alias != "public-alias" || records[0].Fail.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("503 usage = %+v, want one %s failure preserving the alias", records, wantModel)
			}
			current, _ := manager.GetByID(auth.ID)
			state := current.ModelStates[wantModel]
			if len(current.ModelStates) != 1 || state == nil || state.LastError == nil || state.LastError.HTTPStatus != http.StatusServiceUnavailable || !state.NextRetryAfter.After(time.Now()) {
				t.Errorf("503 model states = %+v, want only %s cooling", current.ModelStates, wantModel)
			}
			errBlocked := call()
			if errBlocked == nil || calls != 1 {
				t.Errorf("pre-existing target cooldown did not prevent a second upstream attempt: calls=%d err=%v", calls, errBlocked)
			}
			if mode != "no-groups" {
				var hint interface{ RetryAfter() *time.Duration }
				if !errors.As(errBlocked, &hint) || hint.RetryAfter() == nil || *hint.RetryAfter() <= 0 {
					t.Errorf("blocked target did not supply a retry delay: %v", errBlocked)
				}
			}
			if got := drainThinkingGroupUsage(t, capture); len(got) != 0 {
				t.Errorf("preflight rejection published usage: %+v", got)
			}
			current, _ = manager.GetByID(auth.ID)
			if state != nil && (len(current.ModelStates) != 1 || current.ModelStates[wantModel] == nil || !current.ModelStates[wantModel].NextRetryAfter.Equal(state.NextRetryAfter)) {
				t.Errorf("preflight changed model cooldown: %+v", current.ModelStates)
			}
			if mode == "no-groups" {
				return
			}
			manager.MarkResult(t.Context(), cliproxyauth.Result{AuthID: auth.ID, Provider: "antigravity", Model: low, CredentialVersion: auth.CredentialVersion, Success: true})
			current, _ = manager.GetByID(auth.ID)
			if lowState := current.ModelStates[low]; lowState == nil || lowState.Unavailable {
				t.Fatalf("test failed to clear low before success: %+v", lowState)
			}
			readyForSuccess = true
			if errCall := call(); errCall != nil || calls != 2 {
				t.Fatalf("successful retry: calls=%d err=%v", calls, errCall)
			}
			current, _ = manager.GetByID(auth.ID)
			if state := current.ModelStates[high]; highCooldown.IsZero() || state == nil || !state.NextRetryAfter.Equal(highCooldown) {
				t.Errorf("successful low attempt cleared or changed high cooldown: %+v", state)
			}
			if got := drainThinkingGroupUsage(t, capture); len(got) != 1 || got[0].Failed || got[0].Model != low || got[0].Alias != "public-alias" {
				t.Errorf("success usage = %+v, want one %s success preserving the alias", got, low)
			}
		})
	}
}

func TestAntigravityThinkingGroupsReviewClaudeCreditsFallback(t *testing.T) {
	const group = "claude-credits-review"
	const high, low = group + "-high", group + "-low"
	const quotaError = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`
	const rateLimitError = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`
	const capacityError = `{"error":{"code":503,"status":"UNAVAILABLE","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`
	const success = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`
	for _, tc := range []struct {
		name, model, failure, retryAfter string
		failureStatus                    int
		credits, wantRetry, stream       bool
		targetStatus                     cliproxyauth.Status
	}{
		{name: "direct-low", model: low, failure: quotaError, failureStatus: http.StatusTooManyRequests, credits: true, wantRetry: true},
		{name: "switched-low", model: high, failure: quotaError, failureStatus: http.StatusTooManyRequests, credits: true, wantRetry: true},
		{name: "switched-low-stream", model: high, failure: quotaError, failureStatus: http.StatusTooManyRequests, credits: true, wantRetry: true, stream: true},
		{name: "switched-long-rate-limit", model: high, failure: rateLimitError, failureStatus: http.StatusTooManyRequests, retryAfter: "600", credits: true, wantRetry: true},
		{name: "switched-short-rate-limit", model: high, failure: rateLimitError, failureStatus: http.StatusTooManyRequests, retryAfter: "45", credits: true},
		{name: "switched-short-quota-hint", model: high, failure: quotaError, failureStatus: http.StatusTooManyRequests, retryAfter: "45", credits: true},
		{name: "credits-disabled", model: high, failure: quotaError, failureStatus: http.StatusTooManyRequests},
		{name: "non-quota-cooldown", model: high, failure: capacityError, failureStatus: http.StatusServiceUnavailable, credits: true},
		{name: "target-disabled", model: high, failureStatus: http.StatusServiceUnavailable, credits: true, targetStatus: cliproxyauth.StatusDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := newIssue7865TestAuth(t.Name())
			attempts := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1internal:loadCodeAssist" {
					_, _ = io.WriteString(w, `{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"100","minimumCreditAmountForUsage":"50"}]}}`)
					return
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil || r.URL.Path != antigravityStreamPath || ojson.GetBytes(body, "model").Str != low || r.Header.Get("Authorization") != "Bearer token-"+auth.ID {
					t.Errorf("unexpected Claude request: path=%s body=%s err=%v", r.URL.Path, body, errRead)
				}
				credit := ojson.GetBytes(body, "enabledCreditTypes.0").Str
				attempts <- credit
				if credit == "" {
					if tc.retryAfter != "" {
						w.Header().Set("Retry-After", tc.retryAfter)
					}
					w.WriteHeader(tc.failureStatus)
					_, _ = io.WriteString(w, tc.failure)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+success+"\n\n")
			}))
			t.Cleanup(server.Close)
			auth.Attributes = map[string]string{"base_url": server.URL}
			if tc.targetStatus != "" {
				deadline := time.Now().Add(time.Hour)
				auth.ModelStates = map[string]*cliproxyauth.ModelState{low: {
					Status: tc.targetStatus, Unavailable: true, NextRetryAfter: deadline,
					Quota:     cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline},
					LastError: &cliproxyauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: quotaError},
				}}
			}
			cfg := &config.Config{QuotaExceeded: config.QuotaExceeded{AntigravityCredits: tc.credits}}
			e := NewAntigravityExecutor(cfg)
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 1)
			manager.RegisterExecutor(e)
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: high}, {ID: low}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			registered, errRegister := manager.Register(t.Context(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			if !manager.StoreAntigravityModelCatalog(t.Context(), registered, map[string]string{high: "HIGH", low: "LOW"}, map[string]map[string]int{group: {"low": 50000, "high": 60000}}, nil) {
				t.Fatal("failed to publish catalog")
			}
			cliproxyauth.SetAntigravityCreditsHintForAuth(registered, cliproxyauth.AntigravityCreditsHint{Known: true, Available: true, UpdatedAt: time.Now()})
			t.Cleanup(func() {
				cliproxyauth.SetAntigravityCreditsHintForAuth(registered, cliproxyauth.AntigravityCreditsHint{})
			})
			req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			if tc.targetStatus != "" {
				_, errExecute := e.Execute(cliproxyauth.WithAntigravityCredits(t.Context()), registered, req, opts)
				var status interface{ StatusCode() int }
				if !errors.As(errExecute, &status) || status.StatusCode() != tc.failureStatus || len(attempts) != 0 {
					t.Fatalf("credits passed disabled model: err=%v calls=%d", errExecute, len(attempts))
				}
				return
			}
			var payload []byte
			var errExecute error
			if tc.stream {
				var result *cliproxyexecutor.StreamResult
				result, errExecute = manager.ExecuteStream(t.Context(), []string{"antigravity"}, req, opts)
				if errExecute == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errExecute = chunk.Err
						}
						payload = append(payload, chunk.Payload...)
					}
				}
			} else {
				var resp cliproxyexecutor.Response
				resp, errExecute = manager.Execute(t.Context(), []string{"antigravity"}, req, opts)
				payload = resp.Payload
			}
			if tc.wantRetry {
				if errExecute != nil || !strings.Contains(string(payload), "recovered") || len(attempts) != 2 {
					t.Fatalf("expected quota recovery with one same-account credits retry, got err=%v calls=%d", errExecute, len(attempts))
				}
				if first, second := <-attempts, <-attempts; first != "" || second != "GOOGLE_ONE_AI" {
					t.Errorf("credits on initial request/retry = %q/%q", first, second)
				}
				return
			}
			var status interface{ StatusCode() int }
			if !errors.As(errExecute, &status) || status.StatusCode() != tc.failureStatus || len(attempts) != 1 || <-attempts != "" {
				t.Fatalf("unexpected retry or lost first upstream error: err=%v calls=%d", errExecute, len(attempts))
			}
			current, _ := manager.GetByID(auth.ID)
			creditsEnabled := NewAntigravityExecutor(&config.Config{QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true}})
			if _, errBlocked := creditsEnabled.Execute(t.Context(), current, req, opts); errBlocked == nil || len(attempts) != 0 {
				t.Fatalf("ordinary request passed target cooldown: err=%v calls=%d", errBlocked, len(attempts))
			}
			if tc.credits {
				if _, errBlocked := e.Execute(cliproxyauth.WithAntigravityCredits(t.Context()), current, req, opts); errBlocked == nil || len(attempts) != 0 {
					t.Fatalf("credits passed a non-quota cooldown: err=%v calls=%d", errBlocked, len(attempts))
				}
			}
		})
	}
}

// The local short cooldown predates grouped selection and must stay usage-neutral.
func TestAntigravityThinkingGroupsReviewLegacyShortCooldownUsage(t *testing.T) {
	for _, mode := range []string{"generate", "claude", "stream"} {
		t.Run(mode, func(t *testing.T) {
			auth := newIssue7865TestAuth(t.Name())
			model := reviewHigh
			if mode == "claude" {
				model = "claude-review-high"
			}
			e := NewAntigravityExecutor(&config.Config{})
			if errMark := markAntigravityShortCooldownRequired(t.Context(), auth, model, time.Now(), time.Minute); errMark != nil {
				t.Fatal(errMark)
			}
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				t.Errorf("cooldown sent an upstream request to %s", r.URL.Path)
				return nil, errors.New("unexpected upstream request")
			}))
			capture := &antigravityUsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
			var err error
			if mode == "stream" {
				_, err = e.ExecuteStream(ctx, auth, req, opts)
			} else {
				_, err = e.Execute(ctx, auth, req, opts)
			}
			var status statusErr
			if !errors.As(err, &status) || status.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("short cooldown error = %v, want 429", err)
			}
			if got := drainThinkingGroupUsage(t, capture); len(got) != 0 {
				t.Errorf("short cooldown rejection published usage: %+v", got)
			}
		})
	}
}

func TestAntigravityThinkingGroupsReviewHomeCooldownAndUsage(t *testing.T) {
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	blocked := newIssue7865TestAuth(t.Name() + "-blocked")
	next := newIssue7865TestAuth(t.Name() + "-next")
	blocked.ModelStates = map[string]*cliproxyauth.ModelState{reviewLow: {Unavailable: true, NextRetryAfter: time.Now().Add(time.Minute), Status: cliproxyauth.StatusError}}
	received := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil || ojson.GetBytes(body, "model").Str != reviewLow {
			t.Errorf("Home upstream body = %s, err=%v", body, errRead)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"temporarily unavailable"},"modelVersion":"untrusted-model"}`)
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}, OAuthRequestScopedErrors: map[string][]config.RequestScopedErrorRule{
		"antigravity": {{Status: http.StatusTooManyRequests, Action: cliproxyauth.RequestScopedActionStopAndCooldown}},
	}}
	for _, auth := range []*cliproxyauth.Auth{blocked, next} {
		setReviewThinkingGroupSnapshot(t, client, auth, strings.Repeat("a", 64), nil)
		auth.Attributes["base_url"] = server.URL
	}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, time.Second, 2)
	manager.RegisterExecutor(NewAntigravityExecutor(cfg))
	dispatch := &antigravityThinkingGroupsDispatcher{t: t, auths: []*cliproxyauth.Auth{blocked, next}}
	manager.PublishHomeDispatch(dispatch, executionregistry.New(), 1)
	blockedCapture := &antigravityUsageCapture{authID: blocked.ID, records: make(chan usage.Record, 8)}
	nextCapture := &antigravityUsageCapture{authID: next.ID, records: make(chan usage.Record, 8)}
	usage.RegisterNamedPlugin(t.Name()+"-blocked", blockedCapture)
	usage.RegisterNamedPlugin(t.Name()+"-next", nextCapture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name()+"-blocked", antigravityUsageNoop{})
		usage.RegisterNamedPlugin(t.Name()+"-next", antigravityUsageNoop{})
	})
	req := cliproxyexecutor.Request{Model: reviewHigh, Payload: []byte(`{"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "public-alias"}}
	_, errExecute := manager.Execute(t.Context(), []string{"antigravity"}, req, opts)
	var status interface{ StatusCode() int }
	if !errors.As(errExecute, &status) || status.StatusCode() != http.StatusServiceUnavailable {
		t.Errorf("Home execution returned %v, want the real upstream error", errExecute)
	}
	if dispatch.calls != 2 || len(received) != 1 || <-received != "Bearer token-"+next.ID {
		t.Fatalf("Home did not skip the blocked credential: dispatches=%d", dispatch.calls)
	}
	if got := drainThinkingGroupUsage(t, blockedCapture); len(got) != 0 {
		t.Errorf("blocked Home credential emitted usage: %+v", got)
	}
	if got := drainThinkingGroupUsage(t, nextCapture); len(got) != 1 || !got[0].Failed || got[0].Model != reviewLow || got[0].Alias != "public-alias" || got[0].Fail.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Home usage = %+v, want one real low failure preserving caller alias", got)
	}
}

func drainThinkingGroupUsage(t *testing.T, capture *antigravityUsageCapture) []usage.Record {
	t.Helper()
	const fence = "test-usage-fence"
	usage.PublishRecord(t.Context(), usage.Record{Provider: "antigravity", AuthID: capture.authID, Model: fence})
	var out []usage.Record
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case record := <-capture.records:
			if record.Model == fence {
				return out
			}
			out = append(out, record)
		case <-timeout.C:
			t.Fatal("usage queue did not reach the fence")
		}
	}
}

func TestAntigravityThinkingGroupsReviewRefreshReselectsChangedGrant(t *testing.T) {
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	auth := newIssue7865TestAuth(t.Name())
	auth.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	if !cliproxyauth.StoreAntigravityModelCatalog(t.Context(), auth, map[string]string{reviewHigh: "OLD_HIGH", reviewLow: "OLD_LOW"}, map[string]map[string]int{reviewGroup: {"low": 50000, "high": 60000}}, map[string]bool{reviewLow: true}) {
		t.Fatal("failed to publish local catalog")
	}
	var upstream ojson.Result
	var refreshCalls int
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/token":
			refreshCalls++
			body = `{"access_token":"new-access","refresh_token":"new-grant","expires_in":3600}`
		case antigravityGeneratePath:
			if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Error("generation did not use the refreshed token")
			}
			raw, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				return nil, errRead
			}
			upstream = ojson.ParseBytes(raw)
			body = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`
		default:
			return nil, errors.New("unexpected request path: " + r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	e := NewAntigravityExecutor(&config.Config{})
	const payload = `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"web_search","type":"web_search_20250305"}]}`
	req := cliproxyexecutor.Request{Model: reviewHigh, Payload: []byte(payload)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatGemini}
	if _, errExecute := e.Execute(ctx, auth, req, opts); errExecute != nil {
		t.Fatal(errExecute)
	}
	if refreshCalls != 1 || upstream.Get("model").Str != reviewHigh || upstream.Get("request.labels.model_enum").Exists() || upstream.Get("requestType").Str == "web_search" {
		t.Fatalf("old grant's catalog leaked after %d refreshes: %s", refreshCalls, upstream.Raw)
	}
}

func TestAntigravityThinkingGroupsReviewRefusalPrecedesTokenRefresh(t *testing.T) {
	auth := reviewThinkingGroupAuth(t)
	auth.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected network request: %s", r.URL.Path)
		return nil, errors.New("unexpected network request")
	}))
	e := NewAntigravityExecutor(&config.Config{})
	req := cliproxyexecutor.Request{Model: reviewHigh, Payload: []byte(`{"reasoning_effort":"low","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"unavailable"}}]}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	_, errExecute := e.Execute(ctx, auth, req, opts)
	var part *translatorcommon.UnsupportedPartError
	if !errors.As(errExecute, &part) {
		t.Fatalf("expected translation refusal without refresh, got %v", errExecute)
	}
}
