package auth

import "strings"

// codexSubscriptionAttributeKeys are the metadata keys mirrored into runtime
// attributes for Codex auths.
var codexSubscriptionAttributeKeys = []string{"plan_type", "subscription_active_until"}

// ApplyCodexSubscriptionAttributes mirrors the Codex subscription fields stored
// in metadata into the attributes the runtime reads. Codex model-catalog
// selection keys off Attributes["plan_type"], so every code path that builds a
// Codex Auth must keep this in sync. Routing them all through this single
// helper avoids the per-site drift that otherwise leaves Free/Plus/Team
// accounts defaulting to the Pro catalog until a file reload.
func ApplyCodexSubscriptionAttributes(auth *Auth) {
	if auth == nil || auth.Metadata == nil {
		return
	}
	for _, key := range codexSubscriptionAttributeKeys {
		value, ok := auth.Metadata[key].(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if auth.Attributes == nil {
			auth.Attributes = make(map[string]string)
		}
		auth.Attributes[key] = trimmed
	}
}
