// Package claudecode exposes Claude Code client detection to SDK embedders.
//
// The Claude executor uses the same detection to decide whether a request is
// passed through as native Claude Code traffic or cloaked. Embedders that make
// routing decisions before the executor runs (for example, keeping some
// credentials for native Claude Code traffic only) can call DetectRequest to
// reach the same verdict instead of maintaining a separate classifier.
package claudecode

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// RequestDetection reports the signals used to recognize an official Claude
// Code request. Confirmed is true when the request carries every strong signal
// and comes from a native Claude Code entrypoint; the Claude executor treats
// such requests as native traffic and does not cloak them.
type RequestDetection = helps.ClaudeCodeRequestDetection

// DetectRequest classifies a Claude Messages request with the same logic the
// Claude executor uses. headers are the incoming request headers, payload is the
// original request body, and countTokens must be true for count_tokens
// requests (they do not carry metadata.user_id). cfg may be nil; when set, it
// supplies configured overrides such as the Claude Code version baseline and
// allowlisted native entrypoints, so pass the same configuration the service
// runs with to stay consistent with the executor.
func DetectRequest(headers http.Header, payload []byte, countTokens bool, cfg *sdkconfig.Config) RequestDetection {
	return helps.DetectClaudeCodeRequest(headers, payload, countTokens, cfg)
}
