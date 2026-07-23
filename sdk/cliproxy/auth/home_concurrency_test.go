package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type fixtureHomeDispatcher struct {
	payload            []byte
	payloads           [][]byte
	calls              int
	closedForAmbiguity bool
	onAbort            func()
}

func (d *fixtureHomeDispatcher) HeartbeatOK() bool { return true }

func (d *fixtureHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	if len(d.payloads) == 0 {
		return d.payload, nil
	}
	if d.calls >= len(d.payloads) {
		return nil, errors.New("unexpected Home dispatch")
	}
	payload := d.payloads[d.calls]
	d.calls++
	return payload, nil
}

func (d *fixtureHomeDispatcher) AbortAmbiguousDispatch() {
	d.closedForAmbiguity = true
	if d.onAbort != nil {
		d.onAbort()
	}
}

func newHomeSelectionTestManager(t *testing.T, dispatcher homeAuthDispatcher) *Manager {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	return manager
}

type busyHomeRetryDispatcher struct {
	calls atomic.Int32
}

func (*busyHomeRetryDispatcher) HeartbeatOK() bool { return true }

func (d *busyHomeRetryDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	return []byte(`{"error":{"type":"credential_concurrency_exceeded","message":"busy","retryable":true,"retry_after_ms":20000}}`), nil
}

func (*busyHomeRetryDispatcher) AbortAmbiguousDispatch() {}

func TestHomeBusySkipsNormalAndStreamOuterRetries(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "stream"}[stream], func(t *testing.T) {
			dispatcher := &busyHomeRetryDispatcher{}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, 30*time.Second, 0)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: "retry-auth", Provider: "home-busy"}); errRegister != nil {
				t.Fatalf("register retry auth: %v", errRegister)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			started := time.Now()
			go func() {
				if stream {
					_, errExecute := manager.ExecuteStream(ctx, []string{"home-busy"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
					result <- errExecute
					return
				}
				_, errExecute := manager.Execute(ctx, []string{"home-busy"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				result <- errExecute
			}()

			select {
			case errExecute := <-result:
				var busy *HomeConcurrencyBusyError
				if !errors.As(errExecute, &busy) {
					t.Fatalf("execution error = %v, want HomeConcurrencyBusyError", errExecute)
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("Home busy waited for its retry hint")
			}
			if elapsed := time.Since(started); elapsed >= 250*time.Millisecond {
				t.Fatalf("Home busy returned after %v, want prompt return", elapsed)
			}
			if got := dispatcher.calls.Load(); got != 1 {
				t.Fatalf("Home RPOP calls = %d, want 1", got)
			}
		})
	}
}

func TestPickHomeDispatchSelectionReleasesAccountedScopeAfterAuthValidationFailure(t *testing.T) {
	dispatcher := &fixtureHomeDispatcher{payload: []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"auth":{"id":"","provider":"codex"}}`)}
	manager := newHomeSelectionTestManager(t, dispatcher)

	selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
	if selection != nil || errPick == nil {
		t.Fatalf("selection=%#v error=%v", selection, errPick)
	}
	if dispatcher.closedForAmbiguity {
		t.Fatal("accounted local auth validation failure fenced Home")
	}
	if freeze := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now()); len(freeze.Executions) != 0 {
		t.Fatalf("scope was not released: %#v", freeze)
	}
}

func TestPickHomeDispatchSelectionReleasesAccountedScopeAfterPayloadDecodeFailure(t *testing.T) {
	dispatcher := &fixtureHomeDispatcher{payload: []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"model":123,"auth":{"id":"cred-1","provider":"codex"}}`)}
	manager := newHomeSelectionTestManager(t, dispatcher)

	selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
	if selection != nil || errPick == nil {
		t.Fatalf("selection=%#v error=%v", selection, errPick)
	}
	if dispatcher.closedForAmbiguity {
		t.Fatal("accounted payload decode failure fenced Home")
	}
	if freeze := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now()); len(freeze.Executions) != 0 {
		t.Fatalf("scope was not released: %#v", freeze)
	}
}

func TestPickHomeDispatchSelectionRejectsMalformedErrorPresence(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantCode  string
		wantFence bool
	}{
		{name: "string without tuple", payload: `{"error":"busy","auth":{"id":"cred-1","provider":"codex"}}`, wantCode: "invalid_auth"},
		{name: "empty object without tuple", payload: `{"error":{},"auth":{"id":"cred-1","provider":"codex"}}`, wantCode: "invalid_auth"},
		{name: "null without tuple", payload: `{"error":null,"auth":{"id":"cred-1","provider":"codex"}}`, wantCode: "invalid_auth"},
		{name: "empty type and code without tuple", payload: `{"error":{"type":" ","code":""},"auth":{"id":"cred-1","provider":"codex"}}`, wantCode: "invalid_auth"},
		{name: "string with tuple", payload: `{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"error":"busy","auth":{"id":"cred-1","provider":"codex"}}`, wantCode: "invalid_home_concurrency", wantFence: true},
		{name: "empty object with tuple", payload: `{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"error":{},"auth":{"id":"cred-1","provider":"codex"}}`, wantCode: "invalid_home_concurrency", wantFence: true},
		{name: "null with tuple", payload: `{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"error":null,"auth":{"id":"cred-1","provider":"codex"}}`, wantCode: "invalid_home_concurrency", wantFence: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher := &fixtureHomeDispatcher{payload: []byte(tt.payload)}
			manager := newHomeSelectionTestManager(t, dispatcher)
			manager.executors["codex"] = schedulerTestExecutor{provider: "codex"}

			selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
			if selection != nil || errPick == nil {
				t.Fatalf("selection=%#v error=%v, want malformed error rejection", selection, errPick)
			}
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr.Code != tt.wantCode {
				t.Fatalf("error=%#v, want code %q", errPick, tt.wantCode)
			}
			if dispatcher.closedForAmbiguity != tt.wantFence {
				t.Fatalf("fenced=%t, want %t", dispatcher.closedForAmbiguity, tt.wantFence)
			}
			if freeze := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now()); len(freeze.Executions) != 0 {
				t.Fatalf("scope was not released: %#v", freeze)
			}
		})
	}
}

func TestPickHomeDispatchSelectionValidAccountedLocalValidationReleasesAndKeepsHomeHealthy(t *testing.T) {
	validPayload := []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"auth_index":"cred-1","auth":{"id":"cred-1","provider":"codex"}}`)
	tests := map[string][]byte{
		"auth validation":   []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"auth":{"id":"","provider":"codex"}}`),
		"payload decode":    []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"model":123,"auth":{"id":"cred-1","provider":"codex"}}`),
		"auth decode":       []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"auth":"invalid"}`),
		"identity mismatch": []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"auth_index":"other","auth":{"id":"cred-1","provider":"codex"}}`),
	}
	for name, invalidPayload := range tests {
		t.Run(name, func(t *testing.T) {
			dispatcher := &fixtureHomeDispatcher{payloads: [][]byte{invalidPayload, validPayload}}
			manager := newHomeSelectionTestManager(t, dispatcher)
			manager.executors["codex"] = schedulerTestExecutor{provider: "codex"}
			releases := make(map[executionregistry.ReleaseGroup]int64)
			registry := manager.HomeDispatchBundle().registry
			registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, sequence int64) {
				releases[group] = sequence
			})

			selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
			if selection != nil || errPick == nil {
				t.Fatalf("first selection=%#v error=%v, want local validation failure", selection, errPick)
			}
			if dispatcher.closedForAmbiguity {
				t.Fatal("valid accounted local validation failure fenced Home")
			}
			group := executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "gpt"}
			if len(releases) != 1 || releases[group] != 1 {
				t.Fatalf("first releases=%#v, want exactly %v:1", releases, group)
			}

			selection, errPick = manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
			if errPick != nil || selection == nil {
				t.Fatalf("second selection=%#v error=%v, want healthy dispatch", selection, errPick)
			}
			selection.End("test_complete")
			if dispatcher.closedForAmbiguity {
				t.Fatal("second dispatch fenced Home")
			}
			if len(releases) != 1 || releases[group] != 2 {
				t.Fatalf("cumulative releases=%#v, want exactly %v:2", releases, group)
			}
		})
	}
}

func TestPickHomeDispatchSelectionFencesAccountedBusyError(t *testing.T) {
	dispatcher := &fixtureHomeDispatcher{payload: []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"error":{"type":"credential_concurrency_exceeded","message":"busy","retry_after_ms":750}}`)}
	manager := newHomeSelectionTestManager(t, dispatcher)
	abortSawScope := make(chan bool, 1)
	dispatcher.onAbort = func() {
		freeze := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now())
		abortSawScope <- len(freeze.Executions) == 1
	}

	selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
	if selection != nil || errPick == nil {
		t.Fatalf("selection=%#v error=%v", selection, errPick)
	}
	var busy *HomeConcurrencyBusyError
	if errors.As(errPick, &busy) {
		t.Fatalf("accounted error returned ordinary busy response: %v", errPick)
	}
	if !dispatcher.closedForAmbiguity {
		t.Fatal("accounted busy error did not fence Home")
	}
	if sawScope := <-abortSawScope; !sawScope {
		t.Fatal("accounted scope ended before Home dispatch was aborted")
	}
	if freeze := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now()); len(freeze.Executions) != 0 {
		t.Fatalf("scope was not released: %#v", freeze)
	}
}

func TestMalformedAccountedTupleClosesHomeClient(t *testing.T) {
	dispatcher := &fixtureHomeDispatcher{payload: []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":""},"auth":{"id":"cred-1","provider":"codex"}}`)}
	manager := newHomeSelectionTestManager(t, dispatcher)

	selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
	if selection != nil || errPick == nil || !dispatcher.closedForAmbiguity {
		t.Fatalf("selection=%#v error=%v closed=%t", selection, errPick, dispatcher.closedForAmbiguity)
	}
}

func TestConcurrencyDispatchFixture(t *testing.T) {
	t.Run("accounted", func(t *testing.T) {
		raw, errRead := os.ReadFile("../../../internal/home/testdata/concurrency_dispatch_accounted.json")
		if errRead != nil {
			t.Fatalf("ReadFile(accounted fixture) error = %v", errRead)
		}

		var fixture struct {
			Model     string `json:"model"`
			Provider  string `json:"provider"`
			AuthIndex string `json:"auth_index"`
			Auth      struct {
				ID       string `json:"id"`
				Provider string `json:"provider"`
			} `json:"auth"`
			Concurrency homeConcurrencyTuple `json:"concurrency"`
		}
		if errUnmarshal := json.Unmarshal(raw, &fixture); errUnmarshal != nil {
			t.Fatalf("Unmarshal(accounted fixture) error = %v", errUnmarshal)
		}
		wantTuple := homeConcurrencyTuple{Accounted: true, CredentialID: "cred-1", Model: "gpt"}
		if fixture.Concurrency != wantTuple {
			t.Fatalf("accounted concurrency = %#v, want %#v", fixture.Concurrency, wantTuple)
		}
		if fixture.Model != "gpt" || fixture.Provider != "codex" || fixture.AuthIndex != "cred-1" || fixture.Auth.ID != "cred-1" || fixture.Auth.Provider != "codex" {
			t.Fatalf("accounted identity model=%q provider=%q auth_index=%q auth=%#v", fixture.Model, fixture.Provider, fixture.AuthIndex, fixture.Auth)
		}

		envelope, errEnvelope := decodeHomeDispatchConcurrencyEnvelope(raw)
		if errEnvelope != nil {
			t.Fatalf("decodeHomeDispatchConcurrencyEnvelope(accounted fixture) error = %v", errEnvelope)
		}
		if !envelope.Present || envelope.Tuple != wantTuple {
			t.Fatalf("accounted envelope = %#v, want present tuple %#v", envelope, wantTuple)
		}

		dispatcher := &fixtureHomeDispatcher{payload: raw}
		manager := newHomeSelectionTestManager(t, dispatcher)
		manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
		selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
		if errPick != nil || selection == nil {
			t.Fatalf("pickHomeDispatchSelection(accounted fixture) selection=%#v error=%v", selection, errPick)
		}
		defer selection.End("fixture_complete")
		if selection.Auth == nil || selection.Auth.ID != "cred-1" || selection.Auth.Index != "cred-1" || selection.Auth.Provider != "codex" {
			t.Fatalf("selected auth = %#v", selection.Auth)
		}

		bundle := manager.HomeDispatchBundle()
		if bundle == nil || bundle.registry == nil {
			t.Fatal("accounted fixture did not retain a Home dispatch registry")
		}
		freeze := bundle.registry.FreezeInFlight(time.Now())
		if len(freeze.Executions) != 1 {
			t.Fatalf("accounted fixture executions = %#v", freeze.Executions)
		}
		gotScope := freeze.Executions[0]
		if !gotScope.Accounted || gotScope.CredentialID != "cred-1" || gotScope.Model != "gpt" {
			t.Fatalf("accounted fixture scope = %#v", gotScope)
		}
	})

	t.Run("busy", func(t *testing.T) {
		raw, errRead := os.ReadFile("../../../internal/home/testdata/concurrency_dispatch_busy.json")
		if errRead != nil {
			t.Fatalf("ReadFile(busy fixture) error = %v", errRead)
		}

		var fixture struct {
			Error *struct {
				Type         string `json:"type"`
				Message      string `json:"message"`
				Retryable    bool   `json:"retryable"`
				RetryAfterMS int64  `json:"retry_after_ms"`
			} `json:"error"`
		}
		if errUnmarshal := json.Unmarshal(raw, &fixture); errUnmarshal != nil {
			t.Fatalf("Unmarshal(busy fixture) error = %v", errUnmarshal)
		}
		if fixture.Error == nil {
			t.Fatal("busy fixture has no error object")
		}
		if fixture.Error.Type != "credential_concurrency_exceeded" || fixture.Error.Message != "credential concurrency limit reached" || !fixture.Error.Retryable || fixture.Error.RetryAfterMS != 750 {
			t.Fatalf("busy fixture error = %#v", fixture.Error)
		}

		errBusy := decodeHomeDispatchError(raw)
		var busy *HomeConcurrencyBusyError
		if !errors.As(errBusy, &busy) || busy == nil {
			t.Fatalf("decodeHomeDispatchError(busy fixture) error = %#v, want *HomeConcurrencyBusyError", errBusy)
		}
		if got := busy.StatusCode(); got != http.StatusTooManyRequests {
			t.Fatalf("busy status = %d, want %d", got, http.StatusTooManyRequests)
		}
		retryAfter := busy.RetryAfter()
		if retryAfter == nil || *retryAfter != 750*time.Millisecond {
			t.Fatalf("busy retry after = %v, want 750ms", retryAfter)
		}
		var cause *Error
		if !errors.As(errBusy, &cause) || cause == nil || cause.Code != fixture.Error.Type || cause.Message != fixture.Error.Message || !cause.Retryable || cause.HTTPStatus != http.StatusTooManyRequests {
			t.Fatalf("busy typed cause = %#v", cause)
		}
	})
}

func TestHomeBusyErrorMaps429AndRetryAfter(t *testing.T) {
	errBusy := decodeHomeDispatchError([]byte(`{"error":{"type":"credential_concurrency_exceeded","message":"busy","retryable":true,"retry_after_ms":750}}`))
	statusError, ok := errBusy.(interface{ StatusCode() int })
	if !ok || statusError.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %#v", errBusy)
	}
	retryError, ok := errBusy.(interface{ RetryAfter() *time.Duration })
	if !ok || retryError.RetryAfter() == nil || *retryError.RetryAfter() != 750*time.Millisecond {
		t.Fatalf("retry after = %v", retryError.RetryAfter())
	}
}

func TestHomeConcurrencyTupleAuthMismatchEndsScope(t *testing.T) {
	dispatcher := &fixtureHomeDispatcher{payload: []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"auth_index":"other","auth":{"id":"cred-1","provider":"codex"}}`)}
	manager := newHomeSelectionTestManager(t, dispatcher)
	manager.executors["codex"] = schedulerTestExecutor{provider: "codex"}

	selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
	if selection != nil || errPick == nil {
		t.Fatalf("selection=%#v error=%v", selection, errPick)
	}
	if dispatcher.closedForAmbiguity {
		t.Fatal("accounted auth identity mismatch fenced Home")
	}
	if freeze := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now()); len(freeze.Executions) != 0 {
		t.Fatalf("scope was not released: %#v", freeze)
	}
}

func TestOldHomeDispatchIsUnaccounted(t *testing.T) {
	dispatcher := &fixtureHomeDispatcher{payload: []byte(`{"auth":{"id":"cred-1","provider":"codex"}}`)}
	manager := newHomeSelectionTestManager(t, dispatcher)
	manager.executors["codex"] = schedulerTestExecutor{provider: "codex"}

	selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
	if errPick != nil || selection == nil {
		t.Fatalf("selection=%#v error=%v", selection, errPick)
	}
	defer selection.End("test")
	freeze := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now())
	if len(freeze.Executions) != 1 || freeze.Executions[0].Accounted {
		t.Fatalf("old Home dispatch freeze = %#v", freeze)
	}
}

func TestHomeBusyErrorHeadersRoundUpMilliseconds(t *testing.T) {
	errBusy := decodeHomeDispatchError([]byte(`{"error":{"type":"credential_concurrency_exceeded","message":"busy","retry_after_ms":750}}`))
	headers, ok := errBusy.(interface{ SafeResponseHeaders() http.Header })
	if !ok {
		t.Fatalf("error has no safe headers: %#v", errBusy)
	}
	if got := headers.SafeResponseHeaders().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

func TestInstallHomeConcurrencyScopeRejectsNonCanonicalTuple(t *testing.T) {
	registry := executionregistry.New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	defer pending.End()

	_, errInstall := installHomeConcurrencyScope(registry, pending, homeConcurrencyTuple{
		Accounted: true, CredentialID: " cred-1 ", Model: "gpt",
	}, executionregistry.ScopeSpec{Kind: "http", StartedAt: time.Now()})
	if !errors.Is(errInstall, ErrMalformedHomeConcurrencyTuple) {
		t.Fatalf("install error = %v, want malformed tuple", errInstall)
	}
}

func TestPickHomeDispatchSelectionFencesInvalidExplicitConcurrency(t *testing.T) {
	tests := []string{
		`{"concurrency":{"accounted":false,"credential_id":"cred-1","model":"gpt"},"auth":{"id":"cred-1","provider":"codex"}}`,
		`{"concurrency":{"accounted":true,"credential_id":" cred-1","model":"gpt"},"auth":{"id":"cred-1","provider":"codex"}}`,
		`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"other"},"model":"gpt","auth":{"id":"cred-1","provider":"codex"}}`,
	}
	for _, payload := range tests {
		dispatcher := &fixtureHomeDispatcher{payload: []byte(payload)}
		manager := newHomeSelectionTestManager(t, dispatcher)

		selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
		if selection != nil || errPick == nil || !dispatcher.closedForAmbiguity {
			t.Fatalf("payload=%s selection=%#v error=%v closed=%t", payload, selection, errPick, dispatcher.closedForAmbiguity)
		}
	}
}

func TestPickHomeDispatchSelectionReleasesAccountedScopeAfterAuthDecodeFailure(t *testing.T) {
	dispatcher := &fixtureHomeDispatcher{payload: []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"auth":"invalid"}`)}
	manager := newHomeSelectionTestManager(t, dispatcher)

	selection, errPick := manager.pickHomeDispatchSelection(context.Background(), "gpt", cliproxyexecutor.Options{})
	if selection != nil || errPick == nil || dispatcher.closedForAmbiguity {
		t.Fatalf("selection=%#v error=%v closed=%t", selection, errPick, dispatcher.closedForAmbiguity)
	}
	if freeze := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now()); len(freeze.Executions) != 0 {
		t.Fatalf("scope was not released: %#v", freeze)
	}
}

func TestHomeConcurrencyBusyErrorsRemainTypedWhenWrapped(t *testing.T) {
	for _, code := range []string{"credential_concurrency_exceeded", "credential_model_concurrency_exceeded"} {
		errBusy := decodeHomeDispatchError([]byte(fmt.Sprintf(`{"error":{"type":%q,"message":"busy","retryable":false}}`, code)))
		var busy *HomeConcurrencyBusyError
		if !errors.As(errBusy, &busy) {
			t.Fatalf("code=%s error=%#v, want typed busy error", code, errBusy)
		}
		if busy.RetryAfter() != nil {
			t.Fatalf("code=%s retry after = %v, want nil", code, busy.RetryAfter())
		}
		var cause *Error
		if !errors.As(fmt.Errorf("wrapped: %w", errBusy), &cause) || cause.Code != code || cause.Retryable {
			t.Fatalf("code=%s cause=%#v", code, cause)
		}
	}
}

func TestRetryAfterFromWrappedHomeBusyError(t *testing.T) {
	errBusy := NewHomeConcurrencyBusyError("busy", 750*time.Millisecond)
	if got := retryAfterFromError(fmt.Errorf("wrapped: %w", errBusy)); got == nil || *got != 750*time.Millisecond {
		t.Fatalf("retry after = %v, want 750ms", got)
	}
}

func TestCanonicalHomeConcurrencyModelKeyMatchesHomeLimiter(t *testing.T) {
	cases := map[string]string{
		" gpt(high) ":       "gpt",
		"gpt(8192)":         "gpt",
		"gpt(-1)":           "gpt",
		" GPT(AUTO) ":       "gpt",
		"model(custom)":     "model(custom)",
		"model(+1)":         "model(+1)",
		"model(2147483648)": "model(2147483648)",
		"(high)":            "(high)",
	}
	for input, want := range cases {
		if got := canonicalHomeConcurrencyModelKey(input); got != want {
			t.Fatalf("canonicalHomeConcurrencyModelKey(%q) = %q, want %q", input, got, want)
		}
	}
	if got := canonicalHomeConcurrencyModelKey("gpt\xff(high)"); got != "" {
		t.Fatalf("canonicalHomeConcurrencyModelKey() = %q, want empty for malformed UTF-8", got)
	}
}

func TestAccountedHomeConcurrencyTupleRequiresCanonicalLimiterModel(t *testing.T) {
	for _, model := range []string{"GPT", "gpt(high)", "model(custom) "} {
		errValidate := validateAccountedHomeConcurrencyTuple(homeConcurrencyTuple{Accounted: true, CredentialID: "cred-1", Model: model})
		if !errors.Is(errValidate, ErrMalformedHomeConcurrencyTuple) {
			t.Fatalf("model=%q validation error = %v, want malformed tuple", model, errValidate)
		}
	}
}

func TestHomeConcurrencyTupleStringsAreValidUTF8(t *testing.T) {
	if utf8.ValidString(string([]byte{0xff})) {
		t.Fatal("test setup expected invalid UTF-8")
	}
	if _, errDecode := decodeHomeDispatchConcurrencyEnvelope([]byte{'{', 0xff, '}'}); errDecode == nil {
		t.Fatal("raw non-UTF-8 Home envelope was accepted")
	}
	if !errors.Is(validateAccountedHomeConcurrencyTuple(homeConcurrencyTuple{Accounted: true, CredentialID: string([]byte{0xff}), Model: "gpt"}), ErrMalformedHomeConcurrencyTuple) {
		t.Fatal("invalid UTF-8 credential was accepted")
	}
	if !errors.Is(validateAccountedHomeConcurrencyTuple(homeConcurrencyTuple{Accounted: true, CredentialID: "cred-1", Model: strings.Repeat("g", 257)}), ErrMalformedHomeConcurrencyTuple) {
		t.Fatal("oversized model was accepted")
	}
}
