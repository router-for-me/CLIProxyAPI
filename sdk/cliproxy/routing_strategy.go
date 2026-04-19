package cliproxy

import "strings"

func normalizeRoutingStrategyName(strategy string) string {
	normalized := strings.ToLower(strings.TrimSpace(strategy))
	switch normalized {
	case "fill-first", "fillfirst", "ff":
		return "fill-first"
	case "oauth-quota-burst-sync-sticky", "quota-burst-sync-sticky", "burst-sync-sticky", "oauth-burst-sync-sticky":
		return "oauth-quota-burst-sync-sticky"
	case "oauth-quota-reserve-staggered", "quota-reserve-staggered", "reserve-staggered", "oauth-reserve-staggered":
		return "oauth-quota-reserve-staggered"
	case "oauth-quota-weekly-guarded-sticky", "quota-weekly-guarded-sticky", "weekly-guarded-sticky", "oauth-weekly-guarded-sticky":
		return "oauth-quota-weekly-guarded-sticky"
	default:
		return "round-robin"
	}
}
