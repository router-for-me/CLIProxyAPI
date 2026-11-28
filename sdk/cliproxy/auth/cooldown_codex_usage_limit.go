package auth

import (
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// codexUsageLimitMinCooldown keeps short cooldowns retryable: a client that
// waits a few minutes loses less than one that stops and needs a resume.
const codexUsageLimitMinCooldown = 5 * time.Minute

// codexUsageLimit is what the Codex CLI needs to recognise a usage limit and
// report its reset: the body's type and resets_at.
type codexUsageLimit struct {
	resetsAt     int64
	resetSeconds int64
	planType     string
}

// codexUsageLimit reports whether the cooldown was caused by an upstream
// usage_limit_reached refusal and lasts long enough to be reported as one.
// The reset is the cooldown's own deadline, which Retry-After already reports.
func (e *modelCooldownError) codexUsageLimit() (codexUsageLimit, bool) {
	if e == nil || e.cause == nil || e.resetIn < codexUsageLimitMinCooldown {
		return codexUsageLimit{}, false
	}
	raw := strings.TrimSpace(e.cause.Error())
	if idx := strings.Index(raw, "{"); idx > 0 {
		raw = raw[idx:]
	}
	if !ojson.Valid(raw) {
		return codexUsageLimit{}, false
	}
	parsed := ojson.Parse(raw)
	for _, node := range []ojson.Result{parsed.Get("error"), parsed, parsed.Get("body.error"), parsed.Get("response.error")} {
		if !node.IsObject() || !strings.EqualFold(strings.TrimSpace(node.Get("type").String()), "usage_limit_reached") {
			continue
		}
		seconds := e.resetSeconds()
		return codexUsageLimit{
			resetsAt:     time.Now().Unix() + seconds,
			resetSeconds: seconds,
			planType:     strings.TrimSpace(node.Get("plan_type").String()),
		}, true
	}
	return codexUsageLimit{}, false
}

func (l codexUsageLimit) addBodyFields(errorBody map[string]any) {
	errorBody["type"] = "usage_limit_reached"
	errorBody["resets_at"] = l.resetsAt
	errorBody["resets_in_seconds"] = l.resetSeconds
	if l.planType != "" {
		errorBody["plan_type"] = l.planType
	}
}
