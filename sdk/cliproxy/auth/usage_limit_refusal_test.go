package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// codexUsageLimitBody is the 429 body the Codex backend sends when a ChatGPT
// plan's window runs out.
func codexUsageLimitBody(windowMinutes int, resetsAt time.Time) string {
	return fmt.Sprintf(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"pro","resets_at":%d,"eligible_promo":null,"limit_window_minutes":%d,"resets_in_seconds":%d}}`,
		resetsAt.Unix(), windowMinutes, int(time.Until(resetsAt).Seconds()))
}

func credentialQuotaAuth(id string, next time.Time, lastErr *Error) *Auth {
	return &Auth{
		ID:        id,
		Provider:  "codex",
		Status:    StatusActive,
		LastError: lastErr,
		Quota:     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: next},
	}
}

func usageLimitError(windowMinutes int, resetsAt time.Time) *Error {
	return &Error{HTTPStatus: http.StatusTooManyRequests, Message: codexUsageLimitBody(windowMinutes, resetsAt)}
}

func pickCooldownError(t *testing.T, model string, auths []*Auth) *modelCooldownError {
	t.Helper()
	_, err := (&FillFirstSelector{}).Pick(context.Background(), "codex", model, cliproxyexecutor.Options{}, auths)
	var cooldown *modelCooldownError
	if !errors.As(err, &cooldown) {
		t.Fatalf("Pick() error = %v (%T), want *modelCooldownError", err, err)
	}
	return cooldown
}

func cooldownErrorBody(t *testing.T, err error) map[string]any {
	t.Helper()
	var payload map[string]any
	if errUnmarshal := json.Unmarshal([]byte(err.Error()), &payload); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal(Error()) error = %v", errUnmarshal)
	}
	body, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("Error() payload missing error object: %v", payload)
	}
	return body
}

func TestPoolOutOfCodexQuotaReportsUsageLimit(t *testing.T) {
	now := time.Now()
	sessionReset := now.Add(2 * time.Hour)
	weeklyReset := now.Add(72 * time.Hour)
	auths := []*Auth{
		credentialQuotaAuth("pro", weeklyReset, usageLimitError(10080, weeklyReset)),
		credentialQuotaAuth("team", sessionReset, usageLimitError(300, sessionReset)),
	}

	cooldown := pickCooldownError(t, "gpt-6-astra", auths)
	if !IsUsageLimitReachedError(cooldown) {
		t.Fatalf("IsUsageLimitReachedError() = false, want true")
	}
	body := cooldownErrorBody(t, cooldown)
	if got := body["code"]; got != "model_cooldown" {
		t.Fatalf("error.code = %v, want model_cooldown", got)
	}
	if got := body["type"]; got != "usage_limit_reached" {
		t.Fatalf("error.type = %v, want usage_limit_reached", got)
	}
	if got, _ := body["resets_at"].(float64); int64(got) != sessionReset.Unix() {
		t.Fatalf("error.resets_at = %v, want the first reset %d", body["resets_at"], sessionReset.Unix())
	}
	if got := body["retryable"]; got != false {
		t.Fatalf("error.retryable = %v, want false", got)
	}
	if got, _ := body["limit_window_minutes"].(float64); got != 300 {
		t.Fatalf("error.limit_window_minutes = %v, want 300", body["limit_window_minutes"])
	}

	headers := cooldown.Headers()
	if got := headers.Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want none", got)
	}
	if got := headers.Get("X-Should-Retry"); got != "false" {
		t.Fatalf("X-Should-Retry = %q, want false", got)
	}
	if got := headers.Get("X-Codex-Primary-Used-Percent"); got != "100" {
		t.Fatalf("X-Codex-Primary-Used-Percent = %q, want 100", got)
	}
	if got := headers.Get("X-Codex-Primary-Window-Minutes"); got != "300" {
		t.Fatalf("X-Codex-Primary-Window-Minutes = %q, want 300", got)
	}
	if got := headers.Get("X-Codex-Primary-Reset-At"); got != strconv.FormatInt(sessionReset.Unix(), 10) {
		t.Fatalf("X-Codex-Primary-Reset-At = %q, want %d", got, sessionReset.Unix())
	}
	if got := headers.Get("X-Codex-Secondary-Used-Percent"); got != "" {
		t.Fatalf("X-Codex-Secondary-Used-Percent = %q, want none for the later credential", got)
	}
}

func TestWeeklyUsageLimitUsesSecondaryWindow(t *testing.T) {
	reset := time.Now().Add(48 * time.Hour)
	cooldown := pickCooldownError(t, "gpt-6-astra", []*Auth{credentialQuotaAuth("pro", reset, usageLimitError(10080, reset))})
	headers := cooldown.Headers()
	if got := headers.Get("X-Codex-Secondary-Used-Percent"); got != "100" {
		t.Fatalf("X-Codex-Secondary-Used-Percent = %q, want 100", got)
	}
	if got := headers.Get("X-Codex-Secondary-Window-Minutes"); got != "10080" {
		t.Fatalf("X-Codex-Secondary-Window-Minutes = %q, want 10080", got)
	}
	if got := headers.Get("X-Codex-Primary-Used-Percent"); got != "" {
		t.Fatalf("X-Codex-Primary-Used-Percent = %q, want none", got)
	}
}

func TestCooldownStaysRetryableWithoutUsageLimitEvidence(t *testing.T) {
	now := time.Now()
	reset := now.Add(3 * time.Hour)
	tests := []struct {
		name  string
		auths []*Auth
	}{
		{
			name: "one credential rate limited without usage_limit_reached",
			auths: []*Auth{
				credentialQuotaAuth("pro", reset, usageLimitError(300, reset)),
				credentialQuotaAuth("team", reset, &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`}),
			},
		},
		{
			name:  "first reset within the retry window",
			auths: []*Auth{credentialQuotaAuth("pro", now.Add(4*time.Minute), usageLimitError(300, now.Add(4*time.Minute)))},
		},
		{
			name:  "usage_limit_reached text without the status",
			auths: []*Auth{credentialQuotaAuth("pro", reset, &Error{Message: codexUsageLimitBody(300, reset)})},
		},
		{
			name:  "no recorded refusal",
			auths: []*Auth{credentialQuotaAuth("pro", reset, nil)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cooldown := pickCooldownError(t, "gpt-6-astra", tc.auths)
			if IsUsageLimitReachedError(cooldown) {
				t.Fatalf("IsUsageLimitReachedError() = true, want false")
			}
			if _, ok := cooldownErrorBody(t, cooldown)["type"]; ok {
				t.Fatalf("error.type is set on a retryable cooldown")
			}
			headers := cooldown.Headers()
			if got := headers.Get("Retry-After"); got == "" {
				t.Fatalf("Retry-After = empty, want the cooldown")
			}
			if got := headers.Get("X-Should-Retry"); got != "" {
				t.Fatalf("X-Should-Retry = %q, want none", got)
			}
		})
	}
}

func TestCodexUsageLimitWindowReadsEveryBodyForm(t *testing.T) {
	for name, message := range map[string]string{
		"compact":         `{"error":{"type":"usage_limit_reached","limit_window_minutes":300}}`,
		"indented":        "{\n  \"error\": {\n    \"type\": \"usage_limit_reached\",\n    \"limit_window_minutes\": 300\n  }\n}",
		"spaced":          `{"error": {"type": "usage_limit_reached", "limit_window_minutes": 300}}`,
		"prefixed":        `status 429: {"error":{"type":"usage_limit_reached","limit_window_minutes":300}}`,
		"websocket event": `{"status":429,"error":{"type":"usage_limit_reached","limit_window_minutes":300}}`,
		"top level":       `{"type":"usage_limit_reached","limit_window_minutes":300}`,
		"websocket body":  `{"type":"error","status":429,"body":{"error":{"type":"usage_limit_reached","limit_window_minutes":300}}}`,
		"response failed": `{"type":"response.failed","response":{"error":{"type":"usage_limit_reached","limit_window_minutes":300}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			window, ok := codexUsageLimitWindow(&Error{HTTPStatus: http.StatusTooManyRequests, Message: message})
			if !ok || window != 300 {
				t.Fatalf("codexUsageLimitWindow() = %d, %t, want 300, true", window, ok)
			}
		})
	}
}

func TestUsageLimitEvidenceFromModelStateAndWebsocketEvent(t *testing.T) {
	reset := time.Now().Add(5 * time.Hour)
	websocketEvent := &Error{
		HTTPStatus: http.StatusTooManyRequests,
		Message:    `{"status":429,"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":` + strconv.FormatInt(reset.Unix(), 10) + `,"limit_window_minutes":300}}`,
	}
	auth := &Auth{
		ID:       "team",
		Provider: "codex",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"gpt-6-astra": {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: reset,
				LastError:      websocketEvent,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: reset},
			},
		},
	}
	cooldown := pickCooldownError(t, "gpt-6-astra", []*Auth{auth})
	if !IsUsageLimitReachedError(cooldown) {
		t.Fatalf("IsUsageLimitReachedError() = false, want true")
	}
	if got := cooldown.Headers().Get("X-Codex-Primary-Window-Minutes"); got != "300" {
		t.Fatalf("X-Codex-Primary-Window-Minutes = %q, want 300", got)
	}
}

type usageLimitTestExecutor struct {
	provider string
	reset    time.Duration
	window   int
	// body replaces the usage_limit_reached refusal when set, as for another
	// provider's generic 429.
	body string
}

type usageLimitTestStatusError struct {
	body       string
	retryAfter time.Duration
}

func (e usageLimitTestStatusError) Error() string              { return e.body }
func (e usageLimitTestStatusError) StatusCode() int            { return http.StatusTooManyRequests }
func (e usageLimitTestStatusError) IsCredentialScoped() bool   { return true }
func (e usageLimitTestStatusError) RetryAfter() *time.Duration { return &e.retryAfter }

func (e *usageLimitTestExecutor) Identifier() string { return e.provider }

func (e *usageLimitTestExecutor) refusal() error {
	body := e.body
	if body == "" {
		body = codexUsageLimitBody(e.window, time.Now().Add(e.reset))
	}
	return usageLimitTestStatusError{body: body, retryAfter: e.reset}
}

func (e *usageLimitTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.refusal()
}

func (e *usageLimitTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, e.refusal()
}

func (*usageLimitTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*usageLimitTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*usageLimitTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// TestManagerReportsUsageLimitOnceEveryCredentialIsRefused drives the real
// selection path: both credentials answer usage_limit_reached, and the next
// request is refused locally as a usage limit, also after a restart.
func TestManagerReportsUsageLimitOnceEveryCredentialIsRefused(t *testing.T) {
	withQuotaCooldownEnabled(t)
	ctx := context.Background()
	provider := "usage-limit-" + uuid.NewString()
	model := "usage-limit-model-" + uuid.NewString()
	executor := &usageLimitTestExecutor{provider: provider, reset: 3 * time.Hour, window: 300}
	store := NewFileCooldownStateStore(t.TempDir())
	newManager := func() *Manager {
		manager := NewManager(nil, nil, nil)
		manager.SetRetryConfig(1, 0, 0)
		manager.RegisterExecutor(executor)
		manager.SetCooldownStateStore(store)
		return manager
	}

	manager := newManager()
	ids := []string{provider + "-a", provider + "-b"}
	for _, id := range ids {
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := manager.Register(ctx, &Auth{ID: id, Provider: provider, Status: StatusActive}); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
	}
	request := cliproxyexecutor.Request{Model: model}
	if _, errFirst := manager.Execute(ctx, []string{provider}, request, cliproxyexecutor.Options{}); errFirst == nil {
		t.Fatalf("first Execute() error = nil, want the upstream refusal")
	}

	assertUsageLimit := func(t *testing.T, m *Manager) {
		t.Helper()
		_, errExecute := m.Execute(ctx, []string{provider}, request, cliproxyexecutor.Options{})
		if !IsUsageLimitReachedError(errExecute) {
			t.Fatalf("Execute() error = %v, want a usage limit", errExecute)
		}
		_, errStream := m.ExecuteStream(ctx, []string{provider}, request, cliproxyexecutor.Options{Stream: true})
		if !IsUsageLimitReachedError(errStream) {
			t.Fatalf("ExecuteStream() error = %v, want a usage limit", errStream)
		}
		if got := SafeResponseHeaders(errStream).Get("X-Codex-Primary-Used-Percent"); got != "100" {
			t.Fatalf("X-Codex-Primary-Used-Percent = %q, want 100", got)
		}
	}
	assertUsageLimit(t, manager)

	restarted := newManager()
	for _, id := range ids {
		if _, errRegister := restarted.Register(ctx, &Auth{ID: id, Provider: provider, Status: StatusActive}); errRegister != nil {
			t.Fatalf("Register() after restart error = %v", errRegister)
		}
	}
	if errRestore := restarted.RestoreCooldownStates(ctx); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() error = %v", errRestore)
	}
	assertUsageLimit(t, restarted)
}

// registerUsageLimitPool registers two credentials of each executor's provider
// for model on a new manager and returns the manager and the credential IDs.
func registerUsageLimitPool(t *testing.T, selector Selector, model string, executors ...*usageLimitTestExecutor) (*Manager, []string) {
	t.Helper()
	manager := NewManager(nil, selector, nil)
	manager.SetRetryConfig(1, 0, 0)
	var ids []string
	for _, executor := range executors {
		manager.RegisterExecutor(executor)
		for _, suffix := range []string{"-a", "-b"} {
			id := executor.provider + suffix
			registry.GetGlobalRegistry().RegisterClient(id, executor.provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: executor.provider, Status: StatusActive}); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			ids = append(ids, id)
		}
	}
	return manager, ids
}

// executeTwice sends the same request twice: the first meets the upstream
// refusals, the second is answered locally from the cooldowns they set.
func executeTwice(t *testing.T, manager *Manager, providers []string, model string) error {
	t.Helper()
	request := cliproxyexecutor.Request{Model: model}
	if _, errFirst := manager.Execute(context.Background(), providers, request, cliproxyexecutor.Options{}); errFirst == nil {
		t.Fatal("first Execute() error = nil, want the upstream refusal")
	}
	_, errSecond := manager.Execute(context.Background(), providers, request, cliproxyexecutor.Options{})
	return errSecond
}

// The production selector, fill-first behind session affinity, decides through
// the manager's own availability check rather than the selector's.
func TestUsageLimitThroughSessionAffinitySelector(t *testing.T) {
	withQuotaCooldownEnabled(t)
	provider := "usage-limit-affinity-" + uuid.NewString()
	model := "usage-limit-model-" + uuid.NewString()
	manager, _ := registerUsageLimitPool(t, NewSessionAffinitySelector(&FillFirstSelector{}), model,
		&usageLimitTestExecutor{provider: provider, reset: 3 * time.Hour, window: 300})
	if err := executeTwice(t, manager, []string{provider}, model); !IsUsageLimitReachedError(err) {
		t.Fatalf("Execute() error = %v, want a usage limit", err)
	}
}

// Two providers go through the mixed scheduler. The credential that reopens
// first, here the weekly one, names the window.
func TestUsageLimitAcrossTwoProviders(t *testing.T) {
	withQuotaCooldownEnabled(t)
	fiveHour := "usage-limit-5h-" + uuid.NewString()
	weekly := "usage-limit-weekly-" + uuid.NewString()
	model := "usage-limit-model-" + uuid.NewString()
	manager, _ := registerUsageLimitPool(t, &FillFirstSelector{}, model,
		&usageLimitTestExecutor{provider: fiveHour, reset: 3 * time.Hour, window: 300},
		&usageLimitTestExecutor{provider: weekly, reset: 2 * time.Hour, window: 10080})
	err := executeTwice(t, manager, []string{fiveHour, weekly}, model)
	if !IsUsageLimitReachedError(err) {
		t.Fatalf("Execute() error = %v, want a usage limit", err)
	}
	if got := SafeResponseHeaders(err).Get("X-Codex-Secondary-Window-Minutes"); got != "10080" {
		t.Fatalf("X-Codex-Secondary-Window-Minutes = %q, want 10080", got)
	}
}

// A provider cooling on a generic 429 keeps the answer retryable, even beside
// credentials that are out of Codex quota.
func TestUsageLimitNotClaimedBesideAnotherProvidersRateLimit(t *testing.T) {
	for name, selector := range map[string]Selector{
		"fill-first":       &FillFirstSelector{},
		"session affinity": NewSessionAffinitySelector(&FillFirstSelector{}),
	} {
		t.Run(name, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			codex := "usage-limit-codex-" + uuid.NewString()
			other := "usage-limit-generic-" + uuid.NewString()
			model := "usage-limit-model-" + uuid.NewString()
			manager, _ := registerUsageLimitPool(t, selector, model,
				&usageLimitTestExecutor{provider: codex, reset: 3 * time.Hour, window: 300},
				&usageLimitTestExecutor{provider: other, reset: 3 * time.Hour, body: `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`})
			if err := executeTwice(t, manager, []string{codex, other}, model); IsUsageLimitReachedError(err) {
				t.Fatalf("Execute() error = %v, want the retryable cooldown", err)
			}
		})
	}
}

// A pinned request (a Responses websocket session pins each turn to the
// credential that served the last one) sees only that credential. Its quota
// says nothing about the rest of the pool, so the answer stays retryable.
func TestPinnedRequestKeepsRetryableCooldown(t *testing.T) {
	for name, selector := range map[string]Selector{
		"fill-first":       &FillFirstSelector{},
		"session affinity": NewSessionAffinitySelector(&FillFirstSelector{}),
	} {
		t.Run(name, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			ctx := context.Background()
			provider := "usage-limit-pinned-" + uuid.NewString()
			model := "usage-limit-model-" + uuid.NewString()
			manager, ids := registerUsageLimitPool(t, selector, model,
				&usageLimitTestExecutor{provider: provider, reset: 3 * time.Hour, window: 300})
			retry := 3 * time.Hour
			manager.MarkResult(ctx, Result{AuthID: ids[0], Provider: provider, Model: model, Error: usageLimitError(300, time.Now().Add(retry)), RetryAfter: &retry, CredentialScope: true})

			request := cliproxyexecutor.Request{Model: model}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: ids[0]}}
			_, errStream := manager.ExecuteStream(ctx, []string{provider}, request, opts)
			_, errExecute := manager.Execute(ctx, []string{provider}, request, opts)
			_, errSelect := manager.SelectAuth(ctx, provider, model, opts)
			for call, err := range map[string]error{"ExecuteStream": errStream, "Execute": errExecute, "SelectAuth": errSelect} {
				var cooldown *modelCooldownError
				if !errors.As(err, &cooldown) {
					t.Fatalf("pinned %s() error = %v, want a model cooldown", call, err)
				}
				if IsUsageLimitReachedError(err) {
					t.Fatalf("pinned %s() reports a usage limit for the pool", call)
				}
				if SafeResponseHeaders(err).Get("Retry-After") == "" {
					t.Fatalf("pinned %s() lost Retry-After", call)
				}
			}
		})
	}
}

// A stream already running on a refused credential can end afterwards with an
// ordinary upstream error. That replaces LastError, but the credential is still
// out of quota, so the pool still reports the usage limit.
func TestUsageLimitSurvivesALaterFailure(t *testing.T) {
	for name, later := range map[string]*Error{
		"502":           {HTTPStatus: http.StatusBadGateway, Message: `{"error":{"message":"upstream connect error"}}`},
		"transient 429": {HTTPStatus: http.StatusTooManyRequests, Code: transientRateLimitErrorCode, Message: `{"error":{"message":"busy, retry in 2s"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			ctx := context.Background()
			provider := "usage-limit-later-" + uuid.NewString()
			model := "usage-limit-model-" + uuid.NewString()
			manager, ids := registerUsageLimitPool(t, NewSessionAffinitySelector(&FillFirstSelector{}), model,
				&usageLimitTestExecutor{provider: provider, reset: 3 * time.Hour, window: 300})
			if err := executeTwice(t, manager, []string{provider}, model); !IsUsageLimitReachedError(err) {
				t.Fatalf("Execute() error = %v, want a usage limit", err)
			}
			retry := 2 * time.Second
			manager.MarkResult(ctx, Result{AuthID: ids[0], Provider: provider, Model: model, Error: later, RetryAfter: &retry})
			_, err := manager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			if !IsUsageLimitReachedError(err) {
				t.Fatalf("Execute() after a later %s error = %v, want the usage limit still", name, err)
			}
		})
	}
}

// The mark outlives a later failure across a restart too, when only it still
// carries the classification. A Codex usage limit covers the whole account, so
// it also holds for another model of the same credentials.
func TestUsageLimitMarkSurvivesARestart(t *testing.T) {
	withQuotaCooldownEnabled(t)
	ctx := context.Background()
	provider := "usage-limit-restart-" + uuid.NewString()
	model := "usage-limit-model-" + uuid.NewString()
	executor := &usageLimitTestExecutor{provider: provider, reset: 3 * time.Hour, window: 300}
	store := NewFileCooldownStateStore(t.TempDir())
	otherModel := "usage-limit-other-" + uuid.NewString()
	manager, ids := registerUsageLimitPool(t, NewSessionAffinitySelector(&FillFirstSelector{}), model, executor)
	for _, id := range ids {
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}, {ID: otherModel}})
	}
	manager.SetCooldownStateStore(store)
	if err := executeTwice(t, manager, []string{provider}, model); !IsUsageLimitReachedError(err) {
		t.Fatalf("Execute() error = %v, want a usage limit", err)
	}
	for _, id := range ids {
		manager.MarkResult(ctx, Result{AuthID: id, Provider: provider, Model: model, Error: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream connect error"}})
	}

	restarted := NewManager(nil, NewSessionAffinitySelector(&FillFirstSelector{}), nil)
	restarted.SetRetryConfig(1, 0, 0)
	restarted.RegisterExecutor(executor)
	restarted.SetCooldownStateStore(store)
	for _, id := range ids {
		if _, errRegister := restarted.Register(ctx, &Auth{ID: id, Provider: provider, Status: StatusActive}); errRegister != nil {
			t.Fatalf("Register() after restart error = %v", errRegister)
		}
	}
	if errRestore := restarted.RestoreCooldownStates(ctx); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() error = %v", errRestore)
	}
	for _, requested := range []string{model, otherModel} {
		if _, err := restarted.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: requested}, cliproxyexecutor.Options{}); !IsUsageLimitReachedError(err) {
			t.Fatalf("Execute(%s) after restart error = %v, want the usage limit", requested, err)
		}
	}
}

// A mark counts only for the cooldown it was left for. A credential that cools
// again later, for another reason and until another time, is not a usage limit.
func TestUsageLimitMarkIgnoredForANewerCooldown(t *testing.T) {
	now := time.Now()
	next := now.Add(2 * time.Hour)
	auth := credentialQuotaAuth("stale-mark", next, &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"rate_limit_exceeded"}}`})
	auth.Quota.UsageLimitResetAt = now.Add(5 * time.Hour)
	auth.Quota.UsageLimitWindowMinutes = 300
	if _, ok := codexUsageLimitEvidence(auth, "", next, now); ok {
		t.Fatal("a mark left for another cooldown counted as usage-limit evidence")
	}
	auth.Quota.UsageLimitResetAt = next
	if window, ok := codexUsageLimitEvidence(auth, "", next, now); !ok || window != 300 {
		t.Fatalf("evidence = (%d, %v), want (300, true) for the cooldown the mark was left for", window, ok)
	}
}
