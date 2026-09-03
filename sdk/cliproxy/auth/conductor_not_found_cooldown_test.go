package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestMarkResultNotFoundCooldownPolicy(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	previousDisabled := quotaCooldownDisabled.Load()
	SetTransientErrorCooldownSeconds(0)
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() {
		transientErrorCooldownSeconds.Store(previousTransient)
		quotaCooldownDisabled.Store(previousDisabled)
	})

	tests := []struct {
		name            string
		err             *Error
		disableCooling  bool
		initialBackoff  int
		wantMinCooldown time.Duration
		wantMaxCooldown time.Duration
		wantBackoff     int
	}{
		{
			name:            "generic 404 starts short retry",
			err:             &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"},
			wantMinCooldown: 59 * time.Second,
			wantMaxCooldown: 61 * time.Second,
			wantBackoff:     1,
		},
		{
			name:            "repeated generic 404 escalates",
			err:             &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"},
			initialBackoff:  1,
			wantMinCooldown: 119 * time.Second,
			wantMaxCooldown: 121 * time.Second,
			wantBackoff:     2,
		},
		{
			name:            "explicit model not found stays long",
			err:             &Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound, Message: "model unavailable"},
			wantMinCooldown: 12*time.Hour - time.Second,
			wantMaxCooldown: 12*time.Hour + time.Second,
		},
		{
			name:           "disable cooling leaves retry unset",
			err:            &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"},
			disableCooling: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			quotaCooldownDisabled.Store(tc.disableCooling)
			manager := NewManager(nil, nil, nil)
			auth := &Auth{
				ID:       "model-not-found-policy",
				Provider: "codex",
				ModelStates: map[string]*ModelState{
					"gpt-5": {Quota: QuotaState{BackoffLevel: tc.initialBackoff, NextRecoverAt: time.Now().Add(-time.Second)}},
				},
			}
			if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			before := time.Now()
			manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Error: tc.err})
			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated.ModelStates["gpt-5"] == nil {
				t.Fatal("MarkResult() did not retain model cooldown state")
			}
			state := updated.ModelStates["gpt-5"]
			if tc.wantMinCooldown == 0 {
				if !state.NextRetryAfter.IsZero() {
					t.Fatalf("NextRetryAfter = %v, want zero", state.NextRetryAfter)
				}
				return
			}
			cooldown := state.NextRetryAfter.Sub(before)
			if cooldown < tc.wantMinCooldown || cooldown > tc.wantMaxCooldown {
				t.Fatalf("cooldown = %v, want within [%v, %v]", cooldown, tc.wantMinCooldown, tc.wantMaxCooldown)
			}
			if state.Quota.BackoffLevel != tc.wantBackoff {
				t.Fatalf("BackoffLevel = %d, want %d", state.Quota.BackoffLevel, tc.wantBackoff)
			}
			if tc.initialBackoff > 0 {
				manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Success: true})
				reset, _ := manager.GetByID(auth.ID)
				if got := reset.ModelStates["gpt-5"].Quota.BackoffLevel; got != 0 {
					t.Fatalf("successful result left BackoffLevel = %d, want 0", got)
				}
			}
		})
	}
}

func TestMarkResultUnsupportedModelSurvivesRacingGeneric404(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	previousDisabled := quotaCooldownDisabled.Load()
	SetTransientErrorCooldownSeconds(0)
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() {
		transientErrorCooldownSeconds.Store(previousTransient)
		quotaCooldownDisabled.Store(previousDisabled)
	})

	unsupported := &Error{HTTPStatus: http.StatusBadRequest, Message: "requested model is not supported"}
	generic404 := &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"}

	newAuth := func(id string) (*Manager, *Auth) {
		manager := NewManager(nil, nil, nil)
		auth := &Auth{ID: id, Provider: "codex", ModelStates: map[string]*ModelState{
			"gpt-5": {},
		}}
		if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		return manager, auth
	}
	cooldownFor := func(manager *Manager, authID string, before time.Time) time.Duration {
		updated, ok := manager.GetByID(authID)
		if !ok || updated.ModelStates["gpt-5"] == nil {
			t.Fatal("MarkResult() did not retain model cooldown state")
		}
		return updated.ModelStates["gpt-5"].NextRetryAfter.Sub(before)
	}

	// unsupported-model 400 first, then a racing generic 404 for the same key
	// must not shorten the 12h deadline.
	managerA, authA := newAuth("unsupported-then-generic-404")
	before := time.Now()
	managerA.MarkResult(context.Background(), Result{AuthID: authA.ID, Provider: "codex", Model: "gpt-5", Error: unsupported})
	cooldown := cooldownFor(managerA, authA.ID, before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("unsupported-model cooldown = %v, want about 12h", cooldown)
	}
	managerA.MarkResult(context.Background(), Result{AuthID: authA.ID, Provider: "codex", Model: "gpt-5", Error: generic404})
	cooldown = cooldownFor(managerA, authA.ID, before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("racing generic 404 shortened unsupported-model deadline: cooldown = %v, want unchanged ~12h", cooldown)
	}

	// generic 404 first, then an unsupported-model 400 for the same key must
	// land on the 12h deadline.
	managerB, authB := newAuth("generic-404-then-unsupported")
	before = time.Now()
	managerB.MarkResult(context.Background(), Result{AuthID: authB.ID, Provider: "codex", Model: "gpt-5", Error: generic404})
	cooldown = cooldownFor(managerB, authB.ID, before)
	if cooldown < 59*time.Second || cooldown > 61*time.Second {
		t.Fatalf("generic-404-first cooldown = %v, want about 1m", cooldown)
	}
	managerB.MarkResult(context.Background(), Result{AuthID: authB.ID, Provider: "codex", Model: "gpt-5", Error: unsupported})
	cooldown = cooldownFor(managerB, authB.ID, before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("generic-then-unsupported cooldown = %v, want about 12h", cooldown)
	}
}

func TestApplyAuthFailureStateNotFoundCooldownPolicy(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	now := time.Date(2026, 9, 3, 14, 42, 0, 0, time.UTC)
	generic := &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"}
	auth := &Auth{ID: "auth-not-found-policy"}
	applyAuthFailureStateForModel(auth, generic, nil, "", now, false)
	if want := now.Add(time.Minute); !auth.NextRetryAfter.Equal(want) {
		t.Fatalf("first generic 404 NextRetryAfter = %v, want %v", auth.NextRetryAfter, want)
	}
	if auth.Quota.BackoffLevel != 1 {
		t.Fatalf("first generic 404 BackoffLevel = %d, want 1", auth.Quota.BackoffLevel)
	}

	auth.Quota.NextRecoverAt = now.Add(-time.Second)
	applyAuthFailureStateForModel(auth, generic, nil, "", now, false)
	if want := now.Add(2 * time.Minute); !auth.NextRetryAfter.Equal(want) {
		t.Fatalf("repeated generic 404 NextRetryAfter = %v, want %v", auth.NextRetryAfter, want)
	}
	if auth.Quota.BackoffLevel != 2 {
		t.Fatalf("repeated generic 404 BackoffLevel = %d, want 2", auth.Quota.BackoffLevel)
	}
	clearAuthStateOnSuccess(auth, now)
	if auth.Quota.BackoffLevel != 0 || !auth.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("success left auth retry state = %#v, want reset", auth.Quota)
	}

	explicit := &Auth{ID: "auth-explicit-model-not-found"}
	applyAuthFailureStateForModel(explicit, &Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound}, nil, "", now, false)
	if want := now.Add(12 * time.Hour); !explicit.NextRetryAfter.Equal(want) {
		t.Fatalf("explicit model-not-found NextRetryAfter = %v, want %v", explicit.NextRetryAfter, want)
	}

	disabled := &Auth{ID: "auth-not-found-disabled"}
	applyAuthFailureStateForModel(disabled, generic, nil, "", now, true)
	if !disabled.NextRetryAfter.IsZero() {
		t.Fatalf("disabled cooling NextRetryAfter = %v, want zero", disabled.NextRetryAfter)
	}
}

func TestApplyAuthFailureStateExplicitNotFoundSurvivesRacingGeneric404(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	now := time.Date(2026, 9, 3, 14, 42, 0, 0, time.UTC)
	generic := &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"}
	explicit := &Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound, Message: "model unavailable"}

	// explicit-404 then a racing generic-404 for the same key must not shorten the 12h deadline.
	authExplicitFirst := &Auth{ID: "auth-explicit-then-generic"}
	applyAuthFailureStateForModel(authExplicitFirst, explicit, nil, "", now, false)
	want := now.Add(12 * time.Hour)
	if !authExplicitFirst.NextRetryAfter.Equal(want) {
		t.Fatalf("explicit-first NextRetryAfter = %v, want %v", authExplicitFirst.NextRetryAfter, want)
	}
	applyAuthFailureStateForModel(authExplicitFirst, generic, nil, "", now, false)
	if !authExplicitFirst.NextRetryAfter.Equal(want) {
		t.Fatalf("racing generic 404 shortened deadline: NextRetryAfter = %v, want unchanged %v", authExplicitFirst.NextRetryAfter, want)
	}

	// generic-404 then explicit-404 for the same key must land on the 12h deadline.
	authGenericFirst := &Auth{ID: "auth-generic-then-explicit"}
	applyAuthFailureStateForModel(authGenericFirst, generic, nil, "", now, false)
	if got := authGenericFirst.NextRetryAfter; !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("generic-first NextRetryAfter = %v, want %v", got, now.Add(time.Minute))
	}
	applyAuthFailureStateForModel(authGenericFirst, explicit, nil, "", now, false)
	if !authGenericFirst.NextRetryAfter.Equal(want) {
		t.Fatalf("generic-then-explicit NextRetryAfter = %v, want %v", authGenericFirst.NextRetryAfter, want)
	}
}

func TestApplyAuthFailureStateExplicitNotFoundNeverShortensLongerCooldown(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 42, 0, 0, time.UTC)
	explicit := &Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound, Message: "model unavailable"}
	rateLimited := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
	longRetryAfter := 24 * time.Hour
	shortRetryAfter := 5 * time.Minute

	// A long 429 window (e.g. a provider retry window beyond 12h) must survive
	// a later explicit-404 for the same key: the 404 must not shorten it.
	authLong429First := &Auth{ID: "auth-long-429-then-explicit"}
	applyAuthFailureStateForModel(authLong429First, rateLimited, &longRetryAfter, "", now, false)
	want429 := now.Add(longRetryAfter)
	if !authLong429First.NextRetryAfter.Equal(want429) {
		t.Fatalf("long-429 NextRetryAfter = %v, want %v", authLong429First.NextRetryAfter, want429)
	}
	applyAuthFailureStateForModel(authLong429First, explicit, nil, "", now, false)
	if !authLong429First.NextRetryAfter.Equal(want429) {
		t.Fatalf("explicit 404 shortened a longer 429 deadline: NextRetryAfter = %v, want unchanged %v", authLong429First.NextRetryAfter, want429)
	}

	// An explicit-404 recorded first must survive a later long 429: the 429
	// must not lose to a stale short window, and here it is itself longer, so
	// it applies (the max, not the 404, wins when the 429 is genuinely longer).
	authExplicitThenLong429 := &Auth{ID: "auth-explicit-then-long-429"}
	applyAuthFailureStateForModel(authExplicitThenLong429, explicit, nil, "", now, false)
	want12h := now.Add(12 * time.Hour)
	if !authExplicitThenLong429.NextRetryAfter.Equal(want12h) {
		t.Fatalf("explicit-first NextRetryAfter = %v, want %v", authExplicitThenLong429.NextRetryAfter, want12h)
	}
	applyAuthFailureStateForModel(authExplicitThenLong429, rateLimited, &longRetryAfter, "", now, false)
	if !authExplicitThenLong429.NextRetryAfter.Equal(want429) {
		t.Fatalf("explicit-then-long-429 NextRetryAfter = %v, want %v", authExplicitThenLong429.NextRetryAfter, want429)
	}

	// An explicit-404 recorded first must survive a later SHORT 429 for the
	// same key: the shorter 429 window must not override the 12h deadline.
	authExplicitThenShort429 := &Auth{ID: "auth-explicit-then-short-429"}
	applyAuthFailureStateForModel(authExplicitThenShort429, explicit, nil, "", now, false)
	if !authExplicitThenShort429.NextRetryAfter.Equal(want12h) {
		t.Fatalf("explicit-first NextRetryAfter = %v, want %v", authExplicitThenShort429.NextRetryAfter, want12h)
	}
	applyAuthFailureStateForModel(authExplicitThenShort429, rateLimited, &shortRetryAfter, "", now, false)
	if !authExplicitThenShort429.NextRetryAfter.Equal(want12h) {
		t.Fatalf("short 429 shortened the explicit 12h deadline: NextRetryAfter = %v, want unchanged %v", authExplicitThenShort429.NextRetryAfter, want12h)
	}
}

// notFoundExecutionModelExecutor returns an explicit model-not-found error that
// names the model actually present on the request it receives, so the test can
// tell whether the caller classified the failure against the execution model
// (what was sent) or the selection model (what routing picked).
type notFoundExecutionModelExecutor struct{}

func (notFoundExecutionModelExecutor) Identifier() string { return "selection-vs-execution" }
func (notFoundExecutionModelExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"error":{"type":"not_found_error","message":"model ` + req.Model + ` was not found"}}`,
	}
}
func (notFoundExecutionModelExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (notFoundExecutionModelExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (notFoundExecutionModelExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (notFoundExecutionModelExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestExecuteExplicitNotFoundClassifiesAgainstExecutionModelNotSelectionModel(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(notFoundExecutionModelExecutor{})
	auth := &Auth{ID: "selection-vs-execution-auth", Provider: "selection-vs-execution"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "selection-model"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AuthSelectionModelMetadataKey: "selection-model",
	}}
	before := time.Now()
	if _, errExecute := manager.Execute(context.Background(), []string{"selection-vs-execution"}, cliproxyexecutor.Request{Model: "execution-model"}, opts); errExecute == nil {
		t.Fatal("Execute() unexpectedly succeeded")
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found after Execute()")
	}
	var state *ModelState
	for _, candidate := range updated.ModelStates {
		state = candidate
	}
	if state == nil {
		t.Fatal("Execute() did not record model cooldown state")
	}
	cooldown := state.NextRetryAfter.Sub(before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("cooldown = %v, want about 12h (execution-model 404 must not be classified against the selection model and get the short retry)", cooldown)
	}
}

// notFoundWireModelExecutor simulates an executor that normalizes the
// request model internally (e.g. stripping a thinking suffix, remapping an
// alias) before sending it upstream: it reports the normalized model via
// Response.Metadata[WireModelMetadataKey], and its 404 names that normalized
// model, not the pre-normalization req.Model the conductor sent it.
type notFoundWireModelExecutor struct {
	normalize func(model string) string
}

func (e notFoundWireModelExecutor) Identifier() string { return "wire-model-vs-sent" }
func (e notFoundWireModelExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	wire := e.normalize(req.Model)
	resp := cliproxyexecutor.Response{Metadata: map[string]any{cliproxyexecutor.WireModelMetadataKey: wire}}
	return resp, &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"error":{"type":"not_found_error","message":"model ` + wire + ` was not found"}}`,
	}
}
func (e notFoundWireModelExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.normalize == nil {
		return nil, nil
	}
	// Mirrors ExecuteStream's real shape: no Response.Metadata channel exists
	// on failure, so the wire model must ride on the error itself (see
	// internal/runtime/executor's wireModelErr/withWireModel).
	wire := e.normalize(req.Model)
	return nil, wireModelStreamError{
		err: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    `{"error":{"type":"not_found_error","message":"model ` + wire + ` was not found"}}`,
		},
		model: wire,
	}
}

// wireModelStreamError simulates internal/runtime/executor's wireModelErr:
// an error decorated with the wire model, exposed via WireModel(), that
// still unwraps to the underlying classification error.
type wireModelStreamError struct {
	err   *Error
	model string
}

func (e wireModelStreamError) Error() string     { return e.err.Error() }
func (e wireModelStreamError) WireModel() string { return e.model }
func (e wireModelStreamError) Unwrap() error     { return e.err }
func (notFoundWireModelExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (notFoundWireModelExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (notFoundWireModelExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestExecuteExplicitNotFoundClassifiesAgainstReportedWireModel(t *testing.T) {
	cases := []struct {
		name      string
		reqModel  string
		normalize func(string) string
	}{
		{
			// Claude strips a "-thinking-32k" style suffix before sending the
			// model upstream; the provider's 404 names the stripped model.
			name:      "claude thinking suffix stripped",
			reqModel:  "claude-opus-4-8-thinking-32k",
			normalize: func(m string) string { return strings.TrimSuffix(m, "-thinking-32k") },
		},
		{
			// Kimi strips the "kimi-" prefix before sending the model
			// upstream; the provider's 404 names the stripped model.
			name:      "kimi alias stripped",
			reqModel:  "kimi-k3",
			normalize: func(m string) string { return strings.TrimPrefix(m, "kimi-") },
		},
		{
			// A config payload-override rule rewrites the outbound body's
			// top-level model field long after the pre-call normalization
			// that computed the pre-override model (mirrors
			// ApplyPayloadConfigWithRequestTracked/finalWireModel in
			// internal/runtime/executor): the provider's 404 names the
			// override target, not the client-requested model.
			name:      "payload override rewrites model",
			reqModel:  "claude-opus-4-8",
			normalize: func(string) string { return "payload-override-target-model" },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previousDisabled := quotaCooldownDisabled.Load()
			quotaCooldownDisabled.Store(false)
			t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })

			manager := NewManager(nil, nil, nil)
			executor := notFoundWireModelExecutor{normalize: tc.normalize}
			manager.RegisterExecutor(executor)
			auth := &Auth{ID: "wire-model-auth-" + tc.name, Provider: "wire-model-vs-sent"}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: tc.reqModel}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatalf("Register() error = %v", err)
			}

			before := time.Now()
			if _, errExecute := manager.Execute(context.Background(), []string{"wire-model-vs-sent"}, cliproxyexecutor.Request{Model: tc.reqModel}, cliproxyexecutor.Options{}); errExecute == nil {
				t.Fatal("Execute() unexpectedly succeeded")
			}

			updated, ok := manager.GetByID(auth.ID)
			if !ok {
				t.Fatal("auth not found after Execute()")
			}
			var state *ModelState
			for _, candidate := range updated.ModelStates {
				state = candidate
			}
			if state == nil {
				t.Fatal("Execute() did not record model cooldown state")
			}
			cooldown := state.NextRetryAfter.Sub(before)
			if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
				t.Fatalf("cooldown = %v, want about 12h (404 naming the reported wire model must classify as explicit not-found, not the short retry)", cooldown)
			}
		})
	}
}

func TestExecuteStreamExplicitNotFoundClassifiesAgainstReportedWireModel(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })

	manager := NewManager(nil, nil, nil)
	reqModel := "claude-opus-4-8-thinking-32k"
	normalize := func(m string) string { return strings.TrimSuffix(m, "-thinking-32k") }
	executor := notFoundWireModelExecutor{normalize: normalize}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "wire-model-stream-auth", Provider: "wire-model-vs-sent"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: reqModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	before := time.Now()
	if _, errExecute := manager.ExecuteStream(context.Background(), []string{"wire-model-vs-sent"}, cliproxyexecutor.Request{Model: reqModel}, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("ExecuteStream() unexpectedly succeeded")
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found after ExecuteStream()")
	}
	var state *ModelState
	for _, candidate := range updated.ModelStates {
		state = candidate
	}
	if state == nil {
		t.Fatal("ExecuteStream() did not record model cooldown state")
	}
	cooldown := state.NextRetryAfter.Sub(before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("cooldown = %v, want about 12h (stream 404 naming the reported wire model must classify as explicit not-found, not the short retry)", cooldown)
	}
}

func TestMarkResultAliasUsesAttemptedUpstreamModelForExplicitNotFound(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "alias-explicit-model-not-found", Provider: "openai-compatibility"}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:        auth.ID,
		Provider:      auth.Provider,
		Model:         "public-alias",
		UpstreamModel: "provider-model",
		RouteModel:    "public-alias",
		Error: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    `{"error":{"type":"not_found_error","message":"model provider-model was not found"}}`,
		},
	})
	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated.ModelStates["public-alias"] == nil {
		t.Fatal("MarkResult() did not retain alias-keyed model state")
	}
	cooldown := updated.ModelStates["public-alias"].NextRetryAfter.Sub(before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("alias explicit model-not-found cooldown = %v, want about 12h", cooldown)
	}
}

// notFoundCountTokensWireModelExecutor simulates Kimi's countTokensUpstream
// path: it normalizes the request model (e.g. "kimi-k3" -> "k3") before
// sending it upstream, and its 404 names the normalized model. CountTokens
// returns a zero Response on every error path (see
// internal/runtime/executor/claude_executor_tokens.go), so the wire model
// must ride on the error, mirroring ExecuteStream's wireModelErr.
type notFoundCountTokensWireModelExecutor struct {
	normalize func(model string) string
}

func (e notFoundCountTokensWireModelExecutor) Identifier() string { return "count-tokens-wire-model" }
func (notFoundCountTokensWireModelExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (notFoundCountTokensWireModelExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (notFoundCountTokensWireModelExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e notFoundCountTokensWireModelExecutor) CountTokens(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	wire := e.normalize(req.Model)
	return cliproxyexecutor.Response{}, wireModelStreamError{
		err: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    `{"error":{"type":"not_found_error","message":"model ` + wire + ` was not found"}}`,
		},
		model: wire,
	}
}
func (notFoundCountTokensWireModelExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestExecuteCountExplicitNotFoundClassifiesAgainstReportedWireModelNotEndpointNeutral(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })

	manager := NewManager(nil, nil, nil)
	reqModel := "kimi-k3"
	normalize := func(m string) string { return strings.TrimPrefix(m, "kimi-") }
	executor := notFoundCountTokensWireModelExecutor{normalize: normalize}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "count-tokens-wire-model-auth", Provider: "count-tokens-wire-model"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: reqModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	before := time.Now()
	if _, errCount := manager.ExecuteCount(context.Background(), []string{"count-tokens-wire-model"}, cliproxyexecutor.Request{Model: reqModel}, cliproxyexecutor.Options{}); errCount == nil {
		t.Fatal("ExecuteCount() unexpectedly succeeded")
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found after ExecuteCount()")
	}
	var state *ModelState
	for _, candidate := range updated.ModelStates {
		state = candidate
	}
	if state == nil {
		t.Fatal("ExecuteCount() did not record model cooldown state")
	}
	cooldown := state.NextRetryAfter.Sub(before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("cooldown = %v, want about 12h (a count_tokens 404 naming the reported wire model must classify as explicit not-found, not availability-neutral endpoint-unsupported)", cooldown)
	}
}

func TestApplyAuthFailureStateUsesAttemptedUpstreamModelForExplicitNotFound(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 42, 0, 0, time.UTC)
	auth := &Auth{ID: "auth-alias-explicit-model-not-found"}
	err := &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"error":{"type":"not_found_error","message":"model provider-model was not found"}}`,
	}
	applyAuthFailureStateForModel(auth, err, nil, "provider-model", now, false)
	if want := now.Add(12 * time.Hour); !auth.NextRetryAfter.Equal(want) {
		t.Fatalf("alias explicit model-not-found NextRetryAfter = %v, want %v", auth.NextRetryAfter, want)
	}
}

func TestMarkResultModelNotFoundSurvivesRacingCloudflareChallenge(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	previousDisabled := quotaCooldownDisabled.Load()
	SetTransientErrorCooldownSeconds(0)
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() {
		transientErrorCooldownSeconds.Store(previousTransient)
		quotaCooldownDisabled.Store(previousDisabled)
	})

	explicitNotFound := &Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound, Message: "model gpt-5 not found"}
	challenge := &Error{HTTPStatus: http.StatusForbidden, Message: "cloudflare challenge-platform detected"}

	newAuth := func(id string) (*Manager, *Auth) {
		manager := NewManager(nil, nil, nil)
		auth := &Auth{ID: id, Provider: "codex", ModelStates: map[string]*ModelState{
			"gpt-5": {},
		}}
		if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		return manager, auth
	}
	cooldownFor := func(manager *Manager, authID string, before time.Time) time.Duration {
		updated, ok := manager.GetByID(authID)
		if !ok || updated.ModelStates["gpt-5"] == nil {
			t.Fatal("MarkResult() did not retain model cooldown state")
		}
		return updated.ModelStates["gpt-5"].NextRetryAfter.Sub(before)
	}

	// explicit not-found first (12h), then a racing Cloudflare challenge for
	// the same model key must not shorten the stored deadline.
	manager, auth := newAuth("not-found-then-challenge")
	before := time.Now()
	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Error: explicitNotFound})
	cooldown := cooldownFor(manager, auth.ID, before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("explicit not-found cooldown = %v, want about 12h", cooldown)
	}
	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Error: challenge})
	cooldown = cooldownFor(manager, auth.ID, before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("racing cloudflare challenge shortened explicit not-found deadline: cooldown = %v, want unchanged ~12h", cooldown)
	}
}

func TestApplyAuthFailureStateModelNotFoundSurvivesRacingCloudflareChallenge(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	now := time.Date(2026, 9, 3, 17, 30, 0, 0, time.UTC)
	explicitNotFound := &Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound, Message: "model gpt-5 not found"}
	challenge := &Error{HTTPStatus: http.StatusForbidden, Message: "cloudflare challenge-platform detected"}

	auth := &Auth{ID: "auth-not-found-then-challenge"}
	applyAuthFailureStateForModel(auth, explicitNotFound, nil, "gpt-5", now, false)
	if want := now.Add(12 * time.Hour); !auth.NextRetryAfter.Equal(want) {
		t.Fatalf("explicit not-found NextRetryAfter = %v, want %v", auth.NextRetryAfter, want)
	}

	applyAuthFailureStateForModel(auth, challenge, nil, "gpt-5", now.Add(time.Minute), false)
	if want := now.Add(12 * time.Hour); !auth.NextRetryAfter.Equal(want) {
		t.Fatalf("racing cloudflare challenge shortened explicit not-found deadline: NextRetryAfter = %v, want unchanged %v", auth.NextRetryAfter, want)
	}
}

// notFoundCountTokensResponseMetadataExecutor simulates a custom CountTokens
// executor that follows the public Response.Metadata contract
// (cliproxyexecutor.WireModelMetadataKey) instead of the error-carried
// wireModelStreamError channel used by the Claude/Kimi executors: it returns
// a non-zero Response naming the normalized model alongside a plain error
// with no WireModel() method. If the classification path only reads
// wireModelFromError, it falls back to the pre-call sent model here and
// misclassifies an explicit not-found as an endpoint-neutral 404.
type notFoundCountTokensResponseMetadataExecutor struct {
	normalize func(model string) string
}

func (e notFoundCountTokensResponseMetadataExecutor) Identifier() string {
	return "count-tokens-response-metadata"
}
func (notFoundCountTokensResponseMetadataExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (notFoundCountTokensResponseMetadataExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (notFoundCountTokensResponseMetadataExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e notFoundCountTokensResponseMetadataExecutor) CountTokens(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	wire := e.normalize(req.Model)
	resp := cliproxyexecutor.Response{Metadata: map[string]any{cliproxyexecutor.WireModelMetadataKey: wire}}
	err := &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"error":{"type":"not_found_error","message":"model ` + wire + ` was not found"}}`,
	}
	return resp, err
}
func (notFoundCountTokensResponseMetadataExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestExecuteCountReadsWireModelFromResponseMetadataNotJustError(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })

	manager := NewManager(nil, nil, nil)
	reqModel := "kimi-k3"
	normalize := func(m string) string { return strings.TrimPrefix(m, "kimi-") }
	executor := notFoundCountTokensResponseMetadataExecutor{normalize: normalize}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "count-tokens-response-metadata-auth", Provider: "count-tokens-response-metadata"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: reqModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	before := time.Now()
	if _, errCount := manager.ExecuteCount(context.Background(), []string{"count-tokens-response-metadata"}, cliproxyexecutor.Request{Model: reqModel}, cliproxyexecutor.Options{}); errCount == nil {
		t.Fatal("ExecuteCount() unexpectedly succeeded")
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found after ExecuteCount()")
	}
	var state *ModelState
	for _, candidate := range updated.ModelStates {
		state = candidate
	}
	if state == nil {
		t.Fatal("ExecuteCount() did not record model cooldown state")
	}
	cooldown := state.NextRetryAfter.Sub(before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("explicit not-found (via Response.Metadata) cooldown = %v, want about 12h (endpoint-neutral misclassification: wire model not read from resp.Metadata)", cooldown)
	}
}

// midStreamWireModelExecutor mirrors an OpenAICompat-style executor whose
// ExecuteStream call itself succeeds (HTTP 200, err == nil) but whose SSE
// body contains a structured 404 arriving as a later frame. The conductor
// only ever observes this via StreamChunk.Err from the returned channel
// (see wrapStreamResult in conductor_stream.go), never via ExecuteStream's
// return error — so this is the shape Codex's item (b) finding targeted.
type midStreamWireModelExecutor struct {
	normalize func(model string) string
}

func (e midStreamWireModelExecutor) Identifier() string { return "mid-stream-wire-model" }
func (e midStreamWireModelExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e midStreamWireModelExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	wire := e.normalize(req.Model)
	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{}}]}\n\n")}
	ch <- cliproxyexecutor.StreamChunk{Err: wireModelStreamError{
		err: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    `{"error":{"type":"not_found_error","message":"model ` + wire + ` was not found"}}`,
		},
		model: wire,
	}}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}
func (midStreamWireModelExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (midStreamWireModelExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (midStreamWireModelExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestExecuteStreamMidStreamChunkErrorClassifiesAgainstReportedWireModel(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })

	manager := NewManager(nil, nil, nil)
	reqModel := "claude-opus-4-8-thinking-32k"
	normalize := func(m string) string { return strings.TrimSuffix(m, "-thinking-32k") }
	executor := midStreamWireModelExecutor{normalize: normalize}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "mid-stream-wire-model-auth", Provider: "mid-stream-wire-model"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: reqModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	before := time.Now()
	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{"mid-stream-wire-model"}, cliproxyexecutor.Request{Model: reqModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() unexpectedly failed before streaming began: %v", errExecute)
	}
	for range streamResult.Chunks {
		// Drain to let wrapStreamResult observe the terminal chunk error and
		// record the Result.
	}

	// wrapStreamResult records asynchronously as it forwards chunks; give it
	// a moment to finish before reading state.
	var state *ModelState
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		updated, ok := manager.GetByID(auth.ID)
		if !ok {
			t.Fatal("auth not found after ExecuteStream()")
		}
		for _, candidate := range updated.ModelStates {
			state = candidate
		}
		if state != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state == nil {
		t.Fatal("ExecuteStream() did not record model cooldown state from the mid-stream chunk error")
	}
	cooldown := state.NextRetryAfter.Sub(before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("cooldown = %v, want about 12h (mid-stream chunk error naming the reported wire model must classify as explicit not-found, not the short retry)", cooldown)
	}
}

// TestApplyAuthFailureStateGenericNotFoundSurvivesRacingCloudflareChallenge
// covers Codex's item (c) finding: a generic-404 backoff is stored in
// NextRecoverAt WITHOUT Exceeded set (see notFoundRetryAfter's non-explicit
// branch), so preserveLongerCooldown's old Exceeded-gated guard let a later
// Cloudflare challenge overwrite it with a shorter deadline. The fix drops
// the Exceeded requirement so ANY active (future) NextRecoverAt is honored.
func TestApplyAuthFailureStateGenericNotFoundSurvivesRacingCloudflareChallenge(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	now := time.Date(2026, 9, 3, 14, 42, 0, 0, time.UTC)
	generic404 := &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"}
	challenge := &Error{HTTPStatus: http.StatusForbidden, Message: "cloudflare challenge detected"}

	auth := &Auth{ID: "auth-generic-404-then-challenge"}
	// Escalate the generic-404 backoff a few times so its stored deadline is
	// clearly longer than a fresh level-0 Cloudflare cooldown, isolating the
	// Exceeded-gating bug from a coincidental tie in magnitude.
	for i := 0; i < 3; i++ {
		applyAuthFailureStateForModel(auth, generic404, nil, "", now, false)
	}
	if auth.Quota.Exceeded {
		t.Fatalf("generic 404 unexpectedly set Exceeded = true (test premise requires the non-Exceeded storage path)")
	}
	genericDeadline := auth.NextRetryAfter
	if !genericDeadline.After(now) {
		t.Fatalf("generic 404 NextRetryAfter = %v, want after %v", genericDeadline, now)
	}

	applyAuthFailureStateForModel(auth, challenge, nil, "", now, false)
	if auth.NextRetryAfter.Before(genericDeadline) {
		t.Fatalf("Cloudflare challenge shortened the generic-404 deadline: NextRetryAfter = %v, want >= %v", auth.NextRetryAfter, genericDeadline)
	}
}

// TestMarkResultGenericNotFoundSurvivesRacingShort429 covers the model-scoped
// half of Codex's item (c) finding: MarkResult's 429 branch (unlike
// applyAuthFailureStateForModel's auth-scoped 429 branch, which sets
// Exceeded=true before calling preserveLongerCooldown and so is
// self-satisfying either way) calls preserveLongerCooldown(state.Quota, next)
// BEFORE setting Exceeded on this failure, so it genuinely reads whatever
// Exceeded was left by the PRIOR failure. A generic-404 leaves Exceeded
// unset, so the old Exceeded-gated guard let a later, shorter 429 overwrite
// it; the fix (dropping the Exceeded requirement) closes that gap.
func TestMarkResultGenericNotFoundSurvivesRacingShort429(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	previousDisabled := quotaCooldownDisabled.Load()
	SetTransientErrorCooldownSeconds(0)
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() {
		transientErrorCooldownSeconds.Store(previousTransient)
		quotaCooldownDisabled.Store(previousDisabled)
	})

	generic404 := &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"}
	rateLimited := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
	shortRetryAfter := 5 * time.Second

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "generic-404-then-short-429", Provider: "codex", ModelStates: map[string]*ModelState{
		"gpt-5": {},
	}}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	before := time.Now()
	// Escalate a few times so the generic-404 deadline is clearly longer than
	// a fresh level-0 429 cooldown, isolating the Exceeded-gating bug from a
	// coincidental tie in magnitude.
	for i := 0; i < 3; i++ {
		manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Error: generic404})
	}
	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated.ModelStates["gpt-5"] == nil {
		t.Fatal("MarkResult() did not retain model cooldown state")
	}
	if updated.ModelStates["gpt-5"].Quota.Exceeded {
		t.Fatalf("generic 404 unexpectedly set Exceeded = true (test premise requires the non-Exceeded storage path)")
	}
	genericDeadline := updated.ModelStates["gpt-5"].NextRetryAfter
	if !genericDeadline.After(before) {
		t.Fatalf("generic 404 NextRetryAfter = %v, want after %v", genericDeadline, before)
	}

	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Error: rateLimited, RetryAfter: &shortRetryAfter})
	updated, ok = manager.GetByID(auth.ID)
	if !ok || updated.ModelStates["gpt-5"] == nil {
		t.Fatal("MarkResult() did not retain model cooldown state after 429")
	}
	if updated.ModelStates["gpt-5"].NextRetryAfter.Before(genericDeadline) {
		t.Fatalf("short 429 shortened the generic-404 deadline: NextRetryAfter = %v, want >= %v", updated.ModelStates["gpt-5"].NextRetryAfter, genericDeadline)
	}
}
