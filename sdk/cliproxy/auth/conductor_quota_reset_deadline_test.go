package auth

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// quotaDeadlineErr is the error shape an Antigravity executor returns for an exhausted
// quota window: a 429 whose RetryAfter is a hard upstream reset deadline.
type quotaDeadlineErr struct {
	group    string
	after    time.Duration
	hintOnly bool
}

func (e quotaDeadlineErr) Error() string              { return "Individual quota reached" }
func (e quotaDeadlineErr) StatusCode() int            { return 429 }
func (e quotaDeadlineErr) QuotaGroup() string         { return e.group }
func (e quotaDeadlineErr) IsCredentialScoped() bool   { return false }
func (e quotaDeadlineErr) IsQuotaResetDeadline() bool { return !e.hintOnly }

func (e quotaDeadlineErr) RetryAfter() *time.Duration {
	v := e.after
	return &v
}

// TestQuotaResetDeadlineHonoredWhenCoolingDisabled is the regression guard for the
// reported production behavior: with routing.cooldown.disable-cooling: true (the live
// configuration), an Antigravity QUOTA_EXHAUSTED 429 carrying a ~48h reset deadline must
// still cool that credential's quota group. Before the fix, disable-cooling short
// circuited the whole 429 branch, so nothing was recorded and selection re-picked the
// same exhausted credential seconds later.
func TestQuotaResetDeadlineHonoredWhenCoolingDisabled(t *testing.T) {
	const model = "gemini-3.8-flash-high"
	resetAfter := 47*time.Hour + 55*time.Minute + 2*time.Second

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{DisableCooling: true})
	t.Cleanup(func() { manager.SetConfig(&internalconfig.Config{}) })
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "antigravity-1", Provider: "antigravity", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	before := time.Now()
	result := Result{
		AuthID:             "antigravity-1",
		Provider:           "antigravity",
		Model:              model,
		RouteModel:         model,
		Success:            false,
		Error:              &Error{HTTPStatus: 429, Message: "Individual quota reached"},
		QuotaGroup:         AntigravityQuotaGroupGemini,
		QuotaResetDeadline: true,
	}
	result.RetryAfter = &resetAfter
	manager.MarkResult(context.Background(), result)

	stored, ok := manager.GetByID("antigravity-1")
	if !ok || stored == nil {
		t.Fatal("auth was not found after MarkResult")
	}
	state := stored.ModelStates[model]
	if state == nil {
		t.Fatalf("ModelStates = %+v, want an entry for %q", stored.ModelStates, model)
	}
	if !state.Quota.Exceeded || !state.NextRetryAfter.After(before.Add(40*time.Hour)) {
		t.Fatalf("state = %+v, want a quota cooldown until the ~48h upstream reset", state)
	}
	if remaining := state.Quota.NextRecoverAt.Sub(before); remaining < resetAfter-time.Minute {
		t.Fatalf("cooldown remaining = %v, want at least %v", remaining, resetAfter)
	}

	// The selector must skip the credential for that model even though cooling is
	// disabled, otherwise the same exhausted account is re-picked immediately.
	cooling := stored
	healthy := &Auth{ID: "antigravity-healthy", Provider: "antigravity", Status: StatusActive}
	healthy.ModelStates = map[string]*ModelState{model: {Status: StatusActive}}
	picked, errPick := (&RoundRobinSelector{}).Pick(context.Background(), "antigravity", model, cliproxyexecutor.Options{}, []*Auth{cooling, healthy})
	if errPick != nil {
		t.Fatalf("Pick() error = %v, want the healthy credential", errPick)
	}
	if picked.ID != healthy.ID {
		t.Fatalf("Pick() = %q, want %q", picked.ID, healthy.ID)
	}
}

// TestQuotaGroupCooledWhenCoolingDisabled verifies the whole quota group is cooled, not
// just the failing model, matching Antigravity's per-group metering.
func TestQuotaGroupCooledWhenCoolingDisabled(t *testing.T) {
	resetAfter := 47*time.Hour + 55*time.Minute + 2*time.Second

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{DisableCooling: true})
	t.Cleanup(func() { manager.SetConfig(&internalconfig.Config{}) })
	antigravityGroupAuth(t, manager, "antigravity-2")

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:             "antigravity-2",
		Provider:           "antigravity",
		Model:              "gemini-3.8-flash-high",
		RouteModel:         "gemini-3.8-flash-high",
		Success:            false,
		Error:              &Error{HTTPStatus: 429, Message: "Individual quota reached"},
		RetryAfter:         &resetAfter,
		QuotaGroup:         AntigravityQuotaGroupGemini,
		QuotaResetDeadline: true,
	})

	stored, _ := manager.GetByID("antigravity-2")
	if stored == nil {
		t.Fatal("auth was not found after MarkResult")
	}
	sibling := stored.ModelStates["gemini-3.1-pro-high"]
	if sibling == nil || !sibling.Quota.Exceeded {
		t.Fatalf("ModelStates[%q] = %+v, want the sibling Gemini model cooled too", "gemini-3.1-pro-high", sibling)
	}
	if remaining := sibling.NextRetryAfter.Sub(before); remaining < resetAfter-time.Minute {
		t.Fatalf("sibling cooldown remaining = %v, want at least %v", remaining, resetAfter)
	}
	// The other quota group must stay selectable.
	other := stored.ModelStates["claude-opus-5-5-high"]
	if other != nil && other.Quota.Exceeded {
		t.Fatalf("ModelStates[%q] = %+v, want the Claude/GPT group unaffected", "claude-opus-5-5-high", other)
	}
}

// TestSpeculativeRetryHintNotHonoredWhenCoolingDisabled pins the fix's scope: a plain
// retry hint under disable-cooling keeps the previous no-cooldown behavior, so the fix
// does not reintroduce blackout windows for ordinary rate limits.
func TestSpeculativeRetryHintNotHonoredWhenCoolingDisabled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{DisableCooling: true})
	t.Cleanup(func() { manager.SetConfig(&internalconfig.Config{}) })
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "antigravity-3", Provider: "antigravity", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	hint := 30 * time.Second
	manager.MarkResult(context.Background(), Result{
		AuthID:     "antigravity-3",
		Provider:   "antigravity",
		Model:      "gemini-3.8-flash-high",
		RouteModel: "gemini-3.8-flash-high",
		Success:    false,
		Error:      &Error{HTTPStatus: 429, Message: "rate limited"},
		RetryAfter: &hint,
	})

	stored, _ := manager.GetByID("antigravity-3")
	if stored == nil {
		t.Fatal("auth was not found after MarkResult")
	}
	if state := stored.ModelStates["gemini-3.8-flash-high"]; state != nil && !state.NextRetryAfter.IsZero() {
		t.Fatalf("state = %+v, want no cooldown for a speculative hint under disable-cooling", state)
	}
}

// TestApplyQuotaRetryHintClassifiesDeadline verifies the shared helper used by every
// conductor branch recognizes the deadline flag, so the non-stream Execute path, the
// stream bootstrap paths, and the count-tokens path all behave identically.
func TestApplyQuotaRetryHintClassifiesDeadline(t *testing.T) {
	resetAfter := 47 * time.Hour
	err := quotaDeadlineErr{group: AntigravityQuotaGroupGemini, after: resetAfter}

	var deadline Result
	applyQuotaRetryHint(&deadline, err)
	if !deadline.QuotaResetDeadline {
		t.Fatal("QuotaResetDeadline = false, want true for a provider reset deadline")
	}
	if deadline.RetryAfter == nil || *deadline.RetryAfter != resetAfter {
		t.Fatalf("RetryAfter = %v, want %v", deadline.RetryAfter, resetAfter)
	}
	if deadline.QuotaGroup != AntigravityQuotaGroupGemini {
		t.Fatalf("QuotaGroup = %q, want %q", deadline.QuotaGroup, AntigravityQuotaGroupGemini)
	}

	var hint Result
	applyQuotaRetryHint(&hint, quotaDeadlineErr{after: 30 * time.Second, hintOnly: true})
	if hint.QuotaResetDeadline {
		t.Fatal("QuotaResetDeadline = true, want false for a speculative retry hint")
	}
}
