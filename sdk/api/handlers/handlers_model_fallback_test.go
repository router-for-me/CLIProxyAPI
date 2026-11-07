package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// fallbackCall records one upstream call seen by fallbackTestExecutor.
type fallbackCall struct {
	authID         string
	model          string
	requestedModel string
}

// fallbackTestExecutor fakes one provider. respond decides the outcome per call.
type fallbackTestExecutor struct {
	provider string
	respond  func(model string) fallbackOutcome

	mu    sync.Mutex
	calls []fallbackCall
}

// fallbackOutcome is either an error (returned before streaming) or a list of chunks.
type fallbackOutcome struct {
	err    error
	chunks []coreexecutor.StreamChunk
}

func (e *fallbackTestExecutor) Identifier() string { return e.provider }

func (e *fallbackTestExecutor) record(auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) fallbackOutcome {
	requested, _ := opts.Metadata[coreexecutor.RequestedModelMetadataKey].(string)
	e.mu.Lock()
	e.calls = append(e.calls, fallbackCall{authID: auth.ID, model: req.Model, requestedModel: requested})
	e.mu.Unlock()
	return e.respond(req.Model)
}

func (e *fallbackTestExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	outcome := e.record(auth, req, opts)
	if outcome.err != nil {
		return coreexecutor.Response{}, outcome.err
	}
	reporter := helps.NewUsageReporter(ctx, e.provider, req.Model, auth)
	reporter.Publish(ctx, coreusage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2})
	var payload []byte
	for _, chunk := range outcome.chunks {
		payload = append(payload, chunk.Payload...)
	}
	return coreexecutor.Response{Payload: payload}, nil
}

func (e *fallbackTestExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	outcome := e.record(auth, req, opts)
	if outcome.err != nil {
		return nil, outcome.err
	}
	chunks := make(chan coreexecutor.StreamChunk, len(outcome.chunks))
	for _, chunk := range outcome.chunks {
		chunks <- chunk
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *fallbackTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *fallbackTestExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *fallbackTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "not implemented", HTTPStatus: http.StatusNotImplemented}
}

func (e *fallbackTestExecutor) Calls() []fallbackCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]fallbackCall(nil), e.calls...)
}

func fallbackOK(body string) fallbackOutcome {
	return fallbackOutcome{chunks: []coreexecutor.StreamChunk{{Payload: []byte(body)}}}
}

func fallbackQuota() fallbackOutcome {
	return fallbackOutcome{err: &coreauth.Error{Code: "rate_limit", Message: "usage limit reached", HTTPStatus: http.StatusTooManyRequests}}
}

type fallbackFixture struct {
	handler *BaseAPIHandler
	codex   *fallbackTestExecutor
	claude  *fallbackTestExecutor
	primary string
	second  string
	third   string
}

// newFallbackFixture registers two codex credentials serving primary and one claude
// credential serving second and third. Model names are unique per test because the
// model registry is global.
func newFallbackFixture(t *testing.T, codex, claude func(model string) fallbackOutcome, fallback internalconfig.ModelFallbackConfig) *fallbackFixture {
	t.Helper()
	coreauth.SetQuotaCooldownDisabled(false)
	name := strings.NewReplacer("/", "-", " ", "-").Replace(strings.ToLower(t.Name()))
	f := &fallbackFixture{
		codex:   &fallbackTestExecutor{provider: "codex", respond: codex},
		claude:  &fallbackTestExecutor{provider: "claude", respond: claude},
		primary: name + "-primary",
		second:  name + "-second",
		third:   name + "-third",
	}
	chains := make(map[string][]string, len(fallback.Chains))
	replacer := strings.NewReplacer("PRIMARY", f.primary, "SECOND", f.second, "THIRD", f.third)
	for source, targets := range fallback.Chains {
		mapped := make([]string, 0, len(targets))
		for _, target := range targets {
			mapped = append(mapped, replacer.Replace(target))
		}
		chains[replacer.Replace(source)] = mapped
	}
	fallback.Chains = chains

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(f.codex)
	manager.RegisterExecutor(f.claude)
	for _, auth := range []*coreauth.Auth{
		{ID: name + "-codex-a", Provider: "codex", Status: coreauth.StatusActive},
		{ID: name + "-codex-b", Provider: "codex", Status: coreauth.StatusActive},
		{ID: name + "-claude", Provider: "claude", Status: coreauth.StatusActive},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		models := []*registry.ModelInfo{{ID: f.primary}}
		if auth.Provider == "claude" {
			models = []*registry.ModelInfo{{ID: f.second}, {ID: f.third}}
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, models)
		authID := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	f.handler = NewBaseAPIHandlers(&sdkconfig.SDKConfig{ModelFallback: fallback}, manager)
	return f
}

func enabledFallback(chains map[string][]string) internalconfig.ModelFallbackConfig {
	return internalconfig.ModelFallbackConfig{Enabled: true, Chains: chains}
}

func readFallbackStream(t *testing.T, dataChan <-chan []byte, errChan <-chan *interfaces.ErrorMessage) (string, *interfaces.ErrorMessage) {
	t.Helper()
	var body strings.Builder
	if dataChan != nil {
		for chunk := range dataChan {
			body.Write(chunk)
		}
	}
	var errMsg *interfaces.ErrorMessage
	for msg := range errChan {
		if msg != nil {
			errMsg = msg
		}
	}
	return body.String(), errMsg
}

func assertCodexExhausted(t *testing.T, f *fallbackFixture) {
	t.Helper()
	calls := f.codex.Calls()
	if len(calls) != 2 {
		t.Fatalf("codex calls = %+v, want both credentials tried before fallback", calls)
	}
	seen := map[string]bool{}
	for _, call := range calls {
		seen[call.authID] = true
	}
	if len(seen) != 2 {
		t.Fatalf("codex calls = %+v, want two distinct credentials", calls)
	}
}

func TestModelFallbackNonStreamFallsBackAfterAllCredentialsAreRateLimited(t *testing.T) {
	f := newFallbackFixture(t,
		func(string) fallbackOutcome { return fallbackQuota() },
		func(model string) fallbackOutcome { return fallbackOK("claude:" + model) },
		enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}),
	)
	capture := &issue7056UsageCapture{records: make(chan coreusage.Record, 4), model: f.second}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), &issue7056UsageCapture{}) })

	body, headers, errMsg := f.handler.ExecuteWithAuthManager(context.Background(), "openai", f.primary, []byte(`{"model":"`+f.primary+`"}`), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager error = %+v, want fallback success", errMsg)
	}
	if string(body) != "claude:"+f.second {
		t.Fatalf("body = %q, want response from %s", body, f.second)
	}
	if got := headers.Get(ModelFallbackHeader); got != f.second {
		t.Fatalf("%s = %q, want %q", ModelFallbackHeader, got, f.second)
	}
	assertCodexExhausted(t, f)
	claudeCalls := f.claude.Calls()
	if len(claudeCalls) != 1 || claudeCalls[0].model != f.second || claudeCalls[0].requestedModel != f.primary {
		t.Fatalf("claude calls = %+v, want one call for %s with requested_model %s", claudeCalls, f.second, f.primary)
	}
	select {
	case record := <-capture.records:
		if record.Model != f.second || record.Alias != f.primary || record.Provider != "claude" {
			t.Fatalf("usage record model=%q alias=%q provider=%q, want real model %q, alias %q, provider claude", record.Model, record.Alias, record.Provider, f.second, f.primary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage record of the fallback model")
	}
}

func TestModelFallbackStreamFallsBackBeforeFirstByte(t *testing.T) {
	for _, mode := range []string{"execute-error", "bootstrap-chunk-error"} {
		t.Run(mode, func(t *testing.T) {
			f := newFallbackFixture(t,
				func(string) fallbackOutcome {
					if mode == "execute-error" {
						return fallbackQuota()
					}
					return fallbackOutcome{chunks: []coreexecutor.StreamChunk{{Err: fallbackQuota().err}}}
				},
				func(model string) fallbackOutcome { return fallbackOK("claude:" + model) },
				enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}),
			)
			dataChan, headers, errChan := f.handler.ExecuteStreamWithAuthManager(context.Background(), "openai", f.primary, []byte(`{"model":"`+f.primary+`"}`), "")
			body, errMsg := readFallbackStream(t, dataChan, errChan)
			if errMsg != nil {
				t.Fatalf("stream error = %+v, want fallback success", errMsg)
			}
			if body != "claude:"+f.second {
				t.Fatalf("stream body = %q, want response from %s", body, f.second)
			}
			if got := headers.Get(ModelFallbackHeader); got != f.second {
				t.Fatalf("%s = %q, want %q", ModelFallbackHeader, got, f.second)
			}
			assertCodexExhausted(t, f)
			if calls := f.claude.Calls(); len(calls) != 1 || calls[0].requestedModel != f.primary {
				t.Fatalf("claude calls = %+v, want one fallback call with requested_model %s", calls, f.primary)
			}
		})
	}
}

func TestModelFallbackSkipsIneligibleErrors(t *testing.T) {
	f := newFallbackFixture(t,
		func(string) fallbackOutcome {
			return fallbackOutcome{err: &coreauth.Error{Code: "invalid_request", Message: "bad request", HTTPStatus: http.StatusBadRequest}}
		},
		func(model string) fallbackOutcome { return fallbackOK("claude:" + model) },
		enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}),
	)
	_, headers, errMsg := f.handler.ExecuteWithAuthManager(context.Background(), "openai", f.primary, []byte(`{}`), "")
	if errMsg == nil || errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %+v, want the primary 400", errMsg)
	}
	if headers.Get(ModelFallbackHeader) != "" {
		t.Fatal("fallback header set on an ineligible failure")
	}
	if calls := f.claude.Calls(); len(calls) != 0 {
		t.Fatalf("claude calls = %+v, want none for a 400", calls)
	}

	dataChan, _, errChan := f.handler.ExecuteStreamWithAuthManager(context.Background(), "openai", f.primary, []byte(`{}`), "")
	if _, streamErr := readFallbackStream(t, dataChan, errChan); streamErr == nil || streamErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("stream error = %+v, want the primary 400", streamErr)
	}
	if calls := f.claude.Calls(); len(calls) != 0 {
		t.Fatalf("claude calls = %+v, want none for a streaming 400", calls)
	}
}

func TestModelFallbackNeverSwitchesAfterFirstChunk(t *testing.T) {
	f := newFallbackFixture(t,
		func(string) fallbackOutcome {
			return fallbackOutcome{chunks: []coreexecutor.StreamChunk{{Payload: []byte("partial")}, {Err: fallbackQuota().err}}}
		},
		func(model string) fallbackOutcome { return fallbackOK("claude:" + model) },
		enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}),
	)
	dataChan, headers, errChan := f.handler.ExecuteStreamWithAuthManager(context.Background(), "openai", f.primary, []byte(`{}`), "")
	body, errMsg := readFallbackStream(t, dataChan, errChan)
	if body != "partial" {
		t.Fatalf("stream body = %q, want only the primary's partial output", body)
	}
	if errMsg == nil || errMsg.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("stream error = %+v, want the mid-stream 429", errMsg)
	}
	if headers.Get(ModelFallbackHeader) != "" {
		t.Fatal("fallback header set on a primary stream")
	}
	if calls := f.claude.Calls(); len(calls) != 0 {
		t.Fatalf("claude calls = %+v, want no fallback after the first chunk", calls)
	}
}

func TestModelFallbackHonorsCyclesAndMaxHops(t *testing.T) {
	allQuota := func(string) fallbackOutcome { return fallbackQuota() }
	for _, test := range []struct {
		name       string
		maxHops    int
		wantClaude []string
	}{
		{name: "max-hops-1", maxHops: 1, wantClaude: []string{"SECOND"}},
		{name: "cycle-skipped", maxHops: 3, wantClaude: []string{"SECOND", "THIRD"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fallback := enabledFallback(map[string][]string{
				"PRIMARY": {"SECOND", "PRIMARY", "SECOND", "THIRD"},
				"SECOND":  {"PRIMARY"},
			})
			fallback.MaxHops = test.maxHops
			f := newFallbackFixture(t, allQuota, allQuota, fallback)
			_, _, errMsg := f.handler.ExecuteWithAuthManager(context.Background(), "openai", f.primary, []byte(`{}`), "")
			if errMsg == nil || errMsg.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("error = %+v, want 429 after the chain is exhausted", errMsg)
			}
			var got []string
			for _, call := range f.claude.Calls() {
				got = append(got, call.model)
			}
			want := make([]string, 0, len(test.wantClaude))
			for _, model := range test.wantClaude {
				want = append(want, strings.NewReplacer("SECOND", f.second, "THIRD", f.third).Replace(model))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("claude models = %v, want %v", got, want)
			}
			assertCodexExhausted(t, f)
		})
	}
}

func TestModelFallbackChainExhaustedReturnsMostInformativeError(t *testing.T) {
	t.Run("primary error wins", func(t *testing.T) {
		f := newFallbackFixture(t,
			func(string) fallbackOutcome { return fallbackQuota() },
			func(string) fallbackOutcome {
				return fallbackOutcome{err: &coreauth.Error{Code: "overloaded", Message: "claude overloaded", HTTPStatus: 529}}
			},
			enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}),
		)
		_, _, errMsg := f.handler.ExecuteWithAuthManager(context.Background(), "openai", f.primary, []byte(`{}`), "")
		if errMsg == nil || errMsg.StatusCode != http.StatusTooManyRequests || !strings.Contains(errMsg.Error.Error(), "usage limit reached") {
			t.Fatalf("error = %+v, want the requested model's 429", errMsg)
		}
		if calls := f.claude.Calls(); len(calls) != 1 {
			t.Fatalf("claude calls = %+v, want one fallback attempt", calls)
		}
	})
	t.Run("upstream error beats unserved primary", func(t *testing.T) {
		f := newFallbackFixture(t,
			func(string) fallbackOutcome { return fallbackQuota() },
			func(string) fallbackOutcome {
				return fallbackOutcome{err: &coreauth.Error{Code: "overloaded", Message: "claude overloaded", HTTPStatus: 529}}
			},
			enabledFallback(map[string][]string{"PRIMARY-unserved": {"SECOND"}}),
		)
		dataChan, _, errChan := f.handler.ExecuteStreamWithAuthManager(context.Background(), "openai", f.primary+"-unserved", []byte(`{}`), "")
		_, errMsg := readFallbackStream(t, dataChan, errChan)
		if errMsg == nil || errMsg.StatusCode != 529 {
			t.Fatalf("stream error = %+v, want the fallback model's 529", errMsg)
		}
		if calls := f.claude.Calls(); len(calls) != 1 || calls[0].requestedModel != f.primary+"-unserved" {
			t.Fatalf("claude calls = %+v, want one call keeping the requested model", calls)
		}
	})
}

func TestModelFallbackCountTokensUsesChain(t *testing.T) {
	f := newFallbackFixture(t,
		func(string) fallbackOutcome { return fallbackQuota() },
		func(model string) fallbackOutcome { return fallbackOK(`{"input_tokens":3}`) },
		enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}),
	)
	body, headers, errMsg := f.handler.ExecuteCountWithAuthManager(context.Background(), "claude", f.primary, []byte(`{}`), "")
	if errMsg != nil {
		t.Fatalf("count error = %+v, want fallback success", errMsg)
	}
	if string(body) != `{"input_tokens":3}` || headers.Get(ModelFallbackHeader) != f.second {
		t.Fatalf("count body=%q header=%q, want fallback count from %s", body, headers.Get(ModelFallbackHeader), f.second)
	}
}

func TestModelFallbackFollowsConfigHotReload(t *testing.T) {
	f := newFallbackFixture(t,
		func(string) fallbackOutcome { return fallbackQuota() },
		func(model string) fallbackOutcome { return fallbackOK("claude:" + model) },
		internalconfig.ModelFallbackConfig{Chains: map[string][]string{"PRIMARY": {"SECOND"}}},
	)
	// Disabled: the primary error reaches the client.
	if _, _, errMsg := f.handler.ExecuteWithAuthManager(context.Background(), "openai", f.primary, []byte(`{}`), ""); errMsg == nil {
		t.Fatal("disabled fallback served the request")
	}
	enabled := *f.handler.CurrentConfig()
	enabled.ModelFallback.Enabled = true
	f.handler.UpdateClients(&enabled)
	body, _, errMsg := f.handler.ExecuteWithAuthManager(context.Background(), "openai", f.primary, []byte(`{}`), "")
	if errMsg != nil || string(body) != "claude:"+f.second {
		t.Fatalf("after reload body=%q err=%+v, want fallback success", body, errMsg)
	}
	// Narrowing the conditions disables the fallback again without a restart.
	narrowed := enabled
	narrowed.ModelFallback.On = []string{"503"}
	f.handler.UpdateClients(&narrowed)
	claudeCalls := len(f.claude.Calls())
	if _, _, errMsg = f.handler.ExecuteWithAuthManager(context.Background(), "openai", f.primary, []byte(`{}`), ""); errMsg == nil {
		t.Fatal("fallback ignored the reloaded conditions")
	}
	if len(f.claude.Calls()) != claudeCalls {
		t.Fatal("fallback model called although 429 is no longer eligible")
	}
}

func TestModelFallbackReasonNormalizesProviderSignals(t *testing.T) {
	defaults := internalconfig.ModelFallbackConfig{}.Conditions()
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "429", err: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "slow down"}, want: "429"},
		{name: "529", err: &coreauth.Error{HTTPStatus: 529, Message: "overloaded"}, want: "529"},
		{name: "403 quota", err: &coreauth.Error{HTTPStatus: http.StatusForbidden, Message: `{"error":{"status":"RESOURCE_EXHAUSTED"}}`}},
		{name: "402 insufficient quota", err: &coreauth.Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient_quota"}, want: internalconfig.ModelFallbackOnQuotaExhausted},
		{name: "cooldown", err: coreauth.NewModelCooldownError("m", "codex", time.Minute), want: "429"},
		{name: "auth unavailable", err: &coreauth.Error{Code: "auth_unavailable", Message: "no auth available"}, want: internalconfig.ModelFallbackOnAuthUnavailable},
		{name: "403 plain", err: &coreauth.Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"}},
		{name: "401", err: &coreauth.Error{HTTPStatus: http.StatusUnauthorized, Message: "quota of tokens is fine but unauthorized"}},
		{name: "400", err: &coreauth.Error{HTTPStatus: http.StatusBadRequest, Message: "bad"}},
		{name: "500 not default", err: &coreauth.Error{HTTPStatus: http.StatusInternalServerError, Message: "boom"}},
		{name: "canceled", err: context.Canceled},
		{name: "wrapped 429", err: errors.Join(errors.New("ctx"), &coreauth.Error{HTTPStatus: http.StatusTooManyRequests}), want: "429"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := test.want
			got, ok := modelFallbackReason(context.Background(), defaults, test.err)
			if got != want || ok != (want != "") {
				t.Fatalf("modelFallbackReason = (%q, %v), want %q", got, ok, want)
			}
		})
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := modelFallbackReason(canceledCtx, defaults, &coreauth.Error{HTTPStatus: http.StatusTooManyRequests}); ok {
		t.Fatal("canceled client request must not fall back")
	}
}

func TestModelFallbackDisabledForHomeAndForcedProvider(t *testing.T) {
	f := newFallbackFixture(t,
		func(string) fallbackOutcome { return fallbackQuota() },
		func(model string) fallbackOutcome { return fallbackOK("claude:" + model) },
		enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}),
	)
	if !f.handler.modelFallbackApplies(f.primary, modelExecutionOptions{}) {
		t.Fatal("fallback should apply to a normal request")
	}
	if f.handler.modelFallbackApplies(f.primary, modelExecutionOptions{ForcedProvider: "codex"}) {
		t.Fatal("fallback must not apply to forced-provider executions")
	}
	f.handler.AuthManager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	if f.handler.modelFallbackApplies(f.primary, modelExecutionOptions{}) {
		t.Fatal("fallback must not apply to Home dispatch")
	}
}
