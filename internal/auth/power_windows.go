//go:build windows

package auth

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WindowsPowerState is the power information used to guard OAuth token refreshes on Windows.
type WindowsPowerState struct {
	// ModernStandby reports whether the system uses Modern Standby (S0 low power idle, "AoAc").
	ModernStandby bool
	// DisplayKnown reports whether Windows has delivered the console display state yet.
	DisplayKnown bool
	// DisplayOn reports whether the console display is on or dimmed.
	DisplayOn bool
}

// ReadWindowsPowerState reads the Windows power state used to guard OAuth token refreshes.
var ReadWindowsPowerState = func(ctx context.Context) (WindowsPowerState, error) {
	return readWindowsPowerState(ctx)
}

// CheckWindowsRefreshReadiness defers a token refresh with ErrRefreshDeferred while a Modern
// Standby system has its display off. On these systems turning the display off is how Windows
// enters standby, and the system keeps waking briefly for maintenance with the network
// connected. A refresh sent during such a wake can be cut off after Anthropic has rotated the
// single-use refresh token, which loses the credential.
//
// Systems without Modern Standby are never deferred: their display turns off without the system
// sleeping. When the state cannot be read the refresh is not deferred either, because a probe
// that keeps failing would otherwise stop renewal for good.
func CheckWindowsRefreshReadiness(ctx context.Context, label string, readState func(context.Context) (WindowsPowerState, error)) error {
	if readState == nil {
		readState = ReadWindowsPowerState
	}
	state, errState := readState(ctx)
	if errState != nil || !state.ModernStandby || !state.DisplayKnown || state.DisplayOn {
		return nil
	}
	return fmt.Errorf("%s renewal deferred until the display is on: Windows Modern Standby: %w", label, ErrRefreshDeferred)
}

const (
	deviceNotifyCallback        = 2      // DEVICE_NOTIFY_CALLBACK
	pbtPowerSettingChange       = 0x8013 // PBT_POWERSETTINGCHANGE
	windowsDisplayUnknown int32 = -1
	windowsDisplayOff     int32 = 0 // GUID_CONSOLE_DISPLAY_STATE data: 0 off, 1 on, 2 dimmed
	initialDisplayWait          = 500 * time.Millisecond
)

// guidConsoleDisplayState is GUID_CONSOLE_DISPLAY_STATE from winnt.h.
var guidConsoleDisplayState = windows.GUID{Data1: 0x6fe69556, Data2: 0x704a, Data3: 0x47a0, Data4: [8]byte{0x8f, 0x24, 0xc2, 0x8d, 0x93, 0x6f, 0xda, 0x47}}

var (
	modPowrprof                          = windows.NewLazySystemDLL("powrprof.dll")
	procGetPwrCapabilities               = modPowrprof.NewProc("GetPwrCapabilities")
	procPowerSettingRegisterNotification = modPowrprof.NewProc("PowerSettingRegisterNotification")
)

// systemPowerCapabilities mirrors SYSTEM_POWER_CAPABILITIES (76 bytes); only AoAc is read.
type systemPowerCapabilities struct {
	_    [20]byte // PowerButtonPresent through WakeAlarmPresent
	AoAc byte
	_    [55]byte // DiskSpinDown through DefaultLowLatencyWake
}

// deviceNotifySubscribeParameters mirrors DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS.
type deviceNotifySubscribeParameters struct {
	callback uintptr
	context  uintptr
}

// powerBroadcastSetting mirrors the fixed part of POWERBROADCAST_SETTING.
type powerBroadcastSetting struct {
	PowerSetting windows.GUID
	DataLength   uint32
	Data         [1]byte
}

var (
	modernStandbyOnce sync.Once
	modernStandby     bool
	modernStandbyErr  error

	displayOnce      sync.Once
	displayErr       error
	displayState     atomic.Int32
	displayDelivered = make(chan struct{})
	displayReady     sync.Once
	// displaySubscription stays referenced for the life of the process, as Windows keeps
	// calling the registered callback.
	displaySubscription deviceNotifySubscribeParameters
)

func readWindowsPowerState(context.Context) (WindowsPowerState, error) {
	modernStandbyOnce.Do(func() {
		var caps systemPowerCapabilities
		if ok, _, errCall := procGetPwrCapabilities.Call(uintptr(unsafe.Pointer(&caps))); ok == 0 {
			modernStandbyErr = fmt.Errorf("GetPwrCapabilities: %w", errCall)
			return
		}
		modernStandby = caps.AoAc != 0
	})
	if modernStandbyErr != nil {
		return WindowsPowerState{}, modernStandbyErr
	}
	if !modernStandby {
		return WindowsPowerState{}, nil
	}

	displayOnce.Do(subscribeConsoleDisplayState)
	if displayErr != nil {
		return WindowsPowerState{ModernStandby: true}, displayErr
	}
	state := WindowsPowerState{ModernStandby: true}
	switch value := displayState.Load(); value {
	case windowsDisplayUnknown:
	case windowsDisplayOff:
		state.DisplayKnown = true
	default:
		state.DisplayKnown = true
		state.DisplayOn = true
	}
	return state, nil
}

func subscribeConsoleDisplayState() {
	displayState.Store(windowsDisplayUnknown)
	displaySubscription.callback = syscall.NewCallback(onPowerSettingChange)
	var handle uintptr
	status, _, _ := procPowerSettingRegisterNotification.Call(
		uintptr(unsafe.Pointer(&guidConsoleDisplayState)),
		deviceNotifyCallback,
		uintptr(unsafe.Pointer(&displaySubscription)),
		uintptr(unsafe.Pointer(&handle)),
	)
	if status != 0 {
		displayErr = fmt.Errorf("PowerSettingRegisterNotification: %w", syscall.Errno(status))
		return
	}
	// Windows delivers the current state right after registration, on its own thread.
	select {
	case <-displayDelivered:
	case <-time.After(initialDisplayWait):
	}
}

// onPowerSettingChange is the DEVICE_NOTIFY_CALLBACK_ROUTINE; setting points to memory owned
// by Windows for the duration of the call.
func onPowerSettingChange(_ uintptr, eventType uintptr, setting *powerBroadcastSetting) uintptr {
	if eventType != pbtPowerSettingChange {
		return 0
	}
	if value, ok := consoleDisplayValue(setting); ok {
		displayState.Store(value)
		displayReady.Do(func() { close(displayDelivered) })
	}
	return 0
}

// consoleDisplayValue extracts the GUID_CONSOLE_DISPLAY_STATE value from a power broadcast.
func consoleDisplayValue(setting *powerBroadcastSetting) (int32, bool) {
	if setting == nil || setting.PowerSetting != guidConsoleDisplayState || setting.DataLength < 4 {
		return 0, false
	}
	return *(*int32)(unsafe.Pointer(&setting.Data[0])), true
}
