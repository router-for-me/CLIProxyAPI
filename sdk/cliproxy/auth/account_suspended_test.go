package auth

import (
	"context"
	"encoding/json"
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

// suspendedResult is what the Antigravity executor's suspension error becomes:
// credential scoped, with its 24h retry.
func suspendedResult(authID, model string) Result {
	retry := 24 * time.Hour
	return Result{AuthID: authID, Provider: "antigravity", Model: model, CredentialScope: true, RetryAfter: &retry,
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

// endCredentialCooldown moves the credential-wide cooldown into the past, as if its 24h had run out.
func endCredentialCooldown(m *Manager, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	auth := m.auths[id]
	auth.Quota.NextRecoverAt = time.Now().Add(-time.Minute)
	auth.credQuotaForceUntil = time.Time{}
}

func TestManager_MarkResult_AccountSuspendedCoolsWholeCredential(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b", "model-c")
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", Success: true})
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))

	now := time.Now()
	auth, _ := m.GetByID(id)
	// model-c has no state of its own; the credential-wide cooldown still covers it.
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
	if auth.Quota.Reason != "credential_quota" {
		t.Fatalf("credential cooldown reason = %q, want the credential quota path", auth.Quota.Reason)
	}
	for _, message := range []string{auth.StatusMessage, auth.ModelStates["model-a"].StatusMessage} {
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
	// In-flight requests report the same suspension more than once, on any model.
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))
	m.MarkResult(context.Background(), suspendedResult(id, "model-b"))

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

func TestManager_MarkResult_AccountSuspensionKeepsLongerModelCooldowns(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b", "model-c")
	inFlight := m.nextExecutionSeq()
	long := 48 * time.Hour
	// model-a and model-b each have their own cooldown that outlives the suspension.
	for _, model := range []string{"model-a", "model-b"} {
		m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: model, RetryAfter: &long, ExecutionSeq: m.nextExecutionSeq(),
			Error: &Error{HTTPStatus: http.StatusNotFound, Message: "not found"}})
	}
	// A request sent to model-a before its own cooldown comes back with the 403.
	report := suspendedResult(id, "model-a")
	report.ExecutionSeq = inFlight
	m.MarkResult(context.Background(), report)

	// The 24h run out, then a request succeeds.
	endCredentialCooldown(m, id)
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-c", Success: true, ExecutionSeq: m.nextExecutionSeq()})

	now := time.Now()
	auth, _ := m.GetByID(id)
	if !IsAuthAvailableForModel(auth, "model-c", now.Add(time.Second)) {
		t.Fatal("model-c still blocked after the credential cooldown ended")
	}
	for _, model := range []string{"model-a", "model-b"} {
		if IsAuthAvailableForModel(auth, model, now.Add(30*time.Hour)) {
			t.Fatalf("%s lost its own 48h cooldown", model)
		}
		if !IsAuthAvailableForModel(auth, model, now.Add(49*time.Hour)) {
			t.Fatalf("%s still blocked after its own cooldown", model)
		}
	}
}

func TestManager_MarkResult_AccountSuspensionIgnoresSuccessWhileCooling(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b")
	staleSeq := m.nextExecutionSeq()
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))

	// A request sent before the 403, and one that finishes while the credential cools.
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", Success: true, ExecutionSeq: staleSeq})
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-a", Success: true, ExecutionSeq: m.nextExecutionSeq()})

	auth, _ := m.GetByID(id)
	for _, model := range []string{"model-a", "model-b", "a-model-it-never-served"} {
		if IsAuthAvailableForModel(auth, model, time.Now().Add(time.Hour)) {
			t.Fatalf("a success during the cooldown made %s selectable", model)
		}
	}
}

func TestManager_MarkResult_AccountSuspendedWithoutModel(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil)
	retry := 24 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", RetryAfter: &retry, CredentialScope: true,
		Error: &Error{Code: ErrorCodeAccountSuspended, HTTPStatus: http.StatusForbidden, Message: accountSuspendedBody}})

	now := time.Now()
	auth, _ := m.GetByID(id)
	if IsAuthAvailableForModel(auth, "model-a", now.Add(6*time.Hour)) || !IsAuthAvailableForModel(auth, "model-a", now.Add(25*time.Hour)) {
		t.Fatal("a suspension without a model did not cool the credential for 24h")
	}
	if auth.StatusMessage != "account suspended for a Terms of Service violation; retrying after 24h" {
		t.Fatalf("status message = %q, want the suspension, not quota exhausted", auth.StatusMessage)
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
	if entries := suspensionWarnings(hook); len(entries) != 1 || entries[0].Message != "account suspended for a Terms of Service violation; appeal: https://forms.gle/hGzM9MEUv2azZsrb9" {
		t.Fatalf("suspension log lines = %d, want 1 without a retry time", len(entries))
	}
}

func TestManager_RestoreCooldownStates_KeepsAccountSuspension(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b")
	long := 48 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", RetryAfter: &long,
		Error: &Error{HTTPStatus: http.StatusNotFound, Message: "not found"}})
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))

	// Save the records the way the file store does, then load them into a fresh manager.
	raw, errMarshal := json.Marshal(m.cooldownStateRecordsSnapshot())
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var records []CooldownStateRecord
	if errUnmarshal := json.Unmarshal(raw, &records); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	restarted := NewManager(nil, nil, nil)
	if _, errRegister := restarted.Register(context.Background(), &Auth{ID: id, Provider: "antigravity"}); errRegister != nil {
		t.Fatal(errRegister)
	}
	restarted.SetCooldownStateStore(&mockCooldownStateStore{records: records})
	if errRestore := restarted.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatal(errRestore)
	}
	reloaded, _ := restarted.GetByID(id)
	if IsAuthAvailableForModel(reloaded, "a-model-it-never-served", time.Now().Add(6*time.Hour)) {
		t.Fatal("the suspension did not come back with the saved cooldowns")
	}

	endCredentialCooldown(restarted, id)
	restarted.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-a", Success: true, ExecutionSeq: restarted.nextExecutionSeq()})
	auth, _ := restarted.GetByID(id)
	if IsAuthAvailableForModel(auth, "model-b", time.Now().Add(30*time.Hour)) {
		t.Fatal("after a restart model-b lost its own 48h cooldown")
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

func TestManager_MarkResult_AccountSuspendedWithoutScopeIsAnOrdinaryForbidden(t *testing.T) {
	m, id := newAccountSuspensionManager(t, nil)
	retry := 24 * time.Hour
	// Without the credential scope the executor sets, a suspension code alone cools nothing extra.
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", RetryAfter: &retry,
		Error: &Error{Code: ErrorCodeAccountSuspended, HTTPStatus: http.StatusForbidden, Message: accountSuspendedBody}})

	auth, _ := m.GetByID(id)
	if !IsAuthAvailableForModel(auth, "model-a", time.Now().Add(31*time.Minute)) {
		t.Fatal("an unscoped suspension cooled the credential past the ordinary 30 minutes")
	}
}

func TestManager_MarkResult_AccountSuspensionWarnsAgainAfterRecovery(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m, id := newAccountSuspensionManager(t, nil, "model-a", "model-b")
	m.MarkResult(context.Background(), suspendedResult(id, "model-a"))

	// The 24h end and a request on another model succeeds, so the account works again.
	endCredentialCooldown(m, id)
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: "model-b", Success: true, ExecutionSeq: m.nextExecutionSeq()})
	auth, _ := m.GetByID(id)
	if isAccountSuspendedResultError(auth.LastError) || isAccountSuspendedResultError(auth.ModelStates["model-a"].LastError) {
		t.Fatalf("the success left the suspension showing: credential %v, model-a %v", auth.LastError, auth.ModelStates["model-a"].LastError)
	}

	// Suspended again later: a new suspension, a new warning.
	later := suspendedResult(id, "model-b")
	later.ExecutionSeq = m.nextExecutionSeq()
	m.MarkResult(context.Background(), later)
	if entries := suspensionWarnings(hook); len(entries) != 2 {
		t.Fatalf("got %d suspension log lines, want 2", len(entries))
	}
}
