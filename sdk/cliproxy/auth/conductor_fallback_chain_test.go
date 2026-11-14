package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/openai"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

type fallbackTestError struct {
	status  int
	body    string
	retry   *time.Duration
	headers http.Header
}

func (e *fallbackTestError) Error() string                { return e.body }
func (e *fallbackTestError) StatusCode() int              { return e.status }
func (e *fallbackTestError) ResponseHeaders() http.Header { return e.headers }
func (e *fallbackTestError) RetryAfter() *time.Duration   { return e.retry }

func TestFallbackExhaustionClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want fallbackExhaustion
	}{
		{"nil", nil, fallbackNotExhausted},
		{"payment", &Error{HTTPStatus: 402, Message: "Insufficient Balance"}, fallbackTerminal},
		{"wrapped payment", fmt.Errorf("upstream: %w", &Error{HTTPStatus: 402}), fallbackTerminal},
		{"unauthorized", &Error{HTTPStatus: 401}, fallbackNotExhausted},
		{"server quota text", &Error{HTTPStatus: 500, Code: "quota_exhausted"}, fallbackNotExhausted},
		{"unknown rate limit", &Error{HTTPStatus: 429}, fallbackAmbiguous},
		{"zero rate limit", &fallbackTestError{status: 429, retry: new(time.Duration(0))}, fallbackNotExhausted},
		{"short rate limit", &fallbackTestError{status: 429, retry: new(time.Second)}, fallbackNotExhausted},
		{"ten second boundary", &fallbackTestError{status: 429, retry: new(10 * time.Second)}, fallbackNotExhausted},
		{"intermediate", &fallbackTestError{status: 429, retry: new(11 * time.Second)}, fallbackIntermediate},
		{"fifteen minute boundary", &fallbackTestError{status: 429, retry: new(15 * time.Minute)}, fallbackIntermediate},
		{"multi-hour", &fallbackTestError{status: 429, retry: new(2 * time.Hour)}, fallbackTerminal},
		{"retry header zero", &fallbackTestError{status: 429, headers: http.Header{"Retry-After": {"0"}}}, fallbackNotExhausted},
		{"quota code", &Error{HTTPStatus: 429, Code: "usage_limit_reached"}, fallbackTerminal},
		{"nested quota code", &fallbackTestError{status: 429, body: `{"error":{"code":"quota_exhausted"}}`}, fallbackTerminal},
		{"top-level quota type", &fallbackTestError{status: 429, body: `{"type":"usage_limit_reached"}`}, fallbackTerminal},
		{"non-quota message", &fallbackTestError{status: 429, body: `{"error":{"message":"quota_exhausted"}}`}, fallbackAmbiguous},
		{"relative reset", &fallbackTestError{status: 429, body: `{"error":{"resets_in_seconds":7200}}`}, fallbackTerminal},
		{"intermediate reset", &fallbackTestError{status: 429, body: `{"resets_in_seconds":300}`}, fallbackIntermediate},
		{"negative reset", &fallbackTestError{status: 429, body: `{"resets_in_seconds":-300}`}, fallbackNotExhausted},
		{"huge reset", &fallbackTestError{status: 429, body: `{"resets_in_seconds":1e100}`}, fallbackTerminal},
		{"retry header", &fallbackTestError{status: 429, headers: http.Header{"Retry-After": {"7200"}}}, fallbackTerminal},
		{"date header", &fallbackTestError{status: 429, headers: http.Header{"Retry-After": {time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)}}}, fallbackTerminal},
		{"intermediate header", &fallbackTestError{status: 429, headers: http.Header{"Retry-After": {"300"}}}, fallbackIntermediate},
		{"invalid header", &fallbackTestError{status: 429, headers: http.Header{"Retry-After": {"tomorrow"}}}, fallbackAmbiguous},
		{"request scoped payment", NewRequestScopedError("payment in request", 402), fallbackNotExhausted},
		{"plugin response", &cliproxyexecutor.RequestTerminatedError{HTTPStatus: 402}, fallbackNotExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := fallbackExhaustionKind(test.err); got != test.want {
				t.Fatalf("classification = %v, want %v", got, test.want)
			}
		})
	}
}

type fallbackTestSelector struct{}

func (fallbackTestSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	var selected *Auth
	for _, auth := range auths {
		if selected == nil || auth.ID < selected.ID {
			selected = auth
		}
	}
	return selected, nil
}

type fallbackTestCall struct {
	authID string
	model  string
	opts   cliproxyexecutor.Options
	req    cliproxyexecutor.Request
}

type fallbackTestExecutor struct {
	provider string
	calls    *[]fallbackTestCall
	failures map[string]error
	inspect  func(cliproxyexecutor.Request, cliproxyexecutor.Options) error
	chunks   []cliproxyexecutor.StreamChunk
}

func (e *fallbackTestExecutor) Identifier() string { return e.provider }
func (e *fallbackTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	*e.calls = append(*e.calls, fallbackTestCall{authID: auth.ID, model: req.Model, opts: opts, req: req})
	if e.inspect != nil {
		if err := e.inspect(req, opts); err != nil {
			return cliproxyexecutor.Response{}, err
		}
	}
	if err := e.failures[auth.ID]; err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
}
func (e *fallbackTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}
func (e *fallbackTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if _, err := e.Execute(ctx, auth, req, opts); err != nil {
		return nil, err
	}
	chunks := e.chunks
	if chunks == nil {
		chunks = []cliproxyexecutor.StreamChunk{{Payload: []byte(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n")}}
	}
	channel := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		channel <- chunk
	}
	close(channel)
	return &cliproxyexecutor.StreamResult{Chunks: channel}, nil
}
func (*fallbackTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }
func (*fallbackTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("HTTP passthrough is not used by the fallback conductor")
}

type fallbackFixture struct {
	manager    *Manager
	calls      []fallbackTestCall
	executors  map[string]*fallbackTestExecutor
	candidates []internalconfig.FallbackCandidate
}

func newFallbackFixture(t *testing.T, providers ...string) *fallbackFixture {
	t.Helper()
	fixture := &fallbackFixture{manager: NewManager(nil, fallbackTestSelector{}, nil), executors: make(map[string]*fallbackTestExecutor)}
	fixture.manager.SetRetryConfig(0, 0, 1)
	for index, provider := range providers {
		candidate := internalconfig.FallbackCandidate{Provider: provider, Model: fmt.Sprintf("fallback-model-%d", index)}
		fixture.candidates = append(fixture.candidates, candidate)
		executor := &fallbackTestExecutor{provider: provider, calls: &fixture.calls, failures: make(map[string]error)}
		fixture.executors[provider] = executor
		fixture.manager.RegisterExecutor(executor)
	}
	fixture.configure(true, "15m")
	return fixture
}

func (f *fallbackFixture) configure(sticky bool, cooldown string) {
	f.manager.SetConfig(&internalconfig.Config{FallbackChain: map[string]internalconfig.FallbackChainConfig{
		"route-alias": {Candidates: f.candidates, StickyPrimary: sticky, ExhaustionCooldown: cooldown},
	}})
}

func (f *fallbackFixture) register(t *testing.T, index int, suffix string) string {
	t.Helper()
	candidate := f.candidates[index]
	id := strings.ReplaceAll(t.Name(), "/", "-") + "-" + candidate.Provider + "-" + suffix
	registry.GetGlobalRegistry().RegisterClient(id, candidate.Provider, []*registry.ModelInfo{{ID: candidate.Model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, err := f.manager.Register(context.Background(), &Auth{ID: id, Provider: candidate.Provider, Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fallbackFixture) ids() []string {
	ids := make([]string, len(f.calls))
	for index, call := range f.calls {
		ids[index] = call.authID
	}
	return ids
}

func (f *fallbackFixture) invoke(mode string, opts cliproxyexecutor.Options) error {
	req := cliproxyexecutor.Request{Model: "route-alias", Payload: []byte(`{"model":"route-alias","messages":[{"role":"user","content":"hi"}]}`)}
	switch mode {
	case "stream":
		opts.Stream = true
		result, err := f.manager.ExecuteStream(context.Background(), []string{f.candidates[0].Provider}, req, opts)
		if err != nil {
			return err
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	case "count":
		_, err := f.manager.ExecuteCount(context.Background(), []string{f.candidates[0].Provider}, req, opts)
		return err
	default:
		_, err := f.manager.Execute(context.Background(), []string{f.candidates[0].Provider}, req, opts)
		return err
	}
}

func TestFallbackChainExhaustsEveryPoolBeforeAdvancing(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			f := newFallbackFixture(t, "codex", "grok", "claude")
			var expected []string
			for index, candidate := range f.candidates {
				for _, suffix := range []string{"a", "b"} {
					id := f.register(t, index, suffix)
					if index == 0 {
						f.executors[candidate.Provider].failures[id] = &Error{HTTPStatus: 402, Message: "monthly balance exhausted"}
					} else if index == 1 {
						f.executors[candidate.Provider].failures[id] = &fallbackTestError{status: 429, retry: new(3 * time.Hour)}
					}
					if index < 2 || suffix == "a" {
						expected = append(expected, id)
					}
				}
			}
			if err := f.invoke(mode, cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			if got := f.ids(); !reflect.DeepEqual(got, expected) {
				t.Fatalf("credential order = %v, want %v", got, expected)
			}
			for _, call := range f.calls {
				if got := authSelectionModelFromOptions(call.opts, ""); got != call.model {
					t.Fatalf("selection model = %q, execution model = %q", got, call.model)
				}
				if got := requestedModelAliasFromOptions(call.opts, ""); got != "route-alias" {
					t.Fatalf("requested alias = %q", got)
				}
			}
		})
	}
}

func TestFallbackChainKeepsHealthyCredentialInPrimaryPool(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			f := newFallbackFixture(t, "codex", "claude")
			first := f.register(t, 0, "a")
			second := f.register(t, 0, "b")
			f.register(t, 1, "a")
			f.executors["codex"].failures[first] = &Error{HTTPStatus: 402}
			if err := f.invoke(mode, cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			if got := f.ids(); !reflect.DeepEqual(got, []string{first, second}) {
				t.Fatalf("calls = %v, want primary pool only", got)
			}
		})
	}
}

func TestFallbackChainRateLimitAndMixedFailurePolicy(t *testing.T) {
	for _, test := range []struct {
		name            string
		first           error
		second          error
		coolingDisabled bool
		fallback        bool
	}{
		{"terminal quota code", &Error{HTTPStatus: 429, Code: "usage_limit_reached"}, &Error{HTTPStatus: 429, Code: "quota_exhausted"}, false, true},
		{"all intermediate cooled", &fallbackTestError{status: 429, retry: new(30 * time.Second)}, &fallbackTestError{status: 429, retry: new(time.Minute)}, false, true},
		{"intermediate without cooldown", &fallbackTestError{status: 429, retry: new(30 * time.Second)}, &fallbackTestError{status: 429, retry: new(time.Minute)}, true, false},
		{"transient", &fallbackTestError{status: 429, retry: new(5 * time.Second)}, &fallbackTestError{status: 429, retry: new(5 * time.Second)}, false, false},
		{"ambiguous 429 full pool", &Error{HTTPStatus: 429, Message: "rate limit exceeded without quota code"}, &Error{HTTPStatus: 429, Message: "rate limit exceeded without quota code"}, false, true},
		{"transient before terminal", &fallbackTestError{status: 429, retry: new(time.Second)}, &Error{HTTPStatus: 402}, false, false},
		{"server error before terminal", &Error{HTTPStatus: 500}, &Error{HTTPStatus: 402}, false, false},
		{"terminal before server error", &Error{HTTPStatus: 402}, &Error{HTTPStatus: 500}, false, false},
	} {
		for _, mode := range []string{"execute", "count", "stream"} {
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				f := newFallbackFixture(t, "codex", "claude")
				first := f.register(t, 0, "a")
				second := f.register(t, 0, "b")
				fallback := f.register(t, 1, "a")
				f.executors["codex"].failures[first] = test.first
				f.executors["codex"].failures[second] = test.second
				if test.coolingDisabled {
					for _, id := range []string{first, second} {
						auth, _ := f.manager.GetByID(id)
						auth.Metadata = map[string]any{"disable_cooling": true}
						if _, err := f.manager.Update(context.Background(), auth); err != nil {
							t.Fatal(err)
						}
					}
				}
				err := f.invoke(mode, cliproxyexecutor.Options{})
				if (err == nil) != test.fallback {
					t.Fatalf("error = %v, want fallback success = %v", err, test.fallback)
				}
				expected := []string{first, second}
				if test.fallback {
					expected = append(expected, fallback)
				}
				if got := f.ids(); !reflect.DeepEqual(got, expected) {
					t.Fatalf("calls = %v, want %v", got, expected)
				}
			})
		}
	}
}

func TestFallbackChainStickyPrimaryAndCooldown(t *testing.T) {
	for _, sticky := range []bool{true, false} {
		t.Run(fmt.Sprint(sticky), func(t *testing.T) {
			f := newFallbackFixture(t, "codex", "claude")
			f.configure(sticky, "15m")
			primary := f.register(t, 0, "a")
			secondary := f.register(t, 1, "a")
			f.executors["codex"].failures[primary] = &Error{HTTPStatus: 402}
			if err := f.invoke("execute", cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			delete(f.executors["codex"].failures, primary)
			f.manager.MarkResult(context.Background(), Result{AuthID: primary, Provider: "codex", Model: f.candidates[0].Model, Success: true})
			f.calls = nil
			if err := f.invoke("execute", cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			if got := f.ids(); !reflect.DeepEqual(got, []string{secondary}) {
				t.Fatalf("active cooldown calls = %v", got)
			}
			f.manager.fallbackChain.mu.Lock()
			f.manager.fallbackChain.chains["route-alias"].cooldowns[0] = time.Now().Add(-time.Second)
			f.manager.fallbackChain.mu.Unlock()
			f.calls = nil
			if err := f.invoke("execute", cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			want := secondary
			if sticky {
				want = primary
			}
			if got := f.ids(); !reflect.DeepEqual(got, []string{want}) {
				t.Fatalf("recovered calls = %v, want %s", got, want)
			}
		})
	}
}

func TestFallbackChainPreCooledPoolAndEligibility(t *testing.T) {
	f := newFallbackFixture(t, "codex", "claude")
	primary := f.register(t, 0, "a")
	excluded := f.register(t, 0, "excluded")
	disabled := f.register(t, 0, "disabled")
	secondary := f.register(t, 1, "a")
	auth, _ := f.manager.GetByID(disabled)
	auth.Disabled = true
	if _, err := f.manager.Update(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	retry := time.Hour
	f.manager.MarkResult(context.Background(), Result{AuthID: primary, Provider: "codex", Model: f.candidates[0].Model, Error: &Error{HTTPStatus: 429, Code: "usage_limit_reached"}, RetryAfter: &retry})
	if err := f.invoke("execute", cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExcludedAuthIDsMetadataKey: []string{excluded}}}); err != nil {
		t.Fatal(err)
	}
	if got := f.ids(); !reflect.DeepEqual(got, []string{secondary}) {
		t.Fatalf("pre-cooled pool calls = %v", got)
	}
}

func TestFallbackChainAllCandidatesExhausted(t *testing.T) {
	f := newFallbackFixture(t, "codex", "claude")
	first := f.register(t, 0, "a")
	second := f.register(t, 1, "a")
	f.executors["codex"].failures[first] = &Error{HTTPStatus: 402}
	f.executors["claude"].failures[second] = &Error{HTTPStatus: 429, Code: "quota_exhausted"}
	if err := f.invoke("execute", cliproxyexecutor.Options{}); statusCodeFromError(err) != 429 {
		t.Fatalf("terminal error = %v", err)
	}
	f.calls = nil
	err := f.invoke("execute", cliproxyexecutor.Options{})
	if statusCodeFromError(err) != 429 || retryAfterFromError(err) == nil || *retryAfterFromError(err) <= 0 || len(f.calls) != 0 {
		t.Fatalf("cached exhaustion error = %v, calls = %v", err, f.ids())
	}
}

func TestFallbackChainStreamingBootstrapVersusCommittedError(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			f := newFallbackFixture(t, "codex", "claude")
			primary := f.register(t, 0, "a")
			secondary := f.register(t, 1, "a")
			quota := &Error{HTTPStatus: 429, Code: "usage_limit_reached"}
			if committed {
				f.executors["codex"].chunks = []cliproxyexecutor.StreamChunk{{Payload: []byte(`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n")}, {Err: quota}}
			} else {
				f.executors["codex"].chunks = []cliproxyexecutor.StreamChunk{{Err: quota}}
			}
			err := f.invoke("stream", cliproxyexecutor.Options{})
			expected := []string{primary, secondary}
			if committed {
				expected = []string{primary}
				if statusCodeFromError(err) != 429 {
					t.Fatalf("committed stream error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := f.ids(); !reflect.DeepEqual(got, expected) {
				t.Fatalf("calls = %v, want %v", got, expected)
			}
		})
	}
}

func TestFallbackChainPreservesSourceThinkingEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   sdktranslator.Format
		provider string
		target   sdktranslator.Format
		payload  string
		field    string
	}{
		{"reasoning to thinking", sdktranslator.FormatOpenAI, "claude", sdktranslator.FormatClaude, `{"model":"fallback-model-1","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`, "output_config.effort"},
		{"thinking to reasoning", sdktranslator.FormatClaude, "openai", sdktranslator.FormatOpenAI, `{"model":"fallback-model-1","max_tokens":32768,"messages":[{"role":"user","content":"hello"}],"thinking":{"type":"enabled","budget_tokens":8192}}`, "reasoning_effort"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFallbackFixture(t, "codex", test.provider)
			primary := f.register(t, 0, "a")
			fallbackAuth := f.register(t, 1, "a")
			f.executors["codex"].failures[primary] = &Error{HTTPStatus: 402}
			payload := []byte(test.payload)
			seen := false
			f.executors[test.provider].inspect = func(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				seen = true
				if opts.SourceFormat != test.source || opts.ResponseFormat != test.source || string(opts.OriginalRequest) != test.payload {
					return errors.New("fallback changed source/response protocol or original request")
				}
				translated := sdktranslator.TranslateRequest(opts.SourceFormat, test.target, req.Model, req.Payload, false)
				originalSource := opts.OriginalRequest
				if len(originalSource) == 0 {
					originalSource = req.Payload
				}
				summaryConfig := thinking.ExtractSummaryConfig(originalSource, opts.SourceFormat.String())
				modelInfo := &registry.ModelInfo{
					ID:       req.Model,
					Thinking: &registry.ThinkingSupport{ZeroAllowed: false, Levels: []string{"low", "medium", "high"}},
				}
				body, err := thinking.ApplyThinkingWithModelInfoAndSummary(translated, originalSource, req.Model, opts.SourceFormat.String(), test.target.String(), test.provider, modelInfo, summaryConfig)
				if err != nil {
					return err
				}
				if !ojson.GetBytes(body, test.field).Exists() {
					return fmt.Errorf("missing adapted %s in %s", test.field, body)
				}
				return nil
			}
			_ = fallbackAuth
			opts := cliproxyexecutor.Options{SourceFormat: test.source, ResponseFormat: test.source, OriginalRequest: payload, Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: "route-alias"}}
			_, err := f.manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "route-alias", Payload: payload, Format: test.source}, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !seen {
				t.Fatal("fallback translator was not reached")
			}
			if string(payload) != test.payload || opts.Metadata[cliproxyexecutor.AuthSelectionModelMetadataKey] != "route-alias" {
				t.Fatal("caller envelope mutated")
			}
		})
	}
}

func TestFallbackChainConfigValidationAndLookup(t *testing.T) {
	for _, test := range []struct {
		name  string
		chain internalconfig.FallbackChainConfig
	}{
		{"empty candidates", internalconfig.FallbackChainConfig{}},
		{"empty provider", internalconfig.FallbackChainConfig{Candidates: []internalconfig.FallbackCandidate{{Model: "model"}}}},
		{"empty model", internalconfig.FallbackChainConfig{Candidates: []internalconfig.FallbackCandidate{{Provider: "codex"}}}},
		{"invalid cooldown", internalconfig.FallbackChainConfig{Candidates: []internalconfig.FallbackCandidate{{Provider: "codex", Model: "model"}}, ExhaustionCooldown: "tomorrow"}},
		{"zero cooldown", internalconfig.FallbackChainConfig{Candidates: []internalconfig.FallbackCandidate{{Provider: "codex", Model: "model"}}, ExhaustionCooldown: "0s"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFallbackFixture(t, "codex")
			f.manager.SetConfig(&internalconfig.Config{FallbackChain: map[string]internalconfig.FallbackChainConfig{"route-alias": test.chain}})
			if err := f.invoke("execute", cliproxyexecutor.Options{}); statusCodeFromError(err) != 400 || len(f.calls) != 0 {
				t.Fatalf("error = %v, calls = %v", err, f.ids())
			}
		})
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{FallbackChain: map[string]internalconfig.FallbackChainConfig{"route": {}, "codex:route": {}}})
	for _, test := range []struct {
		providers   []string
		model, want string
		configured  bool
	}{
		{[]string{"codex"}, "route", "codex:route", true},
		{[]string{"claude"}, "route", "route", true},
		{[]string{"codex", "claude"}, "route", "route", true},
		{[]string{"codex"}, "other", "", false},
		{[]string{"codex"}, "codex:route", "codex:route", true},
	} {
		route, _, configured := manager.fallbackChainForRequest(test.providers, cliproxyexecutor.Request{Model: test.model}, cliproxyexecutor.Options{})
		if route != test.want || configured != test.configured {
			t.Fatalf("lookup %v/%s = %q/%v", test.providers, test.model, route, configured)
		}
	}
}

func TestAdaptFallbackEnvelopeRejectsInvalidPayload(t *testing.T) {
	for _, payload := range []string{"{", "null", "[]"} {
		_, _, err := adaptFallbackEnvelope(cliproxyexecutor.Request{Model: "route", Payload: []byte(payload)}, cliproxyexecutor.Options{}, internalconfig.FallbackCandidate{Provider: "claude", Model: "target"})
		if statusCodeFromError(err) != 400 {
			t.Fatalf("payload %q error = %v", payload, err)
		}
	}
}

func TestFallbackChainConfigReloadClearsExhaustion(t *testing.T) {
	f := newFallbackFixture(t, "codex", "claude")
	primary := f.register(t, 0, "a")
	f.register(t, 1, "a")
	f.executors["codex"].failures[primary] = &Error{HTTPStatus: 402}
	if err := f.invoke("execute", cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	delete(f.executors["codex"].failures, primary)
	f.manager.MarkResult(context.Background(), Result{AuthID: primary, Provider: "codex", Model: f.candidates[0].Model, Success: true})
	f.configure(true, "30m")
	f.calls = nil
	if err := f.invoke("execute", cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := f.ids(); !reflect.DeepEqual(got, []string{primary}) {
		t.Fatalf("reloaded calls = %v", got)
	}
}

func TestFallbackChainDoesNotLeakSubsetCooldown(t *testing.T) {
	f := newFallbackFixture(t, "codex", "claude")
	depleted := f.register(t, 0, "a")
	healthy := f.register(t, 0, "b")
	f.register(t, 1, "a")
	f.executors["codex"].failures[depleted] = &Error{HTTPStatus: 402}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExcludedAuthIDsMetadataKey: []string{healthy}}}
	if err := f.invoke("execute", opts); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := f.invoke("execute", cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := f.ids(); !reflect.DeepEqual(got, []string{healthy}) {
		t.Fatalf("subset exhaustion suppressed healthy primary: %v", got)
	}
}

func TestFallbackChainPreservesUnconfiguredCredentialCap(t *testing.T) {
	f := newFallbackFixture(t, "codex", "claude")
	first := f.register(t, 0, "a")
	f.register(t, 0, "b")
	f.manager.SetConfig(&internalconfig.Config{})
	f.executors["codex"].failures[first] = &Error{HTTPStatus: 402}
	_, err := f.manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: f.candidates[0].Model}, cliproxyexecutor.Options{})
	if err == nil || !reflect.DeepEqual(f.ids(), []string{first}) {
		t.Fatalf("unconfigured execution error = %v, calls = %v", err, f.ids())
	}
}

func TestFallbackChainSameProviderRebindsModelWithoutExclusionLeak(t *testing.T) {
	f := newFallbackFixture(t, "codex", "codex")
	id := f.register(t, 0, "a")
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: f.candidates[0].Model}, {ID: f.candidates[1].Model}})
	f.executors["codex"].inspect = func(req cliproxyexecutor.Request, _ cliproxyexecutor.Options) error {
		if req.Model == f.candidates[0].Model {
			return &Error{HTTPStatus: 402}
		}
		return nil
	}
	if err := f.invoke("execute", cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := f.ids(); !reflect.DeepEqual(got, []string{id, id}) || f.calls[1].model != f.candidates[1].Model {
		t.Fatalf("same-provider model fallback calls = %#v", f.calls)
	}
}

func TestFallbackChainCanceledRequestDoesNotAdvance(t *testing.T) {
	f := newFallbackFixture(t, "codex", "claude")
	primary := f.register(t, 0, "a")
	f.register(t, 1, "a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.executors["codex"].inspect = func(cliproxyexecutor.Request, cliproxyexecutor.Options) error {
		cancel()
		return &Error{HTTPStatus: 402}
	}
	_, err := f.manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "route-alias"}, cliproxyexecutor.Options{})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(f.ids(), []string{primary}) {
		t.Fatalf("canceled request error = %v, calls = %v", err, f.ids())
	}
}

func TestFallbackChainRespectsRequestScopedStop(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			f := newFallbackFixture(t, "codex", "claude")
			primary := f.register(t, 0, "a")
			f.register(t, 1, "a")
			auth, _ := f.manager.GetByID(primary)
			auth.Metadata = map[string]any{
				"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{Status: 402, Match: []string{"stop_on_payment"}, Action: "stop"}},
			}
			if _, err := f.manager.Update(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			f.executors["codex"].failures[primary] = &Error{HTTPStatus: 402, Message: "stop_on_payment"}
			err := f.invoke(mode, cliproxyexecutor.Options{})
			if statusCodeFromError(err) != 402 || !reflect.DeepEqual(f.ids(), []string{primary}) {
				t.Fatalf("stopped request error = %v, calls = %v", err, f.ids())
			}
		})
	}
}

func TestFallbackChainStaleCompletionCannotResetReloadedState(t *testing.T) {
	var conductor FallbackChainConductor
	first := &internalconfig.Config{}
	second := &internalconfig.Config{}
	conductor.start(first, "route", true)
	conductor.finish(first, "route", 0, time.Hour, false)
	conductor.start(second, "route", true)
	conductor.finish(second, "route", 0, time.Hour, false)
	conductor.finish(first, "route", 0, 0, true)
	if _, cooling := conductor.cooling(second, "route", 0, time.Now()); !cooling {
		t.Fatal("stale request cleared reloaded candidate cooldown")
	}
}
