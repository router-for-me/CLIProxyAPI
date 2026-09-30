package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"strings"
)

// RepairResponsesToolItemIDs migrates replayed tool item ids in one raw
// Responses body. A client stores output items exactly as the proxy emitted
// them, so an id minted for one item type is replayed verbatim on the next turn
// and a strict upstream rejects it.
//
// The feature gate is the same emergency switch the HTTP path uses: with the
// bridge disabled the body is returned untouched. A nil config means the
// default, which is enabled. The helper only gates; the prefix table, the route
// policy, and the history walk stay in the core package.
func RepairResponsesToolItemIDs(cfg *config.Config, payload []byte) ([]byte, error) {
	if cfg != nil && !cfg.ResponsesTools.FeatureEnabled() {
		return payload, nil
	}
	out, _, err := responsestools.RepairInputItemIDs(payload)
	return out, err
}

// IsResponsesFamilyFormat reports whether one client format speaks the
// Responses protocol. Only those bodies carry input items whose ids this
// package may migrate; a chat-shaped body is left alone.
func IsResponsesFamilyFormat(format sdktranslator.Format) bool {
	switch sdktranslator.Format(strings.ToLower(strings.TrimSpace(format.String()))) {
	case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex:
		return true
	default:
		return false
	}
}
