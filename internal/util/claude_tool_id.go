package util

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
)

// SanitizeClaudeToolID ensures the given id conforms to Claude's
// tool_use.id regex ^[a-zA-Z0-9_-]+$.  Non-conforming characters are
// replaced with '_'; an empty result gets a generated fallback.
//
// Deprecated: Use common.SanitizeToolCallID instead.
func SanitizeClaudeToolID(id string) string {
	return common.SanitizeToolCallID(id)
}
