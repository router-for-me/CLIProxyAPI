package auth

import (
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// Antigravity meters quota per pool, not per model. retrieveUserQuotaSummary names
// each bucket's pool through its bucketId: "3p-*" is shared by Claude and GPT models,
// "gemini-<window>" is the default Gemini pool, and "gemini-<name>-*" (argon, carbon)
// is a Gemini sub-pool metered apart from the default one.
const (
	antigravityPoolPrefix  = "antigravity:"
	antigravityThirdParty  = antigravityPoolPrefix + "3p"
	antigravityGeminiPool  = antigravityPoolPrefix + "gemini"
	antigravityDailyBase   = "https://daily-cloudcode-pa.googleapis.com"
	antigravityQuotaPath   = "/v1internal:retrieveUserQuotaSummary"
	antigravityTokenMargin = 5 * time.Minute
)

// antigravityRoutingPools are the pools routing status can rank without a model.
// Gemini comes first: it is the default when a status request names neither.
var antigravityRoutingPools = []struct {
	RoutingPool
	window string
}{
	{RoutingPool{Key: "gemini", Label: "Gemini models"}, antigravityGeminiPool},
	{RoutingPool{Key: "claude-gpt", Label: "Claude and GPT models"}, antigravityThirdParty},
}

// antigravityWindowSeconds lists the bucket window spellings the dashboard recognizes.
// Seconds only order windows of one account; they never set a reset deadline.
var antigravityWindowSeconds = map[string]int64{
	"5h":        18000,
	"five-hour": 18000,
	"five_hour": 18000,
	"daily":     86400,
	"day":       86400,
	"weekly":    604800,
	"week":      604800,
	"monthly":   2592000,
	"month":     2592000,
}

// antigravityQuotaPool maps a bucket to its routing pool, or "" for an unknown pool.
func antigravityQuotaPool(bucketID string) string {
	if strings.HasPrefix(bucketID, "3p-") {
		return antigravityThirdParty
	}
	rest, ok := strings.CutPrefix(bucketID, "gemini-")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "-")
	if _, isWindow := antigravityWindowSeconds[name]; isWindow || name == "" {
		return antigravityGeminiPool
	}
	for _, c := range name {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	return antigravityGeminiPool + "-" + name
}

// antigravityPoolApplies matches upstream Antigravity model IDs such as
// "claude-opus-4-6-thinking", "gpt-oss-120b-medium" and "gemini-4-argon-high".
func antigravityPoolApplies(pool, model string) bool {
	model = strings.ToLower(canonicalModelKey(model))
	switch pool {
	case antigravityThirdParty:
		return strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "gpt-")
	case antigravityGeminiPool:
		return strings.HasPrefix(model, "gemini-")
	}
	name, ok := strings.CutPrefix(pool, antigravityGeminiPool+"-")
	if !ok || !strings.HasPrefix(model, "gemini-") {
		return false
	}
	for _, part := range strings.Split(model, "-") {
		if part == name {
			return true
		}
	}
	return false
}

func antigravityField(r ojson.Result, camel, snake string) ojson.Result {
	if v := r.Get(camel); v.Exists() {
		return v
	}
	return r.Get(snake)
}

// parseAntigravityRoutingQuota reads retrieveUserQuotaSummary. It returns the windows,
// whether any bucket of a known pool was present, and whether any such bucket was malformed.
func parseAntigravityRoutingQuota(root ojson.Result, now time.Time) ([]RoutingWindow, bool, bool) {
	var buckets []ojson.Result
	malformed := false
	collect := func(list ojson.Result) {
		if !list.Exists() || list.Type == ojson.Null {
			return
		}
		if !list.IsArray() {
			malformed = true
			return
		}
		list.ForEach(func(_, b ojson.Result) bool {
			buckets = append(buckets, b)
			return len(buckets) < 64
		})
	}
	groups := root.Get("groups")
	if groups.Exists() && groups.Type != ojson.Null && !groups.IsArray() {
		malformed = true
	}
	groups.ForEach(func(_, g ojson.Result) bool {
		collect(g.Get("buckets"))
		return true
	})
	collect(root.Get("buckets"))

	windows := make([]RoutingWindow, 0, len(buckets))
	recognized := false
	for _, b := range buckets {
		idValue := antigravityField(b, "bucketId", "bucket_id")
		id := strings.ToLower(strings.TrimSpace(idValue.String()))
		pool := antigravityQuotaPool(id)
		if idValue.Type != ojson.String || pool == "" {
			continue
		}
		recognized = true
		// The dashboard marks a bucket disabled when another window of its pool is spent.
		if b.Get("disabled").Type == ojson.True {
			continue
		}
		seconds, okWindow := antigravityWindowSeconds[strings.ToLower(strings.TrimSpace(b.Get("window").String()))]
		remaining, okRemaining := quotaNumber(antigravityField(b, "remainingFraction", "remaining_fraction"))
		if !okWindow || !okRemaining {
			malformed = true
			continue
		}
		remaining = min(remaining, 1)
		reset := antigravityField(b, "resetTime", "reset_time")
		// A full bucket without a reset has not started this period.
		if (!reset.Exists() || reset.Type == ojson.Null) && remaining == 1 {
			continue
		}
		resetAt, errParse := time.Parse(time.RFC3339Nano, reset.String())
		if reset.Type != ojson.String || errParse != nil || resetAt.After(now.Add(32*24*time.Hour)) {
			malformed = true
			continue
		}
		windows = append(windows, RoutingWindow{Key: id, ResetsAt: resetAt, UsedPercent: (1 - remaining) * 100, seconds: seconds, model: pool})
	}
	return windows, recognized, malformed
}

// antigravityQuotaBaseURL mirrors the executor's inference endpoint so the probe reads
// the meter that the routed requests consume. Other hosts can meter the same account differently.
func antigravityQuotaBaseURL(a *Auth) string {
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["base_url"]); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	if v, ok := a.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSuffix(strings.TrimSpace(v), "/")
	}
	return antigravityDailyBase
}

// antigravityQuotaBody returns the request body, scoped to the credential's project when known.
func antigravityQuotaBody(a *Auth) string {
	project, _ := a.Metadata["project_id"].(string)
	doc := ojson.NewDocString(`{}`)
	if project = strings.TrimSpace(project); project != "" {
		if errSet := doc.Set("project", project); errSet != nil {
			return `{}`
		}
	}
	return doc.String()
}

// antigravityTokenUsable reports whether the executor can send the probe without refreshing.
func antigravityTokenUsable(a *Auth, now time.Time) bool {
	expiry, ok := a.ExpirationTime()
	return ok && expiry.After(now.Add(antigravityTokenMargin))
}
