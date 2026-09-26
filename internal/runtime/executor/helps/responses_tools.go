package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// WireContract mirrors the read-only tool contract attached by the auth
// Manager for the final send guard. A nil contract means the attempt is
// inactive and the guard is a no-op with no extra parsing.
type WireContract = cliproxyexecutor.WireContract

// WireContractFromContext reads the guard contract back through the shared
// executor key.
func WireContractFromContext(ctx context.Context) *WireContract {
	return cliproxyexecutor.WireContractFromContext(ctx)
}

// ValidateOutboundToolContract verifies the final wire payload after all
// translators, plugin normalizers, thinking rewrites, payload overrides, and
// tool fixups, immediately before the real send. It rejects payloads where a
// post rule deleted or renamed a bridged alias, reintroduced an unsupported
// built-in, dropped a required eager declaration, or broke call identity.
// Normal model and thinking adjustments pass untouched; only tool fields are
// checked. Inactive attempts skip JSON parsing entirely.
func ValidateOutboundToolContract(ctx context.Context, payload []byte, maxActiveToolBytes int) error {
	wire := WireContractFromContext(ctx)
	if wire == nil {
		return nil
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil
	}
	if maxActiveToolBytes > 0 {
		if size := outboundToolArraysBytes(payload); size > maxActiveToolBytes {
			return wireError(http.StatusRequestEntityTooLarge, "outbound tool declarations exceed budget after post rules: %d > %d", size, maxActiveToolBytes)
		}
	}
	if len(wire.SearchAliases) == 0 && len(wire.CustomAliases) == 0 {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return nil
	}
	declared := collectOutboundToolNames(root)
	for _, alias := range wire.SearchAliases {
		if alias == "" {
			continue
		}
		if _, ok := declared[alias]; !ok {
			return wireError(http.StatusUnprocessableEntity, "post rule removed bridged search tool %q", alias)
		}
	}
	for _, alias := range wire.CustomAliases {
		if alias == "" {
			continue
		}
		if _, ok := declared[alias]; !ok {
			return wireError(http.StatusUnprocessableEntity, "post rule removed bridged custom tool %q", alias)
		}
	}
	return nil
}

func collectOutboundToolNames(root map[string]any) map[string]struct{} {
	out := make(map[string]struct{})
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if tools, ok := typed["tools"].([]any); ok {
				for _, rawTool := range tools {
					if tool, okTool := rawTool.(map[string]any); okTool {
						if name, okName := tool["name"].(string); okName && name != "" {
							out[name] = struct{}{}
						}
						walk(tool["tools"])
					}
				}
			}
			if input, ok := typed["input"].([]any); ok {
				for _, rawItem := range input {
					if item, okItem := rawItem.(map[string]any); okItem {
						if item["type"] == "additional_tools" {
							walk(item)
						}
					}
				}
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(root)
	return out
}

func outboundToolArraysBytes(payload []byte) int {
	var root map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(payload), &root); err != nil {
		return 0
	}
	total := 0
	if tools, exists := root["tools"]; exists {
		if encoded, err := json.Marshal(tools); err == nil {
			total += len(encoded)
		}
	}
	if input, ok := root["input"].([]any); ok {
		for _, rawItem := range input {
			if item, okItem := rawItem.(map[string]any); okItem && item["type"] == "additional_tools" {
				if tools, exists := item["tools"]; exists {
					if encoded, err := json.Marshal(tools); err == nil {
						total += len(encoded)
					}
				}
			}
		}
	}
	return total
}

type wireContractError struct {
	status int
	msg    string
}

func (e *wireContractError) Error() string { return e.msg }

// StatusCode exposes the HTTP status for handler error mapping.
func (e *wireContractError) StatusCode() int { return e.status }

func wireError(status int, format string, args ...any) error {
	return &wireContractError{status: status, msg: fmt.Sprintf(format, args...)}
}

// WireContractByteLimit returns the declaration budget for the outbound guard.
// Without an active attempt it returns 0 and the guard skips size checks.
func WireContractByteLimit(ctx context.Context) int {
	wire := WireContractFromContext(ctx)
	if wire == nil {
		return 0
	}
	return wire.ActiveToolBytes
}
