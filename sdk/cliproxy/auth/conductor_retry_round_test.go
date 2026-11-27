package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type retryRoundCallExecutor struct {
	identifier string
	mu         sync.Mutex
	executeIDs []string
	streamIDs  []string
	countIDs   []string
	failure    func(authID, model string) error
	prepare    func(authID string) error
}

func (e *retryRoundCallExecutor) Identifier() string { return e.identifier }

func (e *retryRoundCallExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeIDs = append(e.executeIDs, auth.ID)
	e.mu.Unlock()
	if err := e.requestError(auth.ID, req.Model); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"status":"completed","output_text":"recovered"}`)}, nil
}

func (e *retryRoundCallExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamIDs = append(e.streamIDs, auth.ID)
	e.mu.Unlock()
	if err := e.requestError(auth.ID, req.Model); err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"status":"completed","output_text":"recovered"}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*retryRoundCallExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *retryRoundCallExecutor) CountTokens(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.countIDs = append(e.countIDs, auth.ID)
	e.mu.Unlock()
	if err := e.requestError(auth.ID, req.Model); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"total_tokens":42}`)}, nil
}

func (*retryRoundCallExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *retryRoundCallExecutor) requestError(authID, model string) error {
	if e.failure != nil {
		return e.failure(authID, model)
	}
	return &Error{HTTPStatus: http.StatusInternalServerError, Message: "retry-round test failure"}
}

func (e *retryRoundCallExecutor) ShouldPrepareRequestAuth(_ *Auth) bool {
	return e.prepare != nil
}

func (e *retryRoundCallExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	if e.prepare != nil {
		return auth, e.prepare(auth.ID)
	}
	return auth, nil
}

func (e *retryRoundCallExecutor) ids(kind string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var source []string
	switch kind {
	case "execute":
		source = e.executeIDs
	case "stream":
		source = e.streamIDs
	case "count":
		source = e.countIDs
	}
	return append([]string(nil), source...)
}

func registerRetryRoundLocalAuths(t *testing.T, manager *Manager, provider, model string, limits map[string]int) []string {
	t.Helper()
	ids := make([]string, 0, len(limits))
	for id := range limits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	reg := registry.GetGlobalRegistry()
	for _, id := range ids {
		reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:       id,
			Provider: provider,
			Metadata: map[string]any{"request_retry": limits[id], "disable_cooling": true},
		}); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}
	return ids
}

func countRetryRoundIDs(ids []string) map[string]int {
	counts := make(map[string]int, len(ids))
	for _, id := range ids {
		counts[id]++
	}
	return counts
}

func TestExecuteRetryRoundCredentialWindows(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(*Manager, cliproxyexecutor.Request) error
		kind   string
	}{
		{
			name: "non-stream",
			invoke: func(manager *Manager, req cliproxyexecutor.Request) error {
				_, errExecute := manager.Execute(context.Background(), []string{"retry-round-test"}, req, cliproxyexecutor.Options{})
				return errExecute
			},
			kind: "execute",
		},
		{
			name: "count-tokens",
			invoke: func(manager *Manager, req cliproxyexecutor.Request) error {
				_, errExecute := manager.ExecuteCount(context.Background(), []string{"retry-round-test"}, req, cliproxyexecutor.Options{})
				return errExecute
			},
			kind: "count",
		},
		{
			name: "stream",
			invoke: func(manager *Manager, req cliproxyexecutor.Request) error {
				_, errExecute := manager.ExecuteStream(context.Background(), []string{"retry-round-test"}, req, cliproxyexecutor.Options{Stream: true})
				return errExecute
			},
			kind: "stream",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetRetryConfig(3, 0, 0)
			executor := &retryRoundCallExecutor{identifier: "retry-round-test"}
			manager.RegisterExecutor(executor)
			registerRetryRoundLocalAuths(t, manager, "retry-round-test", "retry-round-model", map[string]int{
				"retry-round-a": 3,
				"retry-round-b": 2,
				"retry-round-c": 2,
			})

			if errExecute := test.invoke(manager, cliproxyexecutor.Request{Model: "retry-round-model"}); errExecute == nil {
				t.Fatal("execution error = nil, want terminal retry error")
			}
			counts := countRetryRoundIDs(executor.ids(test.kind))
			if counts["retry-round-a"] != 4 || counts["retry-round-b"] != 3 || counts["retry-round-c"] != 3 {
				t.Fatalf("credential call counts = %#v, want A=4 B=3 C=3; calls=%v", counts, executor.ids(test.kind))
			}
		})
	}
}

func TestExecuteRetryRoundMaxCredentialsAgesSkippedAuths(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(2, 0, 3)
	executor := &retryRoundCallExecutor{identifier: "retry-round-test"}
	manager.RegisterExecutor(executor)
	registerRetryRoundLocalAuths(t, manager, "retry-round-test", "retry-round-model-cap", map[string]int{
		"retry-cap-a": 1,
		"retry-cap-b": 1,
		"retry-cap-c": 1,
		"retry-cap-d": 2,
	})

	if _, errExecute := manager.Execute(context.Background(), []string{"retry-round-test"}, cliproxyexecutor.Request{Model: "retry-round-model-cap"}, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("execution error = nil, want terminal retry error")
	}
	calls := executor.ids("execute")
	if len(calls) != 7 {
		t.Fatalf("credential calls = %v, want three initial, three round-1, and one round-2 call", calls)
	}
	counts := countRetryRoundIDs(calls)
	if counts["retry-cap-a"] > 2 || counts["retry-cap-b"] > 2 || counts["retry-cap-c"] > 2 || counts["retry-cap-d"] > 3 {
		t.Fatalf("credential call counts exceed their round windows: %#v", counts)
	}
	if calls[len(calls)-1] != "retry-cap-d" {
		t.Fatalf("last retry call = %q, want retry-cap-d; calls=%v", calls[len(calls)-1], calls)
	}
}

func executeMixedRetryRoundPath(ctx context.Context, manager *Manager, mode, provider, model string) ([]byte, error) {
	req := cliproxyexecutor.Request{Model: model}
	switch mode {
	case "stream":
		result, err := manager.ExecuteStream(ctx, []string{provider}, req, cliproxyexecutor.Options{Stream: true})
		if err != nil {
			return nil, err
		}
		var payload []byte
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				return nil, chunk.Err
			}
			payload = append(payload, chunk.Payload...)
		}
		return payload, nil
	case "count":
		result, err := manager.ExecuteCount(ctx, []string{provider}, req, cliproxyexecutor.Options{})
		return result.Payload, err
	default:
		result, err := manager.Execute(ctx, []string{provider}, req, cliproxyexecutor.Options{})
		return result.Payload, err
	}
}

func TestExecuteRetryRoundMixedExclusions(t *testing.T) {
	transient := &Error{Code: "local_unavailable", HTTPStatus: http.StatusServiceUnavailable, Message: "local executor temporarily unavailable"}
	excluded := RejectAntigravityTarget(&Error{Code: "model_excluded", HTTPStatus: http.StatusServiceUnavailable, Message: "model excluded"})
	miss := errors.New("local credential preparation unavailable")
	cases := []struct {
		name           string
		failures       []error
		recover        bool
		wantErr        error
		wantCounts     []int
		maxCredentials int
		localOnly      bool
	}{
		{name: "retryable then excluded", failures: []error{transient, excluded}, recover: true, wantCounts: []int{2, 1}},
		{name: "excluded then retryable", failures: []error{excluded, transient}, recover: true, wantCounts: []int{2, 2}},
		{name: "transport then excluded", failures: []error{dialRefusedError(), excluded}, recover: true, wantCounts: []int{2, 1}, localOnly: true},
		{name: "retryable then miss then excluded", failures: []error{transient, miss, fmt.Errorf("policy: %w", excluded)}, recover: true, wantCounts: []int{2, 1, 1}, maxCredentials: 3},
		{name: "retry budget exhausted", failures: []error{transient, excluded}, wantErr: transient, wantCounts: []int{4, 4}, maxCredentials: 2},
		{name: "all excluded on exhaustion", failures: []error{excluded, excluded}, wantErr: excluded, wantCounts: []int{1, 1}},
		{name: "all excluded at credential cap", failures: []error{excluded, excluded}, wantErr: excluded, wantCounts: []int{1, 1}, maxCredentials: 2},
		{name: "same round recovery", failures: []error{excluded, nil}, wantCounts: []int{1, 1}},
	}
	for _, mode := range []string{"generate", "stream", "count"} {
		for _, homeEnabled := range []bool{false, true} {
			for _, phase := range []string{"execute", "prepare"} {
				for _, tc := range cases {
					if homeEnabled && tc.localOnly {
						continue
					}
					t.Run(fmt.Sprintf("%s/home=%t/%s/%s", mode, homeEnabled, phase, tc.name), func(t *testing.T) {
						const provider = "antigravity"
						model := "mixed-retry-" + t.Name()
						manager := NewManager(nil, &FillFirstSelector{}, nil)
						manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: homeEnabled}})
						manager.SetRetryConfig(3, 0, tc.maxCredentials)
						limits := make(map[string]int, len(tc.failures))
						failures := make(map[string]error, len(tc.failures))
						ids := make([]string, len(tc.failures))
						for i, failure := range tc.failures {
							id := fmt.Sprintf("%s/%02d", t.Name(), i)
							ids[i] = id
							limits[id] = 3
							failures[id] = failure
						}
						if homeEnabled {
							manager.PublishHomeDispatch(&retryRoundHomeDispatcher{limits: limits, provider: provider}, executionregistry.New(), 1)
						} else {
							registerRetryRoundLocalAuths(t, manager, provider, model, limits)
						}
						counts := make(map[string]int, len(ids))
						nextError := func(authID string) error {
							failure, ok := failures[authID]
							if !ok {
								t.Fatalf("unexpected credential %q", authID)
							}
							counts[authID]++
							if tc.recover && counts[authID] > 1 && isRequestRetryRoundError(failure) {
								return nil
							}
							return failure
						}
						executor := &retryRoundCallExecutor{identifier: provider}
						if phase == "prepare" {
							executor.prepare = nextError
							executor.failure = func(string, string) error { return nil }
						} else {
							executor.failure = func(authID, _ string) error { return nextError(authID) }
						}
						manager.RegisterExecutor(executor)
						payload, err := executeMixedRetryRoundPath(t.Context(), manager, mode, provider, model)
						if tc.wantErr != nil {
							if !errors.Is(err, tc.wantErr) {
								t.Fatalf("execution error = %v, want %v", err, tc.wantErr)
							}
						} else {
							if err != nil {
								t.Fatal(err)
							}
							want := `{"status":"completed","output_text":"recovered"}`
							if mode == "count" {
								want = `{"total_tokens":42}`
							}
							if string(payload) != want {
								t.Fatalf("response = %s, want %s", payload, want)
							}
						}
						for i, id := range ids {
							if got := counts[id]; got != tc.wantCounts[i] {
								t.Fatalf("credential %d attempts = %d, want %d; all attempts=%v", i, got, tc.wantCounts[i], counts)
							}
						}
					})
				}
			}
		}
	}
}

func TestExecuteRetryRoundMixedModelPoolExclusions(t *testing.T) {
	for _, mode := range []string{"generate", "stream", "count"} {
		t.Run(mode, func(t *testing.T) {
			const alias = "mixed-model-pool"
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name: "pool",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "transient", Alias: alias},
					{Name: "missing", Alias: alias},
					{Name: "excluded", Alias: alias},
				},
			}}})
			manager.SetRetryConfig(3, 0, 1)
			auth := &Auth{
				ID:         t.Name(),
				Provider:   openAICompatPoolProviderKey,
				Attributes: map[string]string{"api_key": "test-key", "compat_name": "pool", "provider_key": openAICompatPoolProviderKey},
				Metadata:   map[string]any{"request_retry": 3, "disable_cooling": true},
			}
			if _, err := manager.Register(t.Context(), auth); err != nil {
				t.Fatal(err)
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: alias}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			counts := make(map[string]int)
			executor := &retryRoundCallExecutor{
				identifier: auth.Provider,
				failure: func(_ string, model string) error {
					counts[model]++
					switch model {
					case "transient":
						if counts[model] == 1 {
							return &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "local model temporarily unavailable"}
						}
						return nil
					case "missing":
						return errors.New("local model unavailable")
					case "excluded":
						return RejectAntigravityTarget(&Error{Code: "model_excluded", HTTPStatus: http.StatusServiceUnavailable, Message: "model excluded"})
					default:
						t.Fatalf("unexpected upstream model %q", model)
						return nil
					}
				},
			}
			manager.RegisterExecutor(executor)
			payload, err := executeMixedRetryRoundPath(t.Context(), manager, mode, auth.Provider, alias)
			if err != nil {
				t.Fatal(err)
			}
			want := `{"status":"completed","output_text":"recovered"}`
			if mode == "count" {
				want = `{"total_tokens":42}`
			}
			if string(payload) != want || counts["transient"] != 2 || counts["missing"] == 0 || counts["excluded"] == 0 {
				t.Fatalf("response = %s, attempts=%v; want recovery after transient, local miss, and exclusion failures", payload, counts)
			}
		})
	}
}

type retryConfigMutationExecutor struct {
	identifier  string
	manager     *Manager
	nextDefault int

	mu    sync.Mutex
	calls int
}

func (e *retryConfigMutationExecutor) Identifier() string { return e.identifier }

func (e *retryConfigMutationExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.recordCall() == 1 {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "retry config changed"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *retryConfigMutationExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.recordCall() == 1 {
		return nil, &Error{HTTPStatus: http.StatusInternalServerError, Message: "retry config changed"}
	}
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*retryConfigMutationExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *retryConfigMutationExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.recordCall() == 1 {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "retry config changed"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*retryConfigMutationExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *retryConfigMutationExecutor) recordCall() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.calls == 1 {
		e.manager.SetRetryConfig(e.nextDefault, 0, 0)
	}
	return e.calls
}

func (e *retryConfigMutationExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestExecuteSnapshotsDefaultRequestRetry(t *testing.T) {
	paths := []struct {
		name   string
		invoke func(*Manager) error
	}{
		{
			name: "non-stream",
			invoke: func(manager *Manager) error {
				_, errExecute := manager.Execute(context.Background(), []string{"retry-config-snapshot"}, cliproxyexecutor.Request{Model: "retry-config-snapshot-model"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count-tokens",
			invoke: func(manager *Manager) error {
				_, errExecute := manager.ExecuteCount(context.Background(), []string{"retry-config-snapshot"}, cliproxyexecutor.Request{Model: "retry-config-snapshot-model"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "stream",
			invoke: func(manager *Manager) error {
				_, errExecute := manager.ExecuteStream(context.Background(), []string{"retry-config-snapshot"}, cliproxyexecutor.Request{Model: "retry-config-snapshot-model"}, cliproxyexecutor.Options{Stream: true})
				return errExecute
			},
		},
	}
	scenarios := []struct {
		name        string
		initial     int
		next        int
		wantCalls   int
		wantSuccess bool
	}{
		{name: "decrease after request starts", initial: 1, next: 0, wantCalls: 2, wantSuccess: true},
		{name: "increase after request starts", initial: 0, next: 1, wantCalls: 1, wantSuccess: false},
	}

	for _, path := range paths {
		for _, scenario := range scenarios {
			t.Run(path.name+"/"+scenario.name, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				manager.SetRetryConfig(scenario.initial, 0, 0)
				executor := &retryConfigMutationExecutor{
					identifier:  "retry-config-snapshot",
					manager:     manager,
					nextDefault: scenario.next,
				}
				manager.RegisterExecutor(executor)

				const authID = "retry-config-snapshot-auth"
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(authID, "retry-config-snapshot", []*registry.ModelInfo{{ID: "retry-config-snapshot-model"}})
				t.Cleanup(func() { reg.UnregisterClient(authID) })
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       authID,
					Provider: "retry-config-snapshot",
					Metadata: map[string]any{"disable_cooling": true},
				}); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}

				errExecute := path.invoke(manager)
				if scenario.wantSuccess && errExecute != nil {
					t.Fatalf("execution error = %v, want success", errExecute)
				}
				if !scenario.wantSuccess && statusCodeFromError(errExecute) != http.StatusInternalServerError {
					t.Fatalf("execution error = %v, want HTTP 500", errExecute)
				}
				if calls := executor.callCount(); calls != scenario.wantCalls {
					t.Fatalf("executor calls = %d, want %d", calls, scenario.wantCalls)
				}
			})
		}
	}
}

type retryRoundHomeDispatcher struct {
	mu       sync.Mutex
	provider string
	limits   map[string]int
	rounds   []int
	newCalls int
	oldCalls int
}

func (*retryRoundHomeDispatcher) HeartbeatOK() bool { return true }

func (d *retryRoundHomeDispatcher) RPopAuth(ctx context.Context, model, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithRetryRoundConstraints(ctx, model, sessionID, headers, count, 0, nil, "")
}

func (d *retryRoundHomeDispatcher) RPopAuthWithConstraints(ctx context.Context, model, sessionID string, headers http.Header, count int, excluded []string, pinned string) ([]byte, error) {
	d.mu.Lock()
	d.oldCalls++
	d.mu.Unlock()
	return d.RPopAuthWithRetryRoundConstraints(ctx, model, sessionID, headers, count, 0, excluded, pinned)
}

func (d *retryRoundHomeDispatcher) RPopAuthWithRetryRoundConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, retryRound int, excluded []string, pinned string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.newCalls++
	d.rounds = append(d.rounds, retryRound)
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, id := range excluded {
		excludedSet[id] = struct{}{}
	}
	ids := make([]string, 0, len(d.limits))
	maxRetry := 0
	for id, limit := range d.limits {
		if limit >= retryRound {
			ids = append(ids, id)
			if limit > maxRetry {
				maxRetry = limit
			}
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, okExcluded := excludedSet[id]; okExcluded || (pinned != "" && pinned != id) {
			continue
		}
		provider := d.provider
		if provider == "" {
			provider = "retry-round-home"
		}
		return json.Marshal(homeAuthDispatchResponse{
			RequestRetry: func() *int { value := maxRetry; return &value }(),
			Auth:         Auth{ID: id, Provider: provider, Status: StatusActive, Metadata: map[string]any{"request_retry": d.limits[id]}},
		})
	}
	return nil, home.ErrAuthNotFound
}

func (*retryRoundHomeDispatcher) AbortAmbiguousDispatch() {}

func (d *retryRoundHomeDispatcher) roundsSeen() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int(nil), d.rounds...)
}

func (d *retryRoundHomeDispatcher) dispatchMethodCalls() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.newCalls, d.oldCalls
}

func TestExecuteHomeRetryRoundCredentialWindows(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(*Manager, cliproxyexecutor.Request) error
	}{
		{
			name: "non-stream",
			invoke: func(manager *Manager, req cliproxyexecutor.Request) error {
				_, errExecute := manager.Execute(context.Background(), []string{"retry-round-home"}, req, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count-tokens",
			invoke: func(manager *Manager, req cliproxyexecutor.Request) error {
				_, errExecute := manager.ExecuteCount(context.Background(), []string{"retry-round-home"}, req, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "stream",
			invoke: func(manager *Manager, req cliproxyexecutor.Request) error {
				_, errExecute := manager.ExecuteStream(context.Background(), []string{"retry-round-home"}, req, cliproxyexecutor.Options{Stream: true})
				return errExecute
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(3, 0, 3)
			dispatcher := &retryRoundHomeDispatcher{limits: map[string]int{
				"retry-round-a": 3,
				"retry-round-b": 2,
				"retry-round-c": 2,
			}}
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			executor := &retryRoundCallExecutor{identifier: "retry-round-home"}
			manager.RegisterExecutor(executor)

			if errExecute := test.invoke(manager, cliproxyexecutor.Request{Model: "retry-round-model"}); errExecute == nil {
				t.Fatal("execution error = nil, want terminal retry error")
			}
			counts := countRetryRoundIDs(executor.ids(map[string]string{"non-stream": "execute", "count-tokens": "count", "stream": "stream"}[test.name]))
			if counts["retry-round-a"] != 4 || counts["retry-round-b"] != 3 || counts["retry-round-c"] != 3 {
				t.Fatalf("credential call counts = %#v, want A=4 B=3 C=3", counts)
			}
			rounds := dispatcher.roundsSeen()
			if len(rounds) == 0 || rounds[0] != 0 {
				t.Fatalf("Home retry rounds = %v, want initial round 0", rounds)
			}
			foundRoundOne := false
			foundRoundTwo := false
			foundRoundThree := false
			for _, round := range rounds {
				foundRoundOne = foundRoundOne || round == 1
				foundRoundTwo = foundRoundTwo || round == 2
				foundRoundThree = foundRoundThree || round == 3
			}
			if !foundRoundOne || !foundRoundTwo || !foundRoundThree {
				t.Fatalf("Home retry rounds = %v, want rounds 0,1,2,3", rounds)
			}
			newCalls, oldCalls := dispatcher.dispatchMethodCalls()
			if newCalls == 0 || oldCalls != 0 {
				t.Fatalf("Home dispatcher method calls = new %d, old %d; want new interface only", newCalls, oldCalls)
			}
		})
	}
}
