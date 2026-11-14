package management

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// GetModelInventory returns a route-aware secret-free snapshot. It extends the
// existing management surface; configuration writes remain CCS-owned.
func (h *Handler) GetModelInventory(c *gin.Context) {
	now := time.Now().UTC()
	h.mu.Lock()
	manager := h.authManager
	state := h.modelRoutingStateHook
	h.mu.Unlock()
	activeState := modelrouting.ActiveStateV2{}
	if state != nil {
		activeState = state()
	}
	active := activeState.Identity
	projection := activeState.Projection
	if (active == nil) != (activeState.LoadedAt == nil) || (active == nil) != (projection == nil) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "active model-routing identity and activation timestamp are inconsistent"})
		return
	}
	if projection != nil && active == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "active model-routing identity is unavailable"})
		return
	}

	auths := make(map[string]*coreauth.Auth)
	if manager != nil {
		for _, auth := range manager.List() {
			if auth != nil {
				auths[auth.ID] = auth
			}
		}
	}
	registered := registry.GetGlobalRegistry().RegisteredRouteSnapshots()
	inventory := modelrouting.Inventory{
		SchemaVersion:      modelrouting.SchemaVersion,
		GeneratedAt:        now,
		Active:             active,
		ActivationLoadedAt: activeState.LoadedAt,
		BinaryProvenance: modelrouting.BinaryProvenance{
			Version: buildinfo.Version,
			Commit:  buildinfo.Commit,
			BuiltAt: buildinfo.BuildDate,
		},
		RoutingSchema: modelrouting.RoutingSchemaInfo{Version: modelrouting.SchemaVersion, Digest: modelrouting.SchemaDigest()},
		Aliases:       []modelrouting.InventoryAlias{},
	}
	// The activated view of the active providers is the authority this surface
	// serves (the model pipeline reads it as its cycle source). The published
	// projection only describes routes the pipeline already knew, so it is laid
	// over the registry view and never replaces it. An inconsistent registered
	// route therefore fails the inventory even under a valid projection: the
	// active projection keeps serving requests, and the route is repaired
	// instead of being hidden from the pipeline.
	registryModels, errRegistry := bootstrapInventoryModels(registered, auths, now)
	if errRegistry != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("build model inventory: %v", errRegistry)})
		return
	}
	inventory.DirectModels = registryModels
	if projection != nil {
		inventory.DirectModels = mergeRegistryRoutes(projectedInventoryModels(projection, registered, auths, now), registryModels)
		inventory.Aliases = projectedInventoryAliases(projection)
	}
	c.JSON(http.StatusOK, inventory)
}

// mergeRegistryRoutes adds every registered route the projection does not
// describe. Without it the inventory would only echo the previous publication,
// and a provider activated after the first one could never reach the pipeline.
func mergeRegistryRoutes(projected, registryModels []modelrouting.InventoryModel) []modelrouting.InventoryModel {
	merged := append(make([]modelrouting.InventoryModel, 0, len(projected)+len(registryModels)), projected...)
	byKey := make(map[modelrouting.ModelKeyJSON]int, len(merged))
	described := make(map[string]struct{})
	for index, model := range merged {
		byKey[model.ModelKey] = index
		for _, route := range model.Routes {
			described[route.RouteKey.RouteChannel+"\x00"+route.RuntimeModelID] = struct{}{}
		}
	}
	for _, model := range registryModels {
		routes := make([]modelrouting.InventoryRoute, 0, len(model.Routes))
		active := false
		for _, route := range model.Routes {
			if _, exists := described[route.RouteKey.RouteChannel+"\x00"+route.RuntimeModelID]; !exists {
				routes = append(routes, route)
				active = active || route.Selectable
			}
		}
		if len(routes) == 0 {
			continue
		}
		if index, exists := byKey[model.ModelKey]; exists {
			merged[index].Routes = append(merged[index].Routes, routes...)
			merged[index].Active = merged[index].Active || active
			for _, variant := range model.Variants {
				known := false
				for _, existing := range merged[index].Variants {
					known = known || existing.VariantKey.VariantID == variant.VariantKey.VariantID
				}
				if !known {
					merged[index].Variants = append(merged[index].Variants, variant)
				}
			}
			continue
		}
		model.Routes, model.Active = routes, active
		byKey[model.ModelKey] = len(merged)
		merged = append(merged, model)
	}
	for index := range merged {
		sortInventoryModel(&merged[index])
	}
	sortInventoryModels(merged)
	return merged
}

// sortInventoryModel orders one model's routes by channel and its variants by
// identity, the order the inventory contract requires of every model.
func sortInventoryModel(model *modelrouting.InventoryModel) {
	sort.Slice(model.Routes, func(i, j int) bool {
		return model.Routes[i].RouteKey.RouteChannel < model.Routes[j].RouteKey.RouteChannel
	})
	sort.Slice(model.Variants, func(i, j int) bool {
		return model.Variants[i].VariantKey.VariantID < model.Variants[j].VariantKey.VariantID
	})
}

// sortInventoryModels orders models by identity: catalog provider, then
// canonical model. Consumers reject an inventory in any other order.
func sortInventoryModels(models []modelrouting.InventoryModel) {
	sort.Slice(models, func(i, j int) bool {
		left, right := models[i].ModelKey, models[j].ModelKey
		if left.CatalogProviderID != right.CatalogProviderID {
			return left.CatalogProviderID < right.CatalogProviderID
		}
		return left.CanonicalModelID < right.CanonicalModelID
	})
}

func projectedInventoryModels(projection *modelrouting.Config, registered []registry.RegisteredRouteSnapshot, auths map[string]*coreauth.Auth, now time.Time) []modelrouting.InventoryModel {
	byRoute := make(map[string][]registry.RegisteredRouteSnapshot)
	for _, route := range registered {
		key := route.RouteChannel + "\x00" + route.RuntimeModelID
		byRoute[key] = append(byRoute[key], route)
	}
	models := make([]modelrouting.InventoryModel, 0, len(projection.DirectModels))
	for _, direct := range projection.DirectModels {
		model := modelrouting.InventoryModel{
			ModelKey: modelKeyJSON(direct.ModelKey), DisplayName: direct.DisplayName,
			Variants: make([]modelrouting.InventoryVariant, 0, len(direct.Variants)),
			Routes:   make([]modelrouting.InventoryRoute, 0, len(direct.Routes)),
		}
		for _, variant := range direct.Variants {
			model.Variants = append(model.Variants, modelrouting.InventoryVariant{
				VariantKey: modelrouting.VariantKeyJSON{
					ModelKey:  modelKeyJSON(variant.VariantKey.ModelKey),
					VariantID: variant.VariantKey.VariantID,
				},
				DisplayName: cloneOptionalString(variant.DisplayName),
				Protocols:   append([]string(nil), variant.Protocols...),
			})
		}
		for _, directRoute := range direct.Routes {
			allowed := make(map[string]struct{}, len(directRoute.CredentialRefs))
			for _, ref := range directRoute.CredentialRefs {
				allowed[ref.ID+"\x00"+ref.Kind] = struct{}{}
			}
			credentials := make([]modelrouting.InventoryCredential, 0, len(directRoute.CredentialRefs))
			for _, registeredRoute := range byRoute[directRoute.RouteKey.RouteChannel+"\x00"+directRoute.RuntimeModelID] {
				credential := inventoryCredential(registeredRoute, auths[registeredRoute.ClientID], now)
				identity := credential.CredentialRef.ID + "\x00" + credential.CredentialRef.Kind
				if _, exists := allowed[identity]; exists {
					credentials = append(credentials, credential)
				}
			}
			sortCredentials(credentials)
			selectable := directRoute.Selectable && hasSelectableCredential(credentials)
			reason := directRoute.SelectionReason
			if !selectable && directRoute.Selectable {
				reason = "no projected credential is currently selectable"
			}
			health := inventoryHealth(directRoute.Health)
			health.Selectable = health.Selectable && selectable
			if !health.Selectable && health.Status == "healthy" {
				health.Status = "blocked"
			}
			model.Routes = append(model.Routes, modelrouting.InventoryRoute{
				RouteKey: modelrouting.RouteKeyJSON{
					ModelKey:     modelKeyJSON(directRoute.RouteKey.ModelKey),
					RouteChannel: directRoute.RouteKey.RouteChannel,
				},
				CatalogRouteProviderID: directRoute.CatalogRouteProviderID,
				CatalogRouteModelID:    directRoute.CatalogRouteModelID,
				RuntimeModelID:         directRoute.RuntimeModelID,
				RouteSelector:          directRoute.RouteSelector,
				QuotaDomains:           append([]string(nil), directRoute.QuotaDomains...),
				Protocols:              append([]string(nil), directRoute.Protocols...),
				Restrictions:           inventoryRestrictions(directRoute.Restrictions),
				Health:                 health,
				Selectable:             selectable,
				SelectionReason:        reason,
				Credentials:            credentials,
			})
			model.Active = model.Active || selectable
		}
		models = append(models, model)
	}
	return models
}

func projectedInventoryAliases(projection *modelrouting.Config) []modelrouting.InventoryAlias {
	aliases := make([]modelrouting.InventoryAlias, 0, len(projection.Aliases))
	for _, sourceAlias := range projection.Aliases {
		alias := modelrouting.InventoryAlias{
			Name: sourceAlias.Name, TierID: sourceAlias.TierID, Selectable: sourceAlias.Selectable,
			Reason: sourceAlias.Reason, Members: make([]modelrouting.InventoryMember, 0, len(sourceAlias.Members)),
		}
		for _, sourceMember := range sourceAlias.Members {
			member := modelrouting.InventoryMember{
				ModelKey: modelKeyJSON(sourceMember.ModelKey), MemberRank: sourceMember.MemberRank,
				ModelScore: sourceMember.ModelScore, SelectionReason: sourceMember.SelectionReason,
				Candidates: make([]modelrouting.InventoryCandidate, 0, len(sourceMember.Candidates)),
			}
			for _, source := range sourceMember.Candidates {
				refs := make([]modelrouting.InventoryCredentialRef, len(source.CredentialRefs))
				for index, ref := range source.CredentialRefs {
					refs[index] = modelrouting.InventoryCredentialRef{ID: ref.ID, Kind: ref.Kind}
				}
				member.Candidates = append(member.Candidates, modelrouting.InventoryCandidate{
					RouteKey: modelrouting.RouteKeyJSON{
						ModelKey: modelKeyJSON(source.RouteKey.ModelKey), RouteChannel: source.RouteKey.RouteChannel,
					},
					CatalogRouteProviderID: source.CatalogRouteProviderID,
					CatalogRouteModelID:    source.CatalogRouteModelID, RuntimeModelID: source.RuntimeModelID,
					RouteSelector: source.RouteSelector, VariantID: cloneOptionalString(source.VariantID), RouteRank: source.RouteRank,
					QuotaDomains: append([]string(nil), source.QuotaDomains...), CredentialRefs: refs,
					Protocols: append([]string(nil), source.Protocols...), Health: inventoryHealth(source.Health),
					Restrictions: inventoryRestrictions(source.Restrictions), Pricing: inventoryPricing(source.Pricing),
					SelectionReason: source.SelectionReason,
				})
			}
			alias.Members = append(alias.Members, member)
		}
		aliases = append(aliases, alias)
	}
	return aliases
}

type bootstrapModel struct {
	model modelrouting.InventoryModel
	// routes indexes built.model.Routes by "channel\x00runtime-model" so route
	// lookups stay valid across slice growth: storing pointers would orphan them
	// on reallocation and silently drop later credential appends.
	routes map[string]int
}

// bootstrapRouteFacts is the catalog identity one registered route contributes
// to the inventory.
type bootstrapRouteFacts struct {
	routeKey               modelrouting.RouteKey
	displayName            string
	variantID              string
	catalogRouteProviderID string
	catalogRouteModelID    string
	protocols              []string
	selectionReason        string
}

// bootstrapFactsForRoute derives a route's catalog identity. Routes whose type
// has no config keys for catalog facts (claude-api-key, xai, gemini, codex) are
// not catalog-declared, but the route is real: its credential is live and the
// pipeline's models.dev source owns the canonical identity for it. It is
// surfaced under channel identity instead of hidden, which would strand
// generation one.
func bootstrapFactsForRoute(route registry.RegisteredRouteSnapshot, routeIndex int) (bootstrapRouteFacts, error) {
	routeKey, errRoute := route.RoutingKey(routeIndex)
	if errRoute != nil {
		return bootstrapRouteFacts{}, errRoute
	}
	if route.CatalogDeclarationOf() == registry.CatalogDeclarationNone {
		return bootstrapRouteFacts{
			routeKey:               routeKey,
			displayName:            route.Model.DisplayName,
			catalogRouteProviderID: route.RouteChannel,
			catalogRouteModelID:    route.RuntimeModelID,
			protocols:              route.RoutingProtocols(),
			selectionReason:        "route surfaced from its channel without catalog declaration",
		}, nil
	}
	return bootstrapRouteFacts{
		routeKey:               routeKey,
		displayName:            route.Model.DisplayName,
		variantID:              strings.TrimSpace(route.Model.VariantID),
		catalogRouteProviderID: route.Model.CatalogRouteProviderID,
		catalogRouteModelID:    route.Model.CatalogRouteModelID,
		protocols:              route.RoutingProtocols(),
		selectionReason:        "route facts were discovered from the runtime registry",
	}, nil
}

func bootstrapInventoryModels(registered []registry.RegisteredRouteSnapshot, auths map[string]*coreauth.Auth, now time.Time) ([]modelrouting.InventoryModel, error) {
	models := make(map[string]*bootstrapModel)
	for routeIndex, registeredRoute := range registered {
		facts, errFacts := bootstrapFactsForRoute(registeredRoute, routeIndex)
		if errFacts != nil {
			return nil, errFacts
		}
		auth := auths[registeredRoute.ClientID]
		if auth == nil {
			return nil, fmt.Errorf("registered route %d has no matching credential", routeIndex)
		}
		if auth.QuotaDomain == "" || strings.TrimSpace(auth.QuotaDomain) != auth.QuotaDomain || auth.QuotaDomain == "unknown" {
			// The credential is named by its hashed reference: the raw client id
			// is never serialized, and for a file-based auth it is a path.
			return nil, fmt.Errorf("registered route %d has no canonical quota domain (credential=%q provider=%q quotaDomain=%q channel=%q model=%q)", routeIndex, coreauth.CredentialReferenceID(registeredRoute.ClientID), auth.Provider, auth.QuotaDomain, registeredRoute.RouteChannel, registeredRoute.RuntimeModelID)
		}
		if kind := inventoryAuthKind(auth.AuthKind()); kind != "api_key" && kind != "oauth" {
			return nil, fmt.Errorf("registered route %d has no supported credential kind", routeIndex)
		}
		if len(facts.protocols) == 0 {
			// Model routing executes a route through a wire protocol; a route
			// whose model declares none (for example an OpenCode gateway model
			// served natively over Gemini) stays directly addressable but is
			// not a model-routing candidate, so the routing inventory omits it.
			continue
		}
		modelKey := modelKeyJSON(facts.routeKey.ModelKey)
		modelID := modelKey.CatalogProviderID + "\x00" + modelKey.CanonicalModelID
		built := models[modelID]
		if built == nil {
			display := strings.TrimSpace(facts.displayName)
			if display == "" {
				display = modelKey.CanonicalModelID
			}
			built = &bootstrapModel{model: modelrouting.InventoryModel{
				ModelKey: modelKey, DisplayName: display,
				Variants: []modelrouting.InventoryVariant{}, Routes: []modelrouting.InventoryRoute{},
			}, routes: make(map[string]int)}
			models[modelID] = built
		}
		if facts.variantID != "" {
			appendBootstrapVariant(&built.model, registeredRoute.Model, facts.variantID, append([]string(nil), facts.protocols...))
		}
		routeID := registeredRoute.RouteChannel + "\x00" + registeredRoute.RuntimeModelID
		routeIndexExisting, exists := built.routes[routeID]
		if !exists {
			built.model.Routes = append(built.model.Routes, modelrouting.InventoryRoute{
				RouteKey: modelrouting.RouteKeyJSON{
					ModelKey: modelKey, RouteChannel: registeredRoute.RouteChannel,
				},
				CatalogRouteProviderID: facts.catalogRouteProviderID,
				CatalogRouteModelID:    facts.catalogRouteModelID,
				RuntimeModelID:         registeredRoute.RuntimeModelID,
				RouteSelector:          modelrouting.SelectorForRoute(facts.routeKey, registeredRoute.RuntimeModelID),
				Protocols:              facts.protocols,
				Restrictions:           []modelrouting.InventoryRestriction{},
				Credentials:            []modelrouting.InventoryCredential{},
				SelectionReason:        facts.selectionReason,
			})
			routeIndexExisting = len(built.model.Routes) - 1
			built.routes[routeID] = routeIndexExisting
		} else if route := built.model.Routes[routeIndexExisting]; route.CatalogRouteProviderID != facts.catalogRouteProviderID ||
			route.CatalogRouteModelID != facts.catalogRouteModelID ||
			!equalStrings(route.Protocols, facts.protocols) {
			return nil, fmt.Errorf("registered route %d conflicts with an existing catalog route", routeIndex)
		}
		built.model.Routes[routeIndexExisting].Credentials = append(
			built.model.Routes[routeIndexExisting].Credentials, inventoryCredential(registeredRoute, auth, now))
	}

	result := make([]modelrouting.InventoryModel, 0, len(models))
	for _, built := range models {
		for index := range built.model.Routes {
			route := &built.model.Routes[index]
			sortCredentials(route.Credentials)
			route.QuotaDomains = quotaDomains(route.Credentials)
			if containsString(route.QuotaDomains, "unknown") {
				return nil, fmt.Errorf("route %s has an unknown quota domain", route.RouteKey.RouteChannel)
			}
			route.Selectable = hasSelectableCredential(route.Credentials)
			route.Health = modelrouting.InventoryHealth{
				Status: boolStatus(route.Selectable, "healthy", "unknown"), Selectable: route.Selectable,
				ObservedAt: now.Format(time.RFC3339Nano), LatencyMS: nil,
			}
			if !route.Selectable {
				route.SelectionReason = "no credential is currently selectable"
			}
			built.model.Active = built.model.Active || route.Selectable
		}
		sortInventoryModel(&built.model)
		result = append(result, built.model)
	}
	sortInventoryModels(result)
	return result, nil
}

// routeProtocols copies a route's wire protocols into the inventory's array
// contract: a route without protocols is an empty list, never JSON null.
func routeProtocols(protocols []string) []string {
	return append(make([]string, 0, len(protocols)), protocols...)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func inventoryCredential(route registry.RegisteredRouteSnapshot, auth *coreauth.Auth, now time.Time) modelrouting.InventoryCredential {
	refID := coreauth.CredentialReferenceID(route.ClientID)
	kind := "unknown"
	if auth != nil {
		kind = inventoryAuthKind(auth.AuthKind())
	}
	suspended := strings.TrimSpace(route.SuspensionReason) != ""
	suspensionReason := optionalTrimmed(route.SuspensionReason)
	quotaBlocked := route.QuotaBlocked
	var quotaReason, remaining, resetsAt, resumesAt *string
	observedAt := route.LastUpdated
	selectable := auth != nil
	status := "unknown"
	quotaStatus := "unknown"
	if auth != nil {
		if auth.UpdatedAt.After(observedAt) {
			observedAt = auth.UpdatedAt
		}
		// Availability follows request-time selection, so a cooldown or quota
		// window ends at its recovery instant instead of at the next success.
		availability := coreauth.AvailabilityForModel(auth, route.RuntimeModelID, now)
		quota := auth.Quota
		statusMessage := auth.StatusMessage
		if modelState := auth.ModelStates[route.RuntimeModelID]; modelState != nil {
			quota = modelState.Quota
			if !auth.Disabled && auth.Status != coreauth.StatusDisabled {
				statusMessage = modelState.StatusMessage
			}
		}
		quotaBlocked = availability.QuotaCooldown
		if quotaBlocked {
			quotaReason = optionalTrimmed(quota.Reason)
			resetsAt = optionalInstant(availability.RecoverAt)
		}
		if availability.Blocked && !availability.QuotaCooldown {
			if suspensionReason == nil {
				suspensionReason = optionalTrimmed(statusMessage)
			}
			if !suspended {
				resumesAt = optionalInstant(availability.RecoverAt)
			}
			suspended = true
		}
		if value := strings.TrimSpace(quota.Signals["remaining"]); value != "" {
			remaining = &value
		}
		selectable = !availability.Blocked && !suspended
		status = boolStatus(selectable, "healthy", "blocked")
		quotaStatus = boolStatus(quotaBlocked, "blocked", "available")
	} else if route.QuotaResetsAt != nil {
		resetsAt = optionalInstant(*route.QuotaResetsAt)
	}
	if observedAt.IsZero() {
		observedAt = now
	}
	return modelrouting.InventoryCredential{
		CredentialRef: modelrouting.InventoryCredentialRef{ID: refID, Kind: kind},
		QuotaDomain:   credentialQuotaDomainForInventory(auth),
		Health: modelrouting.InventoryHealth{
			Status: status, Selectable: selectable, ObservedAt: observedAt.UTC().Format(time.RFC3339Nano), LatencyMS: nil,
		},
		Quota: modelrouting.InventoryQuota{
			Status: quotaStatus, Remaining: remaining, ResetsAt: resetsAt, Reason: quotaReason,
		},
		Suspension: modelrouting.InventorySuspension{
			Active: suspended, Reason: suspensionReason, ResumesAt: resumesAt,
		},
		Restrictions: []modelrouting.InventoryRestriction{},
	}
}

func appendBootstrapVariant(model *modelrouting.InventoryModel, info *registry.ModelInfo, variantID string, protocols []string) {
	for _, variant := range model.Variants {
		if variant.VariantKey.VariantID == variantID {
			return
		}
	}
	display := strings.TrimSpace(info.DisplayName)
	model.Variants = append(model.Variants, modelrouting.InventoryVariant{
		VariantKey: modelrouting.VariantKeyJSON{
			ModelKey:  model.ModelKey,
			VariantID: variantID,
		},
		DisplayName: optionalTrimmed(display), Protocols: protocols,
	})
}

func inventoryRestrictions(values []modelrouting.Restriction) []modelrouting.InventoryRestriction {
	result := make([]modelrouting.InventoryRestriction, 0, len(values))
	for _, value := range values {
		result = append(result, modelrouting.InventoryRestriction{
			RuleID: value.RuleID, ConfigPath: value.ConfigPath, Active: value.Active, Reason: value.Reason,
		})
	}
	return result
}

func inventoryHealth(value modelrouting.Health) modelrouting.InventoryHealth {
	return modelrouting.InventoryHealth{
		Status: value.Status, Selectable: value.Selectable, ObservedAt: value.ObservedAt, LatencyMS: cloneInt64(value.LatencyMS),
	}
}

func inventoryPricing(value *modelrouting.Pricing) *modelrouting.InventoryPricing {
	if value == nil {
		return nil
	}
	result := &modelrouting.InventoryPricing{
		Currency: value.Currency,
		Unit:     value.Unit,
		SourceID: value.SourceID,
		Entries:  make([]modelrouting.InventoryPricingEntry, len(value.Entries)),
	}
	for index, entry := range value.Entries {
		result.Entries[index] = modelrouting.InventoryPricingEntry{
			Name:       entry.Name,
			Amount:     entry.Amount,
			TierType:   cloneOptionalString(entry.TierType),
			TierSize:   cloneInt64(entry.TierSize),
			ContextKey: cloneOptionalString(entry.ContextKey),
		}
	}
	return result
}

func modelKeyJSON(value modelrouting.ModelKey) modelrouting.ModelKeyJSON {
	return modelrouting.ModelKeyJSON{CatalogProviderID: value.CatalogProviderID, CanonicalModelID: value.CanonicalModelID}
}

func credentialQuotaDomainForInventory(auth *coreauth.Auth) string {
	if auth != nil && strings.TrimSpace(auth.QuotaDomain) != "" {
		return strings.TrimSpace(auth.QuotaDomain)
	}
	return "unknown"
}

func inventoryAuthKind(value string) string {
	if value == coreauth.AuthKindAPIKey {
		return "api_key"
	}
	if value == coreauth.AuthKindOAuth {
		return "oauth"
	}
	return canonicalFact(value)
}

func quotaDomains(credentials []modelrouting.InventoryCredential) []string {
	values := make([]string, 0, len(credentials))
	for _, credential := range credentials {
		values = append(values, credential.QuotaDomain)
	}
	sort.Strings(values)
	return uniqueStrings(values)
}

func sortCredentials(credentials []modelrouting.InventoryCredential) {
	sort.Slice(credentials, func(i, j int) bool {
		if credentials[i].CredentialRef.ID != credentials[j].CredentialRef.ID {
			return credentials[i].CredentialRef.ID < credentials[j].CredentialRef.ID
		}
		return credentials[i].CredentialRef.Kind < credentials[j].CredentialRef.Kind
	})
}

func hasSelectableCredential(credentials []modelrouting.InventoryCredential) bool {
	for _, credential := range credentials {
		if credential.Health.Selectable && credential.Quota.Status != "blocked" && !credential.Suspension.Active {
			return true
		}
	}
	return false
}

func canonicalFact(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func optionalTrimmed(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func optionalInstant(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func uniqueStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func boolStatus(value bool, whenTrue, whenFalse string) string {
	if value {
		return whenTrue
	}
	return whenFalse
}
