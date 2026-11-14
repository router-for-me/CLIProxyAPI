package management

import (
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// routingInventoryCredential uses the same cooldown decision as request routing.
// Account IDs and provider-supplied error text must not cross this boundary.
func routingInventoryCredential(route registry.RegisteredRouteSnapshot, auth *coreauth.Auth, quotaDomain string, now time.Time) modelrouting.InventoryCredential {
	report := modelrouting.InventoryCredential{
		CredentialRef: modelrouting.InventoryCredentialRef{ID: coreauth.CredentialReferenceID(route.ClientID), Kind: "unknown"},
		QuotaDomain:   quotaDomain,
		Health:        modelrouting.InventoryHealth{Status: "unknown", ObservedAt: now.UTC().Format(time.RFC3339Nano)},
		Quota:         modelrouting.InventoryQuota{Status: "unknown"},
		Restrictions:  []modelrouting.InventoryRestriction{},
	}
	if auth == nil {
		return report
	}
	report.Revision = auth.RoutingCredentialRevision()
	switch auth.AuthKind() {
	case coreauth.AuthKindAPIKey:
		report.CredentialRef.Kind = "api_key"
	case coreauth.AuthKindOAuth:
		report.CredentialRef.Kind = "oauth"
	}
	availability := coreauth.AvailabilityForModel(auth, route.RuntimeModelID, now)
	suspended := route.SuspensionReason != "" && route.SuspensionReason != "quota" && route.SuspensionReason != "credential_quota"
	report.Health.Selectable = !availability.Blocked && !suspended
	report.Health.Status = "blocked"
	if report.Health.Selectable {
		report.Health.Status = "healthy"
	}
	report.Suspension.Active = suspended || (availability.Blocked && !availability.QuotaCooldown)
	if suspended {
		// Only expose local classes, never provider-supplied failure text.
		reason := "suspended"
		switch route.SuspensionReason {
		case "manual", "disabled", "unauthorized":
			reason = route.SuspensionReason
		}
		report.Suspension.Reason = &reason
	}
	report.Quota.Status = "available"
	if availability.QuotaCooldown {
		report.Quota.Status = "blocked"
	}
	if !availability.RecoverAt.IsZero() {
		recovery := availability.RecoverAt.UTC().Format(time.RFC3339Nano)
		if availability.QuotaCooldown {
			report.Quota.ResetsAt = &recovery
		} else if !suspended && availability.Blocked {
			report.Suspension.ResumesAt = &recovery
		}
	}
	quota := auth.Quota
	if model := auth.ModelStates[route.RuntimeModelID]; model != nil {
		quota = model.Quota
	}
	if remaining := strings.TrimSpace(quota.Signals["remaining"]); remaining != "" {
		report.Quota.Remaining = &remaining
	}
	return report
}
