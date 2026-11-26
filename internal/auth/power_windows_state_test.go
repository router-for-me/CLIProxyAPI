package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWindowsPowerState_DefaultAwakeAllowsRefresh(t *testing.T) {
	now := time.Date(2026, 11, 24, 5, 55, 0, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, defaultWindowsResumeSettleDelay, true)

	if err := state.checkReadiness("Claude"); err != nil {
		t.Fatalf("checkReadiness() = %v, want nil when system is awake", err)
	}
}

func TestWindowsPowerState_NonS0DisplayOffWithoutSuspendAllowsRefresh(t *testing.T) {
	now := time.Date(2026, 11, 24, 5, 55, 0, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, defaultWindowsResumeSettleDelay, false)

	state.handleDisplayState(windowsDisplayStateOff)
	if err := state.checkReadiness("Claude"); err != nil {
		t.Fatalf("checkReadiness() = %v, want nil when non-S0 display blanks without suspend", err)
	}

	// Turning the display back on without a prior suspend must not trigger a resume settle delay.
	state.handleDisplayState(windowsDisplayStateOn)
	if err := state.checkReadiness("Claude"); err != nil {
		t.Fatalf("checkReadiness() = %v, want nil after display-only unblank on non-S0 host", err)
	}
}

func TestWindowsPowerState_S0StartupWithDisplayAlreadyOffDefersThenSettlesOnWake(t *testing.T) {
	now := time.Date(2026, 11, 24, 5, 55, 34, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, 15*time.Second, true)

	// Proxy starts or restarts during a Modern Standby maintenance window:
	// PowerSettingRegisterNotification delivers initial Off with no historical PBT_APMSUSPEND.
	state.handleDisplayState(windowsDisplayStateOff)
	if errDark := state.checkReadiness("Claude"); !errors.Is(errDark, ErrRefreshDeferred) {
		t.Fatalf("S0 startup with display off checkReadiness() = %v, want ErrRefreshDeferred", errDark)
	}

	// Lid opens later: display turns On, starting the post-resume settle delay.
	now = now.Add(2 * time.Hour)
	state.handleDisplayState(windowsDisplayStateOn)
	if errSettling := state.checkReadiness("Claude"); !errors.Is(errSettling, ErrRefreshDeferred) {
		t.Fatalf("S0 wake from display-off checkReadiness() = %v, want ErrRefreshDeferred during settle window", errSettling)
	}

	now = now.Add(14 * time.Second)
	if errSettling := state.checkReadiness("Claude"); !errors.Is(errSettling, ErrRefreshDeferred) {
		t.Fatalf("S0 wake +14s checkReadiness() = %v, want ErrRefreshDeferred during settle window", errSettling)
	}

	now = now.Add(1 * time.Second)
	if errReady := state.checkReadiness("Claude"); errReady != nil {
		t.Fatalf("S0 wake +15s checkReadiness() = %v, want nil", errReady)
	}
}

func TestWindowsPowerState_DefersDuringSuspendAndMaintenanceWakeThenSettles(t *testing.T) {
	now := time.Date(2026, 11, 24, 0, 59, 42, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, 15*time.Second, true)

	// Lid closed: display turns off and system enters Modern Standby / suspend.
	state.handleDisplayState(windowsDisplayStateOff)
	state.handleSuspendResume(windowsPBTAPMSuspend)

	errSuspended := state.checkReadiness("Claude")
	if !errors.Is(errSuspended, ErrRefreshDeferred) {
		t.Fatalf("suspended checkReadiness() = %v, want ErrRefreshDeferred", errSuspended)
	}

	// Maintenance wake (Incident 1): automatic resume while display remains off.
	now = time.Date(2026, 11, 24, 5, 55, 34, 0, time.UTC)
	state.handleSuspendResume(windowsPBTAPMResumeAutomatic)

	errMaint := state.checkReadiness("Claude")
	if !errors.Is(errMaint, ErrRefreshDeferred) {
		t.Fatalf("maintenance wake checkReadiness() = %v, want ErrRefreshDeferred", errMaint)
	}
	if !strings.Contains(errMaint.Error(), "Claude") {
		t.Fatalf("maintenance wake error %q missing provider label", errMaint.Error())
	}

	// Even after the settle window passes, an S0 unattended wake with display still off stays deferred.
	now = now.Add(30 * time.Second)
	if errStillDark := state.checkReadiness("Claude"); !errors.Is(errStillDark, ErrRefreshDeferred) {
		t.Fatalf("unattended wake after 30s checkReadiness() = %v, want ErrRefreshDeferred", errStillDark)
	}

	// Interactive wake (Incident 2): lid opens, display turns on and PBT_APMRESUMESUSPEND fires.
	now = time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	state.handleSuspendResume(windowsPBTAPMResumeSuspend)
	state.handleDisplayState(windowsDisplayStateOn)

	// Immediately after wake and during network settle window, refresh must defer.
	if errSettling := state.checkReadiness("Claude"); !errors.Is(errSettling, ErrRefreshDeferred) {
		t.Fatalf("immediate post-wake checkReadiness() = %v, want ErrRefreshDeferred during settle window", errSettling)
	}
	now = now.Add(14 * time.Second)
	if errSettling := state.checkReadiness("Claude"); !errors.Is(errSettling, ErrRefreshDeferred) {
		t.Fatalf("post-wake +14s checkReadiness() = %v, want ErrRefreshDeferred during settle window", errSettling)
	}

	// Once the settle window elapses, refresh is allowed.
	now = now.Add(1 * time.Second)
	if errReady := state.checkReadiness("Claude"); errReady != nil {
		t.Fatalf("post-wake +15s checkReadiness() = %v, want nil", errReady)
	}
}

func TestWindowsPowerState_StaleDisplayOnBeforeSuspendDoesNotBypassMaintenanceWake(t *testing.T) {
	now := time.Date(2026, 11, 24, 0, 59, 0, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, 15*time.Second, true)

	// Sequence: On -> Suspend -> ResumeAutomatic (before Off arrives) -> Off.
	// Pre-suspend On must not be treated as interactive wake evidence for the new cycle.
	state.handleDisplayState(windowsDisplayStateOn)
	state.handleSuspendResume(windowsPBTAPMSuspend)

	now = now.Add(5 * time.Hour)
	state.handleSuspendResume(windowsPBTAPMResumeAutomatic)

	// Even before Off arrives and after settleDelay elapses, refresh stays deferred.
	now = now.Add(30 * time.Second)
	if errMaint := state.checkReadiness("Claude"); !errors.Is(errMaint, ErrRefreshDeferred) {
		t.Fatalf("automatic wake with stale pre-suspend On checkReadiness() = %v, want ErrRefreshDeferred", errMaint)
	}

	state.handleDisplayState(windowsDisplayStateOff)
	if errOff := state.checkReadiness("Claude"); !errors.Is(errOff, ErrRefreshDeferred) {
		t.Fatalf("after Off checkReadiness() = %v, want ErrRefreshDeferred", errOff)
	}

	// Real post-suspend On clears automaticWake and starts the settle window.
	now = now.Add(1 * time.Hour)
	state.handleDisplayState(windowsDisplayStateOn)
	if errSettling := state.checkReadiness("Claude"); !errors.Is(errSettling, ErrRefreshDeferred) {
		t.Fatalf("post-suspend On checkReadiness() = %v, want ErrRefreshDeferred during settle window", errSettling)
	}

	now = now.Add(15 * time.Second)
	if errReady := state.checkReadiness("Claude"); errReady != nil {
		t.Fatalf("post-suspend On +15s checkReadiness() = %v, want nil", errReady)
	}
}

func TestWindowsPowerState_DisplayOnBeforeAutomaticResumeDoesNotStickDeferred(t *testing.T) {
	now := time.Date(2026, 11, 22, 19, 0, 0, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, 15*time.Second, true)

	state.handleDisplayState(windowsDisplayStateOff)
	state.handleSuspendResume(windowsPBTAPMSuspend)

	// Some Modern Standby systems turn the console display on after suspend before delivering
	// PBT_APMRESUMEAUTOMATIC and may omit PBT_APMRESUMESUSPEND.
	now = now.Add(6 * time.Minute)
	state.handleDisplayState(windowsDisplayStateOn)
	state.handleSuspendResume(windowsPBTAPMResumeAutomatic)

	if errSettling := state.checkReadiness("Codex"); !errors.Is(errSettling, ErrRefreshDeferred) {
		t.Fatalf("post-wake checkReadiness() = %v, want ErrRefreshDeferred during settle window", errSettling)
	}

	now = now.Add(15 * time.Second)
	if errReady := state.checkReadiness("Codex"); errReady != nil {
		t.Fatalf("post-wake +15s checkReadiness() = %v, want nil", errReady)
	}
}

func TestWindowsPowerState_NonS0UnattendedResumeSettlesBoundedWithoutStarving(t *testing.T) {
	now := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, 15*time.Second, false)

	// Non-S0 host (desktop/server/S3) wakes via timer or WOL with display remaining off.
	state.handleDisplayState(windowsDisplayStateOff)
	state.handleSuspendResume(windowsPBTAPMSuspend)

	if errSuspended := state.checkReadiness("Claude"); !errors.Is(errSuspended, ErrRefreshDeferred) {
		t.Fatalf("non-S0 suspended checkReadiness() = %v, want ErrRefreshDeferred", errSuspended)
	}

	now = now.Add(2 * time.Hour)
	state.handleSuspendResume(windowsPBTAPMResumeAutomatic)

	// Bounded settle window protects network recovery.
	if errSettling := state.checkReadiness("Claude"); !errors.Is(errSettling, ErrRefreshDeferred) {
		t.Fatalf("non-S0 automatic resume checkReadiness() = %v, want ErrRefreshDeferred during settle window", errSettling)
	}

	// Once settle window completes, non-S0 unattended host resumes refreshing instead of starving forever.
	now = now.Add(15 * time.Second)
	if errReady := state.checkReadiness("Claude"); errReady != nil {
		t.Fatalf("non-S0 automatic resume +15s checkReadiness() = %v, want nil", errReady)
	}
}

func TestWindowsPowerState_BackwardClockJumpDoesNotDeferForever(t *testing.T) {
	now := time.Date(2026, 11, 22, 19, 6, 59, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, 15*time.Second, false)

	state.handleSuspendResume(windowsPBTAPMSuspend)
	state.handleSuspendResume(windowsPBTAPMResumeSuspend)

	now = now.Add(-5 * time.Second)
	if errReady := state.checkReadiness("Claude"); errReady != nil {
		t.Fatalf("backward clock jump checkReadiness() = %v, want nil", errReady)
	}
}

func TestWindowsPowerState_DimmedDisplayClearsMaintenanceWake(t *testing.T) {
	now := time.Date(2026, 11, 24, 6, 0, 0, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, 15*time.Second, true)

	state.handleDisplayState(windowsDisplayStateOff)
	state.handleSuspendResume(windowsPBTAPMSuspend)
	state.handleSuspendResume(windowsPBTAPMResumeAutomatic)

	if errMaint := state.checkReadiness(""); !errors.Is(errMaint, ErrRefreshDeferred) {
		t.Fatalf("maintenance wake checkReadiness() = %v, want ErrRefreshDeferred", errMaint)
	}

	now = now.Add(1 * time.Minute)
	state.handleDisplayState(windowsDisplayStateDimmed)
	if errSettling := state.checkReadiness(""); !errors.Is(errSettling, ErrRefreshDeferred) {
		t.Fatalf("post-dimmed-wake checkReadiness() = %v, want ErrRefreshDeferred during settle window", errSettling)
	}

	now = now.Add(15 * time.Second)
	if errReady := state.checkReadiness(""); errReady != nil {
		t.Fatalf("post-dimmed-wake +15s checkReadiness() = %v, want nil", errReady)
	}
}
