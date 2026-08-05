package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestAuthMutationLocksEvictedAfterCredentialChurn(t *testing.T) {
	m := NewManager(nil, nil, nil)

	for i := 0; i < 128; i++ {
		auth := &Auth{
			ID:       fmt.Sprintf("churn-auth-%d", i),
			Provider: "openai-compatibility",
			Status:   StatusActive,
		}
		if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
			t.Fatalf("register auth %d: %v", i, err)
		}
		m.Remove(context.Background(), auth.ID)
	}

	lockCount := 0
	m.authMutationLocks.Range(func(_, _ any) bool {
		lockCount++
		return true
	})
	if lockCount != 0 {
		t.Fatalf("auth mutation lock count after credential churn = %d, want 0", lockCount)
	}
}

func TestPaymentRequiredAction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  *internalconfig.Config
		want string
	}{
		{name: "nil config", want: "cooldown"},
		{name: "empty config", cfg: &internalconfig.Config{}, want: "cooldown"},
		{name: "disable normalized", cfg: &internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: " Disable "}}, want: "disable"},
		{name: "cooldown normalized", cfg: &internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "COOLDOWN"}}, want: "cooldown"},
		{name: "unknown is safe default", cfg: &internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "unknown"}}, want: "cooldown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := paymentRequiredAction(tt.cfg); got != tt.want {
				t.Fatalf("paymentRequiredAction() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyPaymentRequiredModelFailureDisable(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	auth := &Auth{ID: "k1", Status: StatusActive}
	state := &ModelState{Status: StatusActive}
	errResult := &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"}

	suspend := applyPaymentRequiredModelFailure(auth, state, now, errResult, false, "disable")
	if suspend {
		t.Fatal("shouldSuspendModel = true, want false for auth-level disable")
	}
	if !auth.Disabled || auth.Status != StatusDisabled || auth.Unavailable {
		t.Fatalf("auth state = disabled:%v unavailable:%v status:%s", auth.Disabled, auth.Unavailable, auth.Status)
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
	if state.Status != StatusActive || state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("model state changed by auth-level disable: status:%s unavailable:%v next:%v", state.Status, state.Unavailable, state.NextRetryAfter)
	}
	if got, _ := auth.Metadata["disabled"].(bool); !got {
		t.Fatalf("metadata disabled = %#v, want true", auth.Metadata["disabled"])
	}
	if got, _ := auth.Metadata["disabled_reason"].(string); got != "payment_required" {
		t.Fatalf("metadata disabled_reason = %q, want payment_required", got)
	}
	if auth.StatusMessage != "insufficient balance" {
		t.Fatalf("auth StatusMessage = %q, want upstream detail", auth.StatusMessage)
	}
}

func TestApplyPaymentRequiredModelFailureCooldown(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	auth := &Auth{ID: "k1", Status: StatusActive}
	state := &ModelState{Status: StatusActive}
	errResult := &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"}

	suspend := applyPaymentRequiredModelFailure(auth, state, now, errResult, false, "cooldown")
	if !suspend {
		t.Fatal("shouldSuspendModel = false, want true for cooldown")
	}
	if auth.Disabled || auth.Status != StatusError {
		t.Fatalf("auth state = disabled:%v status:%s", auth.Disabled, auth.Status)
	}
	if state.NextRetryAfter.Before(now.Add(29*time.Minute)) || state.NextRetryAfter.After(now.Add(31*time.Minute)) {
		t.Fatalf("model cooldown = %v, want about 30m", state.NextRetryAfter.Sub(now))
	}
	if auth.StatusMessage != "insufficient balance" {
		t.Fatalf("auth StatusMessage = %q, want upstream detail", auth.StatusMessage)
	}
	if state.StatusMessage != "insufficient balance" {
		t.Fatalf("model StatusMessage = %q, want upstream detail", state.StatusMessage)
	}
}

func TestPaymentRequiredDisableResumesPriorModelSuspension(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "cooldown"},
	})
	const model = "payment-required-mode-transition-model"
	auth := &Auth{ID: "payment-mode-transition-key", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if got := reg.GetModelCount(model); got != 1 {
		t.Fatalf("initial registry model count = %d, want 1", got)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: model,
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "cooldown payment failure"},
	})
	if got := reg.GetModelCount(model); got != 0 {
		t.Fatalf("registry model count after cooldown 402 = %d, want 0", got)
	}

	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: model,
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "disable payment failure"},
	})
	if got := reg.GetModelCount(model); got != 1 {
		t.Fatalf("registry model count after auth-level disable = %d, want stale suspension cleared", got)
	}
	current, _ := m.GetByID(auth.ID)
	if current == nil || !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("auth after disable 402 = %#v", current)
	}
}

func TestPaymentRequiredDisablePreservesPriorQuotaMarker(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	const model = "payment-required-prior-quota-model"
	auth := &Auth{ID: "payment-prior-quota-key", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: model,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
	})
	if got := reg.GetModelCount(model); got != 0 {
		t.Fatalf("registry model count after 429 = %d, want 0", got)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: model,
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})
	if got := reg.GetModelCount(model); got != 0 {
		t.Fatalf("registry model count after 402 disable = %d, want independent quota marker preserved", got)
	}
	current, _ := m.GetByID(auth.ID)
	state := current.ModelStates[model]
	if state == nil || !state.Quota.Exceeded || state.LastError == nil || state.LastError.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("independent quota state changed by 402 disable: %#v", state)
	}
}

func TestPaymentRequiredDisabledMetadataUpdatePreservesIndependentQuotaCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	const quotaModel = "payment-disabled-metadata-quota-model"
	const paymentModel = "payment-disabled-metadata-payment-model"
	auth := &Auth{
		ID:       "payment-disabled-metadata-key",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Metadata: map[string]any{"note": "before"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: quotaModel}, {ID: paymentModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: quotaModel,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
	})
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: paymentModel,
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})

	disabled, _ := m.GetByID(auth.ID)
	if disabled == nil || !isPaymentRequiredDisabled(disabled) {
		t.Fatalf("auth was not payment-disabled: %#v", disabled)
	}
	metadataUpdate := disabled.Clone()
	metadataUpdate.Metadata["note"] = "after"
	if _, err := m.Update(context.Background(), metadataUpdate); err != nil {
		t.Fatalf("metadata update: %v", err)
	}

	afterUpdate, _ := m.GetByID(auth.ID)
	quotaState := afterUpdate.ModelStates[quotaModel]
	if quotaState == nil || !quotaState.Quota.Exceeded || quotaState.LastError == nil || quotaState.LastError.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("metadata update cleared independent quota state: %#v", quotaState)
	}
	if got := afterUpdate.Metadata["note"]; got != "after" {
		t.Fatalf("metadata note = %#v, want after", got)
	}

	reenabled := afterUpdate.Clone()
	reenabled.Disabled = false
	reenabled.Status = StatusActive
	reenabled.StatusMessage = ""
	reenabled.Metadata["disabled"] = false
	delete(reenabled.Metadata, "disabled_reason")
	if _, err := m.Update(context.Background(), reenabled); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	current, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(current, quotaModel, time.Now()); !blocked {
		t.Fatal("independent quota cooldown no longer blocks selection after re-enable")
	}
	if got := reg.GetModelCount(quotaModel); got != 0 {
		t.Fatalf("registry model count after re-enable = %d, want 0 for retained quota", got)
	}
}

func TestPaymentRequiredDisableClearsPriorSuspensionsAcrossModels(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "cooldown"},
	})
	const modelA = "payment-required-model-a"
	const modelB = "payment-required-model-b"
	auth := &Auth{ID: "payment-multi-model-key", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelA}, {ID: modelB}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: modelA,
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "model A payment failure"},
	})
	if got := reg.GetModelCount(modelA); got != 0 {
		t.Fatalf("model A count after cooldown 402 = %d, want suspended", got)
	}
	if got := reg.GetModelCount(modelB); got != 1 {
		t.Fatalf("model B count before disable transition = %d, want 1", got)
	}

	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: modelB,
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "model B payment failure"},
	})

	for _, model := range []string{modelA, modelB} {
		if got := reg.GetModelCount(model); got != 1 {
			t.Fatalf("model %s count after auth-level disable = %d, want prior payment suspension cleared", model, got)
		}
	}
	current, _ := m.GetByID(auth.ID)
	if current == nil || !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("auth after disable 402 = %#v", current)
	}
	stateA := current.ModelStates[modelA]
	if stateA == nil || !stateA.NextRetryAfter.IsZero() {
		t.Fatalf("model A payment cooldown remained after auth-level disable: %#v", stateA)
	}
}

func TestMarkResultPaymentRequiredDisableStopsAuthAndKeepsModelReenableSafe(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})

	const paymentModel = "gpt-payment"
	const quotaModel = "gpt-quota"
	auth := &Auth{ID: "pay-key", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: paymentModel}, {ID: quotaModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	stop := m.markResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    paymentModel,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})
	if !stop {
		t.Fatal("stopAuth = false, want true")
	}

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth missing")
	}
	if !updated.Disabled || updated.Status != StatusDisabled {
		t.Fatalf("auth not disabled: disabled=%v status=%s", updated.Disabled, updated.Status)
	}

	retryAt := time.Now().Add(time.Hour)
	updated.ModelStates[paymentModel] = &ModelState{
		Status:         StatusError,
		StatusMessage:  "insufficient balance",
		Unavailable:    true,
		NextRetryAfter: retryAt,
		LastError:      &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	}
	updated.ModelStates[quotaModel] = &ModelState{
		Status:         StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: retryAt,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: retryAt,
		},
		LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
	}
	m.mu.Lock()
	m.auths[auth.ID] = updated.Clone()
	m.mu.Unlock()
	reg.SuspendClientModel(auth.ID, paymentModel, "payment_required")
	reg.SetModelQuotaExceeded(auth.ID, quotaModel)
	reg.SuspendClientModel(auth.ID, quotaModel, "quota")
	seeded, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(seeded, paymentModel, time.Now()); !blocked {
		t.Fatal("stale payment model state did not reproduce the selection block")
	}
	if got := reg.GetModelCount(paymentModel); got != 0 {
		t.Fatalf("payment model count before management re-enable = %d, want 0", got)
	}

	// File persistence of the payment disable is echoed through the watcher as
	// an update without runtime-only model state. It must not erase an
	// independent quota cooldown before management re-enables the credential.
	watcherEcho := &Auth{
		ID:       auth.ID,
		Provider: auth.Provider,
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"disabled":        true,
			"disabled_reason": "payment_required",
		},
	}
	if _, err := m.Update(WithSkipPersist(context.Background()), watcherEcho); err != nil {
		t.Fatalf("watcher echo update: %v", err)
	}
	afterEcho, _ := m.GetByID(auth.ID)
	echoQuotaState := afterEcho.ModelStates[quotaModel]
	if echoQuotaState == nil || !echoQuotaState.Quota.Exceeded || echoQuotaState.LastError == nil || echoQuotaState.LastError.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("watcher echo replaced independent quota state: %#v", echoQuotaState)
	}
	if blocked, _, _ := isAuthBlockedForModel(afterEcho, quotaModel, time.Now()); !blocked {
		t.Fatal("watcher echo cleared independent quota selection block")
	}

	// Exercise the same authoritative update used by management re-enable.
	reenabled := afterEcho.Clone()
	reenabled.Disabled = false
	reenabled.Status = StatusActive
	reenabled.StatusMessage = ""
	reenabled.Metadata["disabled"] = false
	delete(reenabled.Metadata, "disabled_reason")
	if _, err := m.Update(context.Background(), reenabled); err != nil {
		t.Fatalf("management re-enable update: %v", err)
	}

	current, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(current, paymentModel, time.Now()); blocked {
		t.Fatal("stale payment model state blocks auth after management re-enable")
	}
	if got := reg.GetModelCount(paymentModel); got != 1 {
		t.Fatalf("payment model count after management re-enable = %d, want 1", got)
	}
	if blocked, _, _ := isAuthBlockedForModel(current, quotaModel, time.Now()); !blocked {
		t.Fatal("independent quota state was cleared by management re-enable")
	}
	if got := reg.GetModelCount(quotaModel); got != 0 {
		t.Fatalf("quota model count after management re-enable = %d, want 0", got)
	}
	quotaState := current.ModelStates[quotaModel]
	if quotaState == nil || !quotaState.Quota.Exceeded || quotaState.LastError == nil || quotaState.LastError.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("independent quota state changed: %#v", quotaState)
	}
}

func TestMarkResultForbiddenRemainsCooldownWhenPaymentRequiredDisableEnabled(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	auth := &Auth{ID: "forbidden-key", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	stop := m.markResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-test",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"},
	})
	if stop {
		t.Fatal("stopAuth = true for 403, want false")
	}
	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth missing")
	}
	if updated.Disabled || updated.Status == StatusDisabled {
		t.Fatalf("403 disabled auth: disabled=%v status=%s", updated.Disabled, updated.Status)
	}
	state := updated.ModelStates["gpt-test"]
	if state == nil || state.NextRetryAfter.Before(time.Now().Add(29*time.Minute)) {
		t.Fatalf("403 model cooldown missing: %#v", state)
	}
}

func TestPaymentRequiredDisableSurvivesCredentialMaintenanceAndAllowsAuthoritativeReenable(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	auth := &Auth{
		ID:       "maintenance-update-key",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Metadata: map[string]any{"disabled": false},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	staleMaintenance, _ := m.GetByID(auth.ID)

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "gpt-test",
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})
	if _, err := m.Update(withCredentialMaintenanceUpdate(context.Background(), staleMaintenance), staleMaintenance); err != nil {
		t.Fatalf("maintenance update: %v", err)
	}
	current, _ := m.GetByID(auth.ID)
	if current == nil || !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("maintenance update re-enabled auth: %#v", current)
	}

	// Config reloads and management changes are authoritative and do not carry
	// the maintenance context marker.
	authoritative := &Auth{
		ID:       auth.ID,
		Provider: auth.Provider,
		Status:   StatusActive,
		Metadata: map[string]any{"disabled": false},
	}
	if _, err := m.Update(context.Background(), authoritative); err != nil {
		t.Fatalf("authoritative re-enable: %v", err)
	}
	current, _ = m.GetByID(auth.ID)
	if current == nil || current.Disabled || current.Status != StatusActive {
		t.Fatalf("authoritative re-enable was not applied: %#v", current)
	}

	// A maintenance clone started after re-enable but before a later 402 must
	// still be rejected when that second disable wins the race.
	secondStaleMaintenance := current.Clone()
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "gpt-test",
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance again"},
	})
	if _, err := m.Update(withCredentialMaintenanceUpdate(context.Background(), secondStaleMaintenance), secondStaleMaintenance); err != nil {
		t.Fatalf("second maintenance update: %v", err)
	}
	current, _ = m.GetByID(auth.ID)
	if current == nil || !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("second maintenance update re-enabled auth: %#v", current)
	}
}

func TestCredentialMaintenancePreservesRuntimeStateAcrossPaymentDisableReenableCycle(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	auth := &Auth{
		ID:       "maintenance-disable-reenable-cycle",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Metadata: map[string]any{"disabled": false, "token": "old"},
		ModelStates: map[string]*ModelState{
			"baseline-model": {Status: StatusActive},
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	staleMaintenance, _ := m.GetByID(auth.ID)
	staleMaintenance.Metadata["token"] = "refreshed"

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "baseline-model",
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})
	reenabled, _ := m.GetByID(auth.ID)
	reenabled.Disabled = false
	reenabled.Unavailable = false
	reenabled.Status = StatusActive
	reenabled.StatusMessage = ""
	reenabled.LastError = nil
	reenabled.NextRetryAfter = time.Time{}
	reenabled.Metadata["disabled"] = false
	delete(reenabled.Metadata, "disabled_reason")
	if _, err := m.Update(context.Background(), reenabled); err != nil {
		t.Fatalf("authoritative re-enable: %v", err)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "newer-model",
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "newer quota state"},
	})
	beforeMaintenance, _ := m.GetByID(auth.ID)
	newerState := beforeMaintenance.ModelStates["newer-model"]
	if newerState == nil || newerState.NextRetryAfter.IsZero() {
		t.Fatalf("newer runtime state missing before maintenance: %#v", beforeMaintenance.ModelStates)
	}

	if _, err := m.UpdateCredentialMaintenance(context.Background(), staleMaintenance, staleMaintenance); err != nil {
		t.Fatalf("maintenance update: %v", err)
	}
	current, _ := m.GetByID(auth.ID)
	if current == nil {
		t.Fatal("auth missing after maintenance")
	}
	if current.Metadata["token"] != "refreshed" {
		t.Fatalf("credential refresh was lost: metadata=%#v", current.Metadata)
	}
	state := current.ModelStates["newer-model"]
	if state == nil || state.NextRetryAfter.IsZero() || state.StatusMessage != "newer quota state" {
		t.Fatalf("maintenance replaced newer runtime state: %#v", current.ModelStates)
	}
}

func TestPaymentRequiredDisableFallsBackToCooldownForPluginVirtualAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	auth := &Auth{ID: "virtual-key", Provider: "plugin-provider", Status: StatusActive}
	MarkPluginVirtualAuth(auth, "/tmp/plugin-source.json", 0)
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	stop := m.markResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "model-a",
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})
	if stop {
		t.Fatal("stopAuth = true, want cooldown fallback for plugin virtual auth")
	}
	current, _ := m.GetByID(auth.ID)
	if current == nil {
		t.Fatal("auth missing")
	}
	if current.Disabled || current.Status == StatusDisabled {
		t.Fatalf("plugin virtual auth was disabled: %#v", current)
	}
	state := current.ModelStates["model-a"]
	if state == nil || state.NextRetryAfter.Before(time.Now().Add(29*time.Minute)) {
		t.Fatalf("plugin virtual cooldown missing: %#v", state)
	}
}

func TestAntigravityCreditsStopsModelPoolAfterPaymentDisable(t *testing.T) {
	const alias = "claude-payment-pool"
	models := []internalconfig.OpenAICompatibilityModel{
		{Name: "model-a", Alias: alias},
		{Name: "model-b", Alias: alias},
	}
	payErr := &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"}
	executor := &openAICompatPoolExecutor{
		id:            openAICompatPoolProviderKey,
		executeErrors: map[string]error{"model-a": payErr},
	}
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{
			AntigravityCredits: true,
			OnPaymentRequired:  "disable",
		},
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "pool", Models: models,
		}},
	})
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "antigravity-payment-pool-key",
		Provider: "antigravity",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"compat_name":  "pool",
			"provider_key": "pool",
		},
	}
	if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, ok, err := m.tryAntigravityCreditsExecute(
		context.Background(),
		cliproxyexecutor.Request{Model: alias},
		cliproxyexecutor.Options{},
	)
	if err != nil {
		t.Fatalf("credits execute: %v", err)
	}
	if ok {
		t.Fatal("credits execute ok = true, want false after 402")
	}
	if got := executor.ExecuteModels(); len(got) != 1 || got[0] != "model-a" {
		t.Fatalf("credits models = %v, want only model-a after disable", got)
	}
	current, found := m.GetByID(auth.ID)
	if !found || current == nil || !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("auth after credits 402 = %#v, want disabled", current)
	}
}

func TestPaymentRequiredDisableSurvivesConcurrentSuccess(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	auth := &Auth{ID: "concurrent-success-key", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "model-a",
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "model-b", Success: true,
	})

	current, _ := m.GetByID(auth.ID)
	if current == nil || !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("concurrent success cleared payment disable: %#v", current)
	}
}

type paymentRequiredBlockingRefreshExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *paymentRequiredBlockingRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-token"
	return auth, nil
}

func TestPaymentRequiredDisabledRefreshPreservesNewerModelCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	executor := &paymentRequiredBlockingRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "refresh-disable-cooldown-key",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "old-token"},
	}
	if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, err := m.refreshAuthForRequest(context.Background(), auth.ID, "")
		refreshDone <- err
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}

	const cooldownModel = "newer-disabled-cooldown-model"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: cooldownModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "payment-model",
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: cooldownModel,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "newer quota failure"},
	})
	beforeRefresh, _ := m.GetByID(auth.ID)
	beforeState := beforeRefresh.ModelStates[cooldownModel]
	if beforeState == nil || !beforeState.Unavailable || beforeState.NextRetryAfter.IsZero() {
		t.Fatalf("newer cooldown was not established: %#v", beforeState)
	}

	close(executor.release)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("refresh: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}

	current, _ := m.GetByID(auth.ID)
	if current == nil || !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("refresh changed payment-disabled state: %#v", current)
	}
	afterState := current.ModelStates[cooldownModel]
	if afterState == nil || !afterState.Unavailable || !afterState.NextRetryAfter.Equal(beforeState.NextRetryAfter) {
		t.Fatalf("newer model cooldown was overwritten: before=%#v after=%#v", beforeState, afterState)
	}
	if current.Quota != beforeRefresh.Quota {
		t.Fatalf("newer auth quota was overwritten: before=%#v after=%#v", beforeRefresh.Quota, current.Quota)
	}
	if current.LastError == nil || current.LastError.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("newer auth error was overwritten: %#v", current.LastError)
	}
}

func TestPaymentRequiredAuthoritativeReenableWinsInFlightDisabledRefresh(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	executor := &paymentRequiredBlockingRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "refresh-reenable-key",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "old-token"},
	}
	if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "gpt-test",
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})

	refreshDone := make(chan error, 1)
	go func() {
		_, err := m.refreshAuthForRequest(context.Background(), auth.ID, "")
		refreshDone <- err
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}

	current, _ := m.GetByID(auth.ID)
	if current == nil || !current.Disabled {
		t.Fatalf("refresh source auth = %#v, want disabled", current)
	}
	reenabled := current.Clone()
	reenabled.Disabled = false
	reenabled.Unavailable = false
	reenabled.Status = StatusActive
	reenabled.StatusMessage = ""
	reenabled.Metadata["disabled"] = false
	if _, err := m.Update(context.Background(), reenabled); err != nil {
		t.Fatalf("authoritative re-enable: %v", err)
	}
	close(executor.release)

	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("refresh: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}
	current, _ = m.GetByID(auth.ID)
	if current == nil || current.Disabled || current.Status != StatusActive {
		t.Fatalf("in-flight disabled refresh overrode re-enable: %#v", current)
	}
	if got, _ := current.Metadata["access_token"].(string); got != "refreshed-token" {
		t.Fatalf("refreshed token = %q, want refreshed-token", got)
	}
	if _, exists := current.Metadata["disabled_reason"]; exists {
		t.Fatalf("disabled_reason persisted after authoritative re-enable: %#v", current.Metadata["disabled_reason"])
	}
}

func TestPaymentRequiredReenabledRefreshPreservesNewerModelCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	executor := &paymentRequiredBlockingRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "refresh-reenable-cooldown-key",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "old-token"},
	}
	if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "payment-model",
		Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})

	refreshDone := make(chan error, 1)
	go func() {
		_, err := m.refreshAuthForRequest(context.Background(), auth.ID, "")
		refreshDone <- err
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}

	current, _ := m.GetByID(auth.ID)
	reenabled := current.Clone()
	reenabled.Disabled = false
	reenabled.Unavailable = false
	reenabled.Status = StatusActive
	reenabled.StatusMessage = ""
	reenabled.Metadata["disabled"] = false
	if _, err := m.Update(context.Background(), reenabled); err != nil {
		t.Fatalf("authoritative re-enable: %v", err)
	}

	const cooldownModel = "newer-cooldown-model"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: cooldownModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: cooldownModel,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "newer quota failure"},
	})
	beforeRefresh, _ := m.GetByID(auth.ID)
	beforeState := beforeRefresh.ModelStates[cooldownModel]
	if beforeState == nil || !beforeState.Unavailable || beforeState.NextRetryAfter.IsZero() {
		t.Fatalf("newer cooldown was not established: %#v", beforeState)
	}
	if got := reg.GetModelCount(cooldownModel); got != 0 {
		t.Fatalf("registry model count during cooldown = %d, want 0", got)
	}

	close(executor.release)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("refresh: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}

	current, _ = m.GetByID(auth.ID)
	if current == nil || current.Disabled {
		t.Fatalf("refresh changed authoritative re-enable state: %#v", current)
	}
	if got, _ := current.Metadata["access_token"].(string); got != "refreshed-token" {
		t.Fatalf("refreshed token = %q, want refreshed-token", got)
	}
	afterState := current.ModelStates[cooldownModel]
	if afterState == nil || !afterState.Unavailable || !afterState.NextRetryAfter.Equal(beforeState.NextRetryAfter) {
		t.Fatalf("newer model cooldown was overwritten: before=%#v after=%#v", beforeState, afterState)
	}
	if got := reg.GetModelCount(cooldownModel); got != 0 {
		t.Fatalf("registry model count after refresh = %d, want 0", got)
	}
	if current.Quota != beforeRefresh.Quota {
		t.Fatalf("newer auth quota was overwritten: before=%#v after=%#v", beforeRefresh.Quota, current.Quota)
	}
	if current.LastError == nil || current.LastError.HTTPStatus != http.StatusTooManyRequests || current.LastError.Message != "newer quota failure" {
		t.Fatalf("newer auth error was overwritten: %#v", current.LastError)
	}
}

type paymentRequiredRaceStore struct {
	activeSaveStarted chan struct{}
	releaseActiveSave chan struct{}
	disabledSaved     chan struct{}
	activeOnce        sync.Once
	disabledOnce      sync.Once
	mu                sync.Mutex
	saved             []*Auth
}

func newPaymentRequiredRaceStore() *paymentRequiredRaceStore {
	return &paymentRequiredRaceStore{
		activeSaveStarted: make(chan struct{}),
		releaseActiveSave: make(chan struct{}),
		disabledSaved:     make(chan struct{}),
	}
}

func (s *paymentRequiredRaceStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *paymentRequiredRaceStore) Save(ctx context.Context, auth *Auth) (string, error) {
	snapshot := auth.Clone()
	if snapshot != nil && !snapshot.Disabled {
		s.activeOnce.Do(func() { close(s.activeSaveStarted) })
		select {
		case <-s.releaseActiveSave:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	s.mu.Lock()
	s.saved = append(s.saved, snapshot)
	s.mu.Unlock()
	if snapshot != nil && snapshot.Disabled {
		s.disabledOnce.Do(func() { close(s.disabledSaved) })
	}
	return "", nil
}

func (s *paymentRequiredRaceStore) Delete(context.Context, string) error { return nil }

func (s *paymentRequiredRaceStore) lastSaved() *Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.saved) == 0 {
		return nil
	}
	return s.saved[len(s.saved)-1].Clone()
}

func TestPaymentRequiredDisableWinsInFlightRegistrationPersistence(t *testing.T) {
	store := newPaymentRequiredRaceStore()
	m := NewManager(store, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	auth := &Auth{
		ID:       "registration-race-key",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Metadata: map[string]any{"token": "initial"},
	}

	registerDone := make(chan error, 1)
	go func() {
		_, err := m.Register(context.Background(), auth)
		registerDone <- err
	}()
	select {
	case <-store.activeSaveStarted:
	case <-time.After(time.Second):
		t.Fatal("registration persistence did not start")
	}

	markDone := make(chan struct{})
	go func() {
		m.MarkResult(context.Background(), Result{
			AuthID: auth.ID, Provider: auth.Provider, Model: "gpt-test",
			Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
		})
		close(markDone)
	}()
	select {
	case <-store.disabledSaved:
	case <-time.After(100 * time.Millisecond):
	}
	close(store.releaseActiveSave)

	select {
	case err := <-registerDone:
		if err != nil {
			t.Fatalf("register: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("registration did not finish")
	}
	select {
	case <-markDone:
	case <-time.After(time.Second):
		t.Fatal("402 result did not finish")
	}

	last := store.lastSaved()
	if last == nil || !last.Disabled || last.Status != StatusDisabled {
		t.Fatalf("final persisted auth = %#v, want payment-disabled", last)
	}
	current, _ := m.GetByID(auth.ID)
	if current == nil || !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("runtime auth = %#v, want payment-disabled", current)
	}
}

func TestPaymentRequiredDisableWinsInFlightMaintenancePersistence(t *testing.T) {
	store := newPaymentRequiredRaceStore()
	m := NewManager(store, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	auth := &Auth{
		ID:       "persistence-race-key",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Metadata: map[string]any{"token": "old"},
	}
	if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	staleMaintenance, _ := m.GetByID(auth.ID)
	staleMaintenance.Metadata["token"] = "refreshed"

	updateDone := make(chan error, 1)
	go func() {
		_, err := m.Update(withCredentialMaintenanceUpdate(context.Background(), staleMaintenance), staleMaintenance)
		updateDone <- err
	}()
	select {
	case <-store.activeSaveStarted:
	case <-time.After(time.Second):
		t.Fatal("maintenance persistence did not start")
	}

	markDone := make(chan struct{})
	go func() {
		m.MarkResult(context.Background(), Result{
			AuthID: auth.ID, Provider: auth.Provider, Model: "gpt-test",
			Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
		})
		close(markDone)
	}()

	// The old implementation lets the 402 persist while the stale active save is
	// blocked. A serialized implementation reaches this channel only after release.
	select {
	case <-store.disabledSaved:
	case <-time.After(100 * time.Millisecond):
	}
	close(store.releaseActiveSave)

	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("maintenance update: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("maintenance update did not finish")
	}
	select {
	case <-markDone:
	case <-time.After(time.Second):
		t.Fatal("402 result did not finish")
	}

	last := store.lastSaved()
	if last == nil || !last.Disabled || last.Status != StatusDisabled {
		t.Fatalf("final persisted auth = %#v, want payment-disabled", last)
	}
	current, _ := m.GetByID(auth.ID)
	if current == nil || !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("runtime auth = %#v, want payment-disabled", current)
	}
}
