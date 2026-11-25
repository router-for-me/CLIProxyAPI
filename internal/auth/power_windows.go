//go:build windows

package auth

import (
	"context"
	"encoding/binary"
	"sync"
	"time"
	"unsafe"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/windows"
)

const deviceNotifyCallback = 2

// guidConsoleDisplayState is Win32 GUID_CONSOLE_DISPLAY_STATE ({6FE69556-704A-47A0-8F24-C28D936FDA47}).
var guidConsoleDisplayState = windows.GUID{
	Data1: 0x6fe69556,
	Data2: 0x704a,
	Data3: 0x47a0,
	Data4: [8]byte{0x8f, 0x24, 0xc2, 0x8d, 0x93, 0x6f, 0xda, 0x47},
}

type deviceNotifySubscribeParameters struct {
	Callback uintptr
	Context  uintptr
}

type powerBroadcastSettingHeader struct {
	PowerSetting windows.GUID
	DataLength   uint32
}

type batteryReportingScale struct {
	Granularity uint32
	Capacity    uint32
}

// systemPowerCapabilities mirrors Win32 SYSTEM_POWER_CAPABILITIES (winnt.h).
type systemPowerCapabilities struct {
	PowerButtonPresent        byte
	SleepButtonPresent        byte
	LidPresent                byte
	SystemS1                  byte
	SystemS2                  byte
	SystemS3                  byte
	SystemS4                  byte
	SystemS5                  byte
	HiberFilePresent          byte
	FullWake                  byte
	VideoDimPresent           byte
	ApmPresent                byte
	UpsPresent                byte
	ThermalControl            byte
	ProcessorThrottle         byte
	ProcessorMinThrottle      byte
	ProcessorMaxThrottle      byte
	FastSystemS4              byte
	Hiberboot                 byte
	WakeAlarmPresent          byte
	AoAc                      byte
	DiskSpinDown              byte
	HiberFileType             byte
	AoAcConnectivitySupported byte
	Spare3                    [6]byte
	SystemBatteriesPresent    byte
	BatteriesAreShortTerm     byte
	BatteryScale              [3]batteryReportingScale
	AcOnLineWake              uint32
	SoftLidWake               uint32
	RtcWake                   uint32
	MinDeviceWakeState        uint32
	DefaultLowLatencyWake     uint32
}

var (
	modPowrprof                                = windows.NewLazySystemDLL("powrprof.dll")
	procGetPwrCapabilities                     = modPowrprof.NewProc("GetPwrCapabilities")
	procPowerRegisterSuspendResumeNotification = modPowrprof.NewProc("PowerRegisterSuspendResumeNotification")
	procPowerSettingRegisterNotification       = modPowrprof.NewProc("PowerSettingRegisterNotification")

	defaultWindowsPowerState = newWindowsPowerState(time.Now, defaultWindowsResumeSettleDelay, false)

	windowsPowerMonitorOnce    sync.Once
	windowsSuspendResumeParams deviceNotifySubscribeParameters
	windowsDisplayStateParams  deviceNotifySubscribeParameters
	windowsSuspendResumeHandle uintptr
	windowsDisplayStateHandle  uintptr
)

func init() {
	initWindowsPowerMonitor()
}

func detectWindowsAoAc() bool {
	if errFind := procGetPwrCapabilities.Find(); errFind != nil {
		log.Debugf("windows power monitor: GetPwrCapabilities unavailable: %v", errFind)
		return false
	}
	var caps systemPowerCapabilities
	r1, _, _ := procGetPwrCapabilities.Call(uintptr(unsafe.Pointer(&caps)))
	if r1 == 0 {
		log.Debugf("windows power monitor: GetPwrCapabilities failed")
		return false
	}
	return caps.AoAc != 0
}

func initWindowsPowerMonitor() {
	windowsPowerMonitorOnce.Do(func() {
		defaultWindowsPowerState.setAoAc(detectWindowsAoAc())

		if errFind := procPowerRegisterSuspendResumeNotification.Find(); errFind != nil {
			log.Debugf("windows power monitor: PowerRegisterSuspendResumeNotification unavailable: %v", errFind)
		} else {
			windowsSuspendResumeParams = deviceNotifySubscribeParameters{
				Callback: windows.NewCallback(windowsSuspendResumeCallback),
			}
			r1, _, _ := procPowerRegisterSuspendResumeNotification.Call(
				uintptr(deviceNotifyCallback),
				uintptr(unsafe.Pointer(&windowsSuspendResumeParams)),
				uintptr(unsafe.Pointer(&windowsSuspendResumeHandle)),
			)
			if r1 != 0 {
				log.Debugf("windows power monitor: PowerRegisterSuspendResumeNotification failed with error %d", r1)
			}
		}

		if errFind := procPowerSettingRegisterNotification.Find(); errFind != nil {
			log.Debugf("windows power monitor: PowerSettingRegisterNotification unavailable: %v", errFind)
		} else {
			windowsDisplayStateParams = deviceNotifySubscribeParameters{
				Callback: windows.NewCallback(windowsPowerSettingCallback),
			}
			r1, _, _ := procPowerSettingRegisterNotification.Call(
				uintptr(unsafe.Pointer(&guidConsoleDisplayState)),
				uintptr(deviceNotifyCallback),
				uintptr(unsafe.Pointer(&windowsDisplayStateParams)),
				uintptr(unsafe.Pointer(&windowsDisplayStateHandle)),
			)
			if r1 != 0 {
				log.Debugf("windows power monitor: PowerSettingRegisterNotification(GUID_CONSOLE_DISPLAY_STATE) failed with error %d", r1)
			}
		}
	})
}

func dispatchWindowsSuspendResume(state *windowsPowerState, eventType uint32) {
	if state == nil {
		return
	}
	state.handleSuspendResume(eventType)
}

func dispatchWindowsPowerSetting(state *windowsPowerState, eventType uint32, setting unsafe.Pointer) {
	if state == nil || eventType != windowsPBTPowerSettingChange || setting == nil {
		return
	}
	hdr := (*powerBroadcastSettingHeader)(setting)
	if hdr.PowerSetting == guidConsoleDisplayState && hdr.DataLength >= 4 {
		dataPtr := (*[4]byte)(unsafe.Add(setting, unsafe.Sizeof(powerBroadcastSettingHeader{})))
		state.handleDisplayState(binary.LittleEndian.Uint32(dataPtr[:]))
	}
}

func windowsSuspendResumeCallback(_ uintptr, eventType uintptr, _ unsafe.Pointer) uintptr {
	dispatchWindowsSuspendResume(defaultWindowsPowerState, uint32(eventType))
	return 0
}

func windowsPowerSettingCallback(_ uintptr, eventType uintptr, setting unsafe.Pointer) uintptr {
	dispatchWindowsPowerSetting(defaultWindowsPowerState, uint32(eventType), setting)
	return 0
}

func checkWindowsRefreshReadinessWithState(ctx context.Context, state *windowsPowerState, label string) error {
	if ctx != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
	}
	if state == nil {
		return nil
	}
	return state.checkReadiness(label)
}

// CheckWindowsRefreshReadiness verifies that Windows is not suspended, in an
// unattended Modern Standby wake, or still within the post-resume settle window.
func CheckWindowsRefreshReadiness(ctx context.Context, label string) error {
	initWindowsPowerMonitor()
	return checkWindowsRefreshReadinessWithState(ctx, defaultWindowsPowerState, label)
}
