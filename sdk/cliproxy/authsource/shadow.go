package authsource

import (
	"context"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// reconcileShadowAuths removes config-synthesized auths that duplicate a
// catalog identity. Non-config auths are never touched. A shadow whose only
// executable base_url would disappear with it is kept and counted unresolved.
func reconcileShadowAuths(ctx context.Context, target Target, shadow, desired []*coreauth.Auth, out *Result) {
	if out == nil || target == nil || len(shadow) == 0 {
		return
	}

	catalogLabels := make(map[string]struct{}, len(desired)*2)
	catalogWithBase := make(map[string]struct{}, len(desired)*2)
	catalogOwned := make(map[string]struct{}, len(desired)*2)
	for _, auth := range desired {
		if auth == nil {
			continue
		}
		labels := identityLabels(auth)
		hasBase := strings.TrimSpace(attrGet(auth.Attributes, attrBaseURL)) != ""
		owned := strings.HasPrefix(auth.ID, OwnedIDPrefix)
		for _, label := range labels {
			catalogLabels[label] = struct{}{}
			if hasBase {
				catalogWithBase[label] = struct{}{}
			}
			if owned {
				catalogOwned[label] = struct{}{}
			}
		}
	}

	for _, auth := range shadow {
		if !isConfigSynthesizedAuth(auth) {
			continue
		}
		label := identityLabelForLog(auth)
		labels := identityLabels(auth)
		if !catalogHasLabel(catalogLabels, labels) {
			out.ShadowUnresolved++
			continue
		}
		shadowBase := strings.TrimSpace(attrGet(auth.Attributes, attrBaseURL))
		if shadowBase != "" && !catalogHasLabel(catalogWithBase, labels) {
			out.ShadowUnresolved++
			continue
		}
		if catalogHasLabel(catalogOwned, labels) {
			log.WithFields(log.Fields{
				"event":               "cred_dual_registration",
				"provider":            label,
				"shadow_auth_id":      auth.ID,
				"catalog_auth_prefix": OwnedIDPrefix,
			}).Warn("config-synthesized shadow dual-registered with catalog credential")
		}
		target.Remove(ctx, auth.ID)
		out.ShadowRemoved++
	}
}

// isConfigSynthesizedAuth reports a config-synthesized API-key auth, including
// the keyless OpenAI-compat fallback. OAuth is never config synthesis.
func isConfigSynthesizedAuth(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if auth.AuthSourceKind() != coreauth.AuthSourceConfig {
		return false
	}
	if auth.AuthKind() == coreauth.AuthKindOAuth {
		return false
	}
	return true
}

func identityLabels(auth *coreauth.Auth) []string {
	if auth == nil {
		return nil
	}
	seen := make(map[string]struct{}, 3)
	var labels []string
	add := func(raw string) {
		label := strings.ToLower(strings.TrimSpace(raw))
		if label == "" {
			return
		}
		if _, ok := seen[label]; ok {
			return
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	add(attrGet(auth.Attributes, attrCatalogProvider))
	add(attrGet(auth.Attributes, attrCompatName))
	if isPairableExecutorLabel(auth.Provider) {
		add(auth.Provider)
	}
	return labels
}

func isPairableExecutorLabel(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	return p != "" && p != "openai-compatibility" && !strings.HasPrefix(p, "openai-compatible-")
}

func identityLabelForLog(auth *coreauth.Auth) string {
	labels := identityLabels(auth)
	if len(labels) == 0 {
		return ""
	}
	return labels[0]
}

func catalogHasLabel(catalog map[string]struct{}, labels []string) bool {
	for _, label := range labels {
		if _, ok := catalog[label]; ok {
			return true
		}
	}
	return false
}
