//go:build windows

package common

import (
	"os"
	"syscall"
	"unsafe"
)

// GetDiskSpaceInfo 获取缓存目录所在磁盘的空间信息 (Windows)
func GetDiskSpaceInfo() DiskSpaceInfo {
	cachePath := GetDiskCachePath()
	if cachePath == "" {
		cachePath = os.TempDir()
	}

	info := DiskSpaceInfo{}

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64

	pathPtr, err := syscall.UTF16PtrFromString(cachePath)
	if err != nil {
		return info
	}

	ret, _, _ := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)

	if ret == 0 {
		return info
	}

	info.Total = totalBytes
	info.Free = freeBytesAvailable
	info.Used = totalBytes - totalFreeBytes

	if info.Total > 0 {
		info.UsedPercent = float64(info.Used) / float64(info.Total) * 100
	}

	return info
}
