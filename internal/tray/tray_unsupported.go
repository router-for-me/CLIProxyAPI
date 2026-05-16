//go:build !darwin

package tray

import (
	"context"
	"fmt"
	"runtime"
)

// Run reports that tray mode is unavailable on this platform.
func Run(_ context.Context, _ Options) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, runtime.GOOS)
}
