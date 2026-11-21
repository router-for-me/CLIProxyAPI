//go:build !darwin

package auth

import "context"

// CheckMacOSRefreshReadiness is a no-op on non-darwin platforms.
func CheckMacOSRefreshReadiness(context.Context, string, func(context.Context) ([]byte, error)) error {
	return nil
}
