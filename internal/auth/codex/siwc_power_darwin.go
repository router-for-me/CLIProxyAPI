package codex

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// ReadSIWCSystemState reads the macOS power capabilities used by SIWC renewal.
var ReadSIWCSystemState = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "/usr/bin/pmset", "-g", "systemstate").Output()
}

func siwcRefreshReadiness(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// shortcut: checks entry state; use native sleep coordination if in-flight sleep still breaks renewal.
	state, errState := ReadSIWCSystemState(ctx)
	if errState != nil {
		return fmt.Errorf("SIWC renewal deferred: macOS power state unavailable: %w", errState)
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
		return fmt.Errorf("SIWC renewal deferred: unexpected pmset output: %q: %w", raw, ErrSIWCRefreshDeferred)
	}
	return fmt.Errorf("SIWC renewal deferred until interactive macOS wake: missing Graphics/Network (%v): %w", fields, ErrSIWCRefreshDeferred)
}
