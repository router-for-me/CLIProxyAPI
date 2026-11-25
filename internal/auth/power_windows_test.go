//go:build windows

package auth

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type testPowerBroadcastSetting struct {
	Header powerBroadcastSettingHeader
	Data   [4]byte
}

func TestSystemPowerCapabilitiesLayout(t *testing.T) {
	var caps systemPowerCapabilities
	if got := unsafe.Sizeof(caps); got != 76 {
		t.Fatalf("sizeof(systemPowerCapabilities) = %d, want 76", got)
	}
	if got := unsafe.Offsetof(caps.AoAc); got != 20 {
		t.Fatalf("offsetof(systemPowerCapabilities.AoAc) = %d, want 20", got)
	}
}

func TestWindowsPowerCallbacksAndReadiness(t *testing.T) {
	now := time.Date(2026, 11, 24, 0, 59, 42, 0, time.UTC)
	state := newWindowsPowerState(func() time.Time { return now }, 15*time.Second, true)

	// Unrelated GUID, short payload, and nil setting must be ignored safely.
	unrelated := testPowerBroadcastSetting{
		Header: powerBroadcastSettingHeader{
			PowerSetting: windows.GUID{Data1: 0x12345678},
			DataLength:   4,
		},
	}
	dispatchWindowsPowerSetting(state, windowsPBTPowerSettingChange, unsafe.Pointer(&unrelated))
	dispatchWindowsPowerSetting(state, windowsPBTPowerSettingChange, nil)
	shortPayload := testPowerBroadcastSetting{
		Header: powerBroadcastSettingHeader{
			PowerSetting: guidConsoleDisplayState,
			DataLength:   2,
		},
	}
	dispatchWindowsPowerSetting(state, windowsPBTPowerSettingChange, unsafe.Pointer(&shortPayload))

	// S0 startup with display already off (no prior suspend) -> deferred.
	displayOff := testPowerBroadcastSetting{
		Header: powerBroadcastSettingHeader{
			PowerSetting: guidConsoleDisplayState,
			DataLength:   4,
		},
	}
	binary.LittleEndian.PutUint32(displayOff.Data[:], windowsDisplayStateOff)
	dispatchWindowsPowerSetting(state, windowsPBTPowerSettingChange, unsafe.Pointer(&displayOff))

	if errDark := checkWindowsRefreshReadinessWithState(context.Background(), state, "Claude"); !errors.Is(errDark, ErrRefreshDeferred) {
		t.Fatalf("checkWindowsRefreshReadinessWithState() with S0 display off = %v, want ErrRefreshDeferred", errDark)
	}

	// Suspend + automatic wake with display still off -> deferred.
	dispatchWindowsSuspendResume(state, windowsPBTAPMSuspend)
	now = now.Add(5 * time.Hour)
	dispatchWindowsSuspendResume(state, windowsPBTAPMResumeAutomatic)
	if errMaint := checkWindowsRefreshReadinessWithState(context.Background(), state, "Claude"); !errors.Is(errMaint, ErrRefreshDeferred) {
		t.Fatalf("checkWindowsRefreshReadinessWithState() during maintenance wake = %v, want ErrRefreshDeferred", errMaint)
	}

	// Interactive display-on wake -> settles for 15s, then ready.
	now = now.Add(2 * time.Hour)
	displayOn := testPowerBroadcastSetting{
		Header: powerBroadcastSettingHeader{
			PowerSetting: guidConsoleDisplayState,
			DataLength:   4,
		},
	}
	binary.LittleEndian.PutUint32(displayOn.Data[:], windowsDisplayStateOn)
	dispatchWindowsPowerSetting(state, windowsPBTPowerSettingChange, unsafe.Pointer(&displayOn))

	if errSettling := checkWindowsRefreshReadinessWithState(context.Background(), state, "Claude"); !errors.Is(errSettling, ErrRefreshDeferred) {
		t.Fatalf("checkWindowsRefreshReadinessWithState() during settle window = %v, want ErrRefreshDeferred", errSettling)
	}

	now = now.Add(15 * time.Second)
	if errReady := checkWindowsRefreshReadinessWithState(context.Background(), state, "Claude"); errReady != nil {
		t.Fatalf("checkWindowsRefreshReadinessWithState() after settle window = %v, want nil", errReady)
	}

	// Canceled context must propagate cancellation.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if errCanceled := checkWindowsRefreshReadinessWithState(canceledCtx, state, "Claude"); !errors.Is(errCanceled, context.Canceled) {
		t.Fatalf("checkWindowsRefreshReadinessWithState(canceledCtx) = %v, want context.Canceled", errCanceled)
	}
}
