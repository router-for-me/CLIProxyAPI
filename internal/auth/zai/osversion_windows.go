//go:build windows

package zai

import (
	"golang.org/x/sys/windows/registry"
)

// zcodeOsVersion mirrors node's os.version() on Windows (the registered
// product name), which the official client reports as X-Os-Version.
func zcodeOsVersion() string {
	key, errOpen := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if errOpen != nil {
		return ""
	}
	defer func() {
		if errClose := key.Close(); errClose != nil {
			_ = errClose
		}
	}()
	product, _, errRead := key.GetStringValue("ProductName")
	if errRead != nil {
		return ""
	}
	return printableASCII(product)
}
