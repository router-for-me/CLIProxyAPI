package helps

import (
	"bytes"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// ObserveUsagePayload retains only parsed token fields for a later terminal outcome.
func (r *UsageReporter) ObserveUsagePayload(payload []byte) {
	if r == nil {
		return
	}
	payload = JSONPayload(payload)
	if len(payload) == 0 || (!bytes.Contains(payload, []byte(`"usage"`)) && !bytes.Contains(payload, []byte(`"usageMetadata"`)) && !bytes.Contains(payload, []byte(`"usage_metadata"`)) && !bytes.Contains(payload, []byte(`"service_tier"`))) {
		return
	}

	detail := r.usageDetailFromPayload(payload)
	if !detail.UsagePresent && detail.ResponseServiceTier == "" {
		return
	}

	r.observedMu.Lock()
	if detail.UsagePresent {
		if r.provider == "claude" && r.observed.UsagePresent {
			r.observed = MergeStreamUsageDetail(r.observed, detail)
		} else {
			r.observed = detail
		}
	} else {
		r.observed.ResponseServiceTier = detail.ResponseServiceTier
	}
	r.observedMu.Unlock()
}

func (r *UsageReporter) usageDetailFromPayload(payload []byte) usage.Detail {
	var detail usage.Detail
	switch r.provider {
	case "claude":
		detail, _ = ParseClaudeStreamUsage(payload)
	case "gemini", "gemini-cli":
		detail = ParseGeminiUsage(payload)
	case "interactions", "interactions-response":
		detail = ParseInteractionsUsage(payload)
	case "antigravity":
		detail = ParseAntigravityUsage(payload)
	default:
		detail, _ = ParseCodexUsage(payload)
		if !detail.UsagePresent {
			detail = ParseOpenAIUsage(payload)
		}
	}
	return detail
}
