package auth

import (
	"fmt"
	"sync"
	"time"
)

const (
	defaultWindowsResumeSettleDelay = 15 * time.Second

	// Win32 WM_POWERBROADCAST / PowerRegisterSuspendResumeNotification event constants.
	windowsPBTAPMSuspend         uint32 = 0x0004
	windowsPBTAPMResumeSuspend   uint32 = 0x0007
	windowsPBTAPMResumeAutomatic uint32 = 0x0012
	windowsPBTPowerSettingChange uint32 = 0x8013

	// Win32 GUID_CONSOLE_DISPLAY_STATE values (MONITOR_DISPLAY_STATE).
	windowsDisplayStateOff    uint32 = 0
	windowsDisplayStateOn     uint32 = 1
	windowsDisplayStateDimmed uint32 = 2
)

// windowsPowerState tracks Windows suspend/resume and console display transitions
// to defer single-use OAuth token refreshes during Modern Standby maintenance wakes
// and briefly after resume while networking settles.
type windowsPowerState struct {
	mu                    sync.Mutex
	nowFunc               func() time.Time
	settleDelay           time.Duration
	aoAc                  bool
	suspended             bool
	automaticWake         bool
	hasDisplayState       bool
	displayOn             bool
	displayOnSinceSuspend bool
	lastResumeAt          time.Time
}

func newWindowsPowerState(nowFunc func() time.Time, settleDelay time.Duration, aoAc bool) *windowsPowerState {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &windowsPowerState{
		nowFunc:     nowFunc,
		settleDelay: settleDelay,
		aoAc:        aoAc,
	}
}

func (s *windowsPowerState) setAoAc(aoAc bool) {
	s.mu.Lock()
	s.aoAc = aoAc
	s.mu.Unlock()
}

func (s *windowsPowerState) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

func (s *windowsPowerState) handleSuspendResume(eventType uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch eventType {
	case windowsPBTAPMSuspend:
		s.suspended = true
		s.automaticWake = false
		s.displayOnSinceSuspend = false
	case windowsPBTAPMResumeAutomatic:
		s.suspended = false
		s.lastResumeAt = s.now()
		// On Modern Standby (AoAc / S0 low-power idle) systems, keep refreshes deferred
		// across unattended maintenance wakes until interactive wake evidence arrives
		// (PBT_APMRESUMESUSPEND or a post-suspend active console display event).
		// On non-S0 hosts (desktops/servers/classic S3), apply only the bounded settle
		// delay so unattended timer/WOL wakes do not starve refreshes indefinitely.
		if s.aoAc && !s.displayOnSinceSuspend {
			s.automaticWake = true
		}
	case windowsPBTAPMResumeSuspend:
		s.suspended = false
		s.automaticWake = false
		s.lastResumeAt = s.now()
	}
}

func (s *windowsPowerState) handleDisplayState(state uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wasDisplayOff := s.hasDisplayState && !s.displayOn
	s.hasDisplayState = true
	displayOn := state != windowsDisplayStateOff
	s.displayOn = displayOn

	if displayOn {
		s.displayOnSinceSuspend = true
		// Clear suspend/maintenance-wake deferral and start the post-resume settle delay
		// when waking from sleep or exiting S0 Modern Standby (display Off -> active).
		// Display-only unblank on an awake non-S0 desktop or server does not delay refreshes.
		if s.suspended || s.automaticWake || (s.aoAc && wasDisplayOff) {
			s.suspended = false
			s.automaticWake = false
			s.lastResumeAt = s.now()
		}
	}
}

func (s *windowsPowerState) checkReadiness(label string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if label == "" {
		label = "OAuth"
	}
	if s.suspended {
		return fmt.Errorf("%s renewal deferred until interactive Windows wake: system suspended: %w", label, ErrRefreshDeferred)
	}
	if s.automaticWake {
		return fmt.Errorf("%s renewal deferred until interactive Windows wake: automatic wake with display off: %w", label, ErrRefreshDeferred)
	}
	if s.aoAc && s.hasDisplayState && !s.displayOn {
		return fmt.Errorf("%s renewal deferred until interactive Windows wake: Modern Standby display off: %w", label, ErrRefreshDeferred)
	}
	if s.settleDelay > 0 && !s.lastResumeAt.IsZero() {
		elapsed := s.now().Sub(s.lastResumeAt)
		if elapsed >= 0 && elapsed < s.settleDelay {
			remaining := (s.settleDelay - elapsed).Round(time.Second)
			return fmt.Errorf("%s renewal deferred while Windows network settles after resume (%s remaining): %w", label, remaining, ErrRefreshDeferred)
		}
		s.lastResumeAt = time.Time{}
	}
	return nil
}
