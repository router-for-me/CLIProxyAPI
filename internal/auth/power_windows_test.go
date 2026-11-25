//go:build windows

package auth

import (
	"context"
	"errors"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCheckWindowsRefreshReadiness(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     WindowsPowerState
		probeErr  error
		wantDefer bool
	}{
		{name: "modern_standby_display_off", state: WindowsPowerState{ModernStandby: true, DisplayKnown: true}, wantDefer: true},
		{name: "modern_standby_display_on", state: WindowsPowerState{ModernStandby: true, DisplayKnown: true, DisplayOn: true}},
		{name: "modern_standby_display_unknown", state: WindowsPowerState{ModernStandby: true}},
		{name: "no_modern_standby_display_off", state: WindowsPowerState{DisplayKnown: true}},
		{name: "probe_unavailable", probeErr: errors.New("powrprof unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckWindowsRefreshReadiness(context.Background(), "Test", func(context.Context) (WindowsPowerState, error) {
				return tc.state, tc.probeErr
			})
			if deferred := errors.Is(err, ErrRefreshDeferred); deferred != tc.wantDefer {
				t.Fatalf("CheckWindowsRefreshReadiness() = %v; want deferred=%v", err, tc.wantDefer)
			}
			if !tc.wantDefer && err != nil {
				t.Fatalf("CheckWindowsRefreshReadiness() = %v; want nil", err)
			}
		})
	}
}

func TestConsoleDisplayValue(t *testing.T) {
	broadcast := func(guid windows.GUID, length uint32, value int32) *powerBroadcastSetting {
		// POWERBROADCAST_SETTING is followed by DataLength bytes; reserve room for a DWORD.
		buf := make([]byte, unsafe.Sizeof(powerBroadcastSetting{})+4)
		setting := (*powerBroadcastSetting)(unsafe.Pointer(&buf[0]))
		setting.PowerSetting = guid
		setting.DataLength = length
		*(*int32)(unsafe.Pointer(&setting.Data[0])) = value
		return setting
	}
	displayGUID := guidConsoleDisplayState
	otherGUID := guidConsoleDisplayState
	otherGUID.Data1++

	for _, tc := range []struct {
		name    string
		setting *powerBroadcastSetting
		want    int32
		wantOK  bool
	}{
		{name: "off", setting: broadcast(displayGUID, 4, 0), want: 0, wantOK: true},
		{name: "on", setting: broadcast(displayGUID, 4, 1), want: 1, wantOK: true},
		{name: "dimmed", setting: broadcast(displayGUID, 4, 2), want: 2, wantOK: true},
		{name: "other_setting", setting: broadcast(otherGUID, 4, 0)},
		{name: "short_data", setting: broadcast(displayGUID, 1, 0)},
		{name: "nil", setting: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := consoleDisplayValue(tc.setting)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("consoleDisplayValue() = %d, %v; want %d, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestSystemPowerCapabilitiesLayout(t *testing.T) {
	if size := unsafe.Sizeof(systemPowerCapabilities{}); size != 76 {
		t.Fatalf("systemPowerCapabilities size = %d; want 76 to match SYSTEM_POWER_CAPABILITIES", size)
	}
	if offset := unsafe.Offsetof(systemPowerCapabilities{}.AoAc); offset != 20 {
		t.Fatalf("AoAc offset = %d; want 20", offset)
	}
}

// TestReadWindowsPowerStateLive calls the real Windows APIs. It cannot assert a particular
// machine's capabilities, only that the probe succeeds and reports a display state on
// Modern Standby systems.
func TestReadWindowsPowerStateLive(t *testing.T) {
	state, err := ReadWindowsPowerState(context.Background())
	if err != nil {
		t.Fatalf("ReadWindowsPowerState() error = %v", err)
	}
	if state.ModernStandby && !state.DisplayKnown {
		t.Fatalf("ReadWindowsPowerState() = %+v; want the display state delivered on registration", state)
	}
	t.Logf("power state: %+v", state)
}
