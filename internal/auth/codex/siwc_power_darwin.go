package codex

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

var siwcReadSystemState = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "/usr/bin/pmset", "-g", "systemstate").Output()
}

func siwcRefreshReadiness(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// shortcut: checks entry state; use native sleep coordination if in-flight sleep still breaks renewal.
	state, errState := siwcReadSystemState(ctx)
	if errState != nil {
		return fmt.Errorf("SIWC renewal deferred: macOS power state unavailable: %w", errState)
	}
	for _, line := range strings.Split(string(state), "\n") {
		capabilities, ok := strings.CutPrefix(strings.TrimSpace(line), "Current System Capabilities are:")
		if !ok {
			continue
		}
		fields := strings.Fields(capabilities)
		if slices.Contains(fields, "Graphics") && slices.Contains(fields, "Network") {
			return nil
		}
	}
	return fmt.Errorf("SIWC renewal deferred until interactive macOS wake: %w", context.Canceled)
}
