package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
)

const accountSuspendedBody = `{"error":{"code":403,"message":"This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudcode-pa.googleapis.com","metadata":{"uiMessage":"true","appeal_url":"https://forms.gle/hGzM9MEUv2azZsrb9","appeal_url_link_text":"Submit Appeal"}}]}}`

func newAccountSuspensionManager(t *testing.T, metadata map[string]any, models ...string) (*Manager, string) {
	t.Helper()
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	id := "ag-suspended-" + strings.ReplaceAll(t.Name(), "/", "-")
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	registry.GetGlobalRegistry().RegisterClient(id, "antigravity", infos)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, errRegister := m.Register(context.Background(), &Auth{ID: id, Provider: "antigravity", Metadata: metadata}); errRegister != nil {
		t.Fatal(errRegister)
	}
	return m, id
}

func suspendedResult(authID, model string) Result {
	return Result{AuthID: authID, Provider: "antigravity", Model: model,
		Error: &Error{Code: ErrorCodeAccountSuspended, HTTPStatus: http.StatusForbidden, Message: accountSuspendedBody}}
}

func suspensionWarnings(hook interface{ AllEntries() []*log.Entry }) []*log.Entry {
	var entries []*log.Entry
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "account suspended") {
			entries = append(entries, entry)
		}
	}
	return entries
}

func TestManager_MarkResult_AccountSuspendedCoolsWholeCredential(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b", "model-c")
	// model-b already has a state; model-c has none until it is requested.
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", Success: true})
	// In-flight requests can report the same suspension more than once.
	for i := 0; i < 3; i++ {
		m.MarkResult(context.Background(), suspendedResult(id, "model-a"))
	}

	now := time.Now()
	auth, _ := m.GetByID(id)
	for _, model := range []string{"model-a", "model-b", "model-c"} {
		for _, later := range []time.Duration{31 * time.Minute, 23 * time.Hour} {
			if IsAuthAvailableForModel(auth, model, now.Add(later)) {
				t.Fatalf("%s selectable %v after the suspension", model, later)
			}
		}
		if !IsAuthAvailableForModel(auth, model, now.Add(25*time.Hour)) {
			t.Fatalf("%s still cooling after 25h", model)
		}
	}
	if !isCredentialBlocked(auth, 3, now.Add(time.Hour)) {
		t.Fatal("scheduler does not see the credential as blocked")
	}
	for _, message := range []string{auth.StatusMessage, auth.ModelStates["model-a"].StatusMessage, auth.ModelStates["model-b"].StatusMessage} {
		if message != "account suspended for a Terms of Service violation; retrying after 24h" {
			t.Fatalf("status message = %q", message)
		}
	}
}

func TestManager_MarkResult_PlainForbiddenStillCoolsThirtyMinutes(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b")
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-a",
		Error: &Error{HTTPStatus: http.StatusForbidden, Message: `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`}})

	now := time.Now()
	auth, _ := m.GetByID(id)
	if IsAuthAvailableForModel(auth, "model-a", now.Add(time.Minute)) || !IsAuthAvailableForModel(auth, "model-a", now.Add(31*time.Minute)) {
		t.Fatal("ordinary 403 no longer cools the model for 30 minutes")
	}
	if !IsAuthAvailableForModel(auth, "model-b", now.Add(time.Minute)) {
		t.Fatal("ordinary 403 now cools sibling models")
	}
}

func TestManager_MarkResult_AccountSuspendedLogsOnceWithAppealURL(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b")
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity",
		Error: &Error{Code: ErrorCodeAccountSuspended, HTTPStatus: http.StatusForbidden, Message: accountSuspendedBody}})

	entries := suspensionWarnings(hook)
	if len(entries) != 1 {
		t.Fatalf("got %d suspension log lines, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Level != log.WarnLevel || entry.Data["auth_id"] != id || entry.Data["provider"] != "antigravity" ||
		entry.Message != "account suspended for a Terms of Service violation; retrying after 24h; appeal: https://forms.gle/hGzM9MEUv2azZsrb9" {
		t.Fatalf("log entry = %s %q %v", entry.Level, entry.Message, entry.Data)
	}
}

func TestManager_MarkResult_AccountSuspendedWithCoolingDisabled(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m, id := newAccountSuspensionManager(t, map[string]any{"disable_cooling": true}, "model-a", "model-b")
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))

	auth, _ := m.GetByID(id)
	for _, model := range []string{"model-a", "model-b"} {
		if !IsAuthAvailableForModel(auth, model, time.Now().Add(time.Second)) {
			t.Fatalf("%s blocked with cooling disabled", model)
		}
	}
	if auth.StatusMessage != "account suspended for a Terms of Service violation" {
		t.Fatalf("status message = %q, want no retry time with cooling disabled", auth.StatusMessage)
	}
	if entries := suspensionWarnings(hook); len(entries) != 1 {
		t.Fatalf("got %d suspension log lines, want 1", len(entries))
	}
}

func TestManager_MarkResult_SuccessClearsAccountSuspension(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b")
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", Success: true})
	staleSeq := m.nextExecutionSeq()
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))

	// A success dispatched before the suspension proves nothing.
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", Success: true, ExecutionSeq: staleSeq})
	auth, _ := m.GetByID(id)
	if IsAuthAvailableForModel(auth, "model-a", time.Now().Add(time.Hour)) {
		t.Fatal("stale success cleared the suspension")
	}

	// A later success, after an appeal, frees every model at once.
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", Success: true, ExecutionSeq: m.nextExecutionSeq()})
	auth, _ = m.GetByID(id)
	for _, model := range []string{"model-a", "model-b"} {
		if !IsAuthAvailableForModel(auth, model, time.Now().Add(time.Second)) {
			t.Fatalf("%s still blocked after a success", model)
		}
		if state := auth.ModelStates[model]; state.LastError != nil || state.StatusMessage != "" {
			t.Fatalf("%s state not cleared: %+v", model, state)
		}
	}
	if auth.LastError != nil || auth.StatusMessage != "" || auth.Status != StatusActive {
		t.Fatalf("credential not cleared: status %s, message %q, error %v", auth.Status, auth.StatusMessage, auth.LastError)
	}
}

func TestManager_MarkResult_AccountSuspendedWithoutModel(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b")
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", Success: true})
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity",
		Error: &Error{Code: ErrorCodeAccountSuspended, HTTPStatus: http.StatusForbidden, Message: accountSuspendedBody}})

	now := time.Now()
	auth, _ := m.GetByID(id)
	if auth.StatusMessage != "account suspended for a Terms of Service violation; retrying after 24h" {
		t.Fatalf("status message = %q, want the suspension, not payment_required", auth.StatusMessage)
	}
	if auth.NextRetryAfter.Before(now.Add(23 * time.Hour)) {
		t.Fatalf("credential retries after %v, want 24h", auth.NextRetryAfter.Sub(now))
	}
	for _, model := range []string{"model-a", "model-b"} {
		if IsAuthAvailableForModel(auth, model, now.Add(6*time.Hour)) {
			t.Fatalf("%s selectable 6h after the suspension", model)
		}
	}
}

func TestAccountSuspendedAppealURL(t *testing.T) {
	const okURL = "https://forms.gle/hGzM9MEUv2azZsrb9"
	for raw, want := range map[string]string{
		okURL:                                  okURL,
		"https://support.google.com/appeal":    "https://support.google.com/appeal",
		"https://google.com/appeal":            "https://google.com/appeal",
		"http://forms.gle/hGzM9MEUv2azZsrb9":   "",
		"https://forms.gle.example.com/appeal": "",
		"https://forms.gle@evil.com/appeal":    "",
		"https://notgoogle.com/appeal":         "",
		"https://forms.gle:8443/appeal":        "",
	} {
		body := strings.Replace(accountSuspendedBody, okURL, raw, 1)
		if got := accountSuspendedAppealURL(body); got != want {
			t.Fatalf("accountSuspendedAppealURL(%q) = %q, want %q", raw, got, want)
		}
	}

	twoDetails := `{"error":{"details":[{"metadata":{"appeal_url":"http://evil.example/appeal"}},{"metadata":{"appeal_url":"` + okURL + `"}}]}}`
	if got := accountSuspendedAppealURL(twoDetails); got != okURL {
		t.Fatalf("accountSuspendedAppealURL() = %q, want the valid link from the second detail", got)
	}
}

func TestManager_MarkResult_StaleSuccessOnNewModelKeepsAccountSuspension(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b", "model-c")
	staleSeq := m.nextExecutionSeq()
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))

	// model-c has no state yet: its success was dispatched before the suspension.
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-c", Success: true, ExecutionSeq: staleSeq})

	auth, _ := m.GetByID(id)
	for _, model := range []string{"model-a", "model-b", "model-c"} {
		if IsAuthAvailableForModel(auth, model, time.Now().Add(time.Hour)) {
			t.Fatalf("%s selectable after a stale success", model)
		}
	}
	if !isAccountSuspendedResultError(auth.LastError) {
		t.Fatalf("credential error = %v, want the suspension", auth.LastError)
	}
}

func TestManager_MarkResult_StaleFailureKeepsAccountSuspension(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b", "model-c")
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", Success: true})
	staleSeq := m.nextExecutionSeq()
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))

	// A 503 on model-b from a request sent before the suspension.
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", ExecutionSeq: staleSeq,
		Error: &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "overloaded"}})

	auth, _ := m.GetByID(id)
	if IsAuthAvailableForModel(auth, "model-c", time.Now().Add(time.Hour)) {
		t.Fatal("never-served model selectable after a stale failure")
	}
	if !isAccountSuspendedResultError(auth.LastError) || !isAccountSuspendedResultError(auth.ModelStates["model-b"].LastError) {
		t.Fatalf("stale failure replaced the suspension: credential %v, model-b %v", auth.LastError, auth.ModelStates["model-b"].LastError)
	}

	// After an appeal, one success frees every model, model-b included.
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-a", Success: true, ExecutionSeq: m.nextExecutionSeq()})
	auth, _ = m.GetByID(id)
	for _, model := range []string{"model-a", "model-b", "model-c"} {
		if !IsAuthAvailableForModel(auth, model, time.Now().Add(time.Second)) {
			t.Fatalf("%s still blocked after a success", model)
		}
	}
}

func TestManager_AccountSuspensionClearedByDisableAndEnable(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b")
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))

	for _, disabled := range []bool{true, false} {
		current, _ := m.GetByID(id)
		if _, committed, errModify := m.Modify(context.Background(), id, current.RegistrationEpoch, func(auth *Auth) error {
			auth.Disabled = disabled
			auth.Status = StatusActive
			if disabled {
				auth.Status = StatusDisabled
			}
			return nil
		}); errModify != nil || !committed {
			t.Fatalf("Modify(disabled=%v) = %v, committed %v", disabled, errModify, committed)
		}
	}

	auth, _ := m.GetByID(id)
	for _, model := range []string{"model-a", "model-b", "model-c"} {
		if !IsAuthAvailableForModel(auth, model, time.Now().Add(time.Second)) {
			t.Fatalf("%s still cooling after the credential was disabled and enabled again", model)
		}
	}
}
