//go:build darwin

package auth

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// ReadMacOSSystemState reads the macOS power capabilities used to guard OAuth token refreshes.
var ReadMacOSSystemState = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "/usr/bin/pmset", "-g", "systemstate").Output()
}

// CheckMacOSRefreshReadiness verifies that macOS reports both Graphics and Network
// capabilities before issuing a token refresh, deferring with ErrRefreshDeferred
// during maintenance wakes or when power state cannot be verified.
func CheckMacOSRefreshReadiness(ctx context.Context, label string, readState func(context.Context) ([]byte, error)) error {
	if readState == nil {
		readState = ReadMacOSSystemState
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, 2*time.Second)
	defer cancelProbe()
	// shortcut: checks entry state; use native sleep coordination if in-flight sleep still breaks renewal.
	state, errState := readState(probeCtx)
	if errState != nil {
		return fmt.Errorf("%s renewal deferred: macOS power state unavailable: %w: %w", label, errState, ErrRefreshDeferred)
	}
	foundCapabilities := false
	var fields []string
	for _, line := range strings.Split(string(state), "\n") {
		capabilities, ok := strings.CutPrefix(strings.TrimSpace(line), "Current System Capabilities are:")
		if !ok {
			continue
		}
		foundCapabilities = true
		fields = strings.Fields(capabilities)
		if slices.Contains(fields, "Graphics") && slices.Contains(fields, "Network") {
			return nil
		}
	}
	if !foundCapabilities {
		raw := strings.TrimSpace(string(state))
		raw = strings.Join(strings.Fields(strings.ReplaceAll(raw, "\n", " ")), " ")
		if len(raw) > 200 {
			raw = raw[:200] + "..."
		}
		return fmt.Errorf("%s renewal deferred: unexpected pmset output: %q: %w", label, raw, ErrRefreshDeferred)
	}
	return fmt.Errorf("%s renewal deferred until interactive macOS wake: missing Graphics/Network (%v): %w", label, fields, ErrRefreshDeferred)
}
