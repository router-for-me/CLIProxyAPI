package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	thinkingclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	thinkingcodex "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	thinkingopenai "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/openai"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	probeRouteSelectorHeader           = "X-CLIProxy-Route-Selector"
	probeVariantIDHeader               = "X-CLIProxy-Variant-ID"
	effectiveModelHeader               = "X-CLIProxy-Effective-Model"
	quotaDomainHeader                  = "X-CLIProxy-Quota-Domain"
	credentialReferenceHeader          = "X-CLIProxy-Credential-Ref"
	projectionDigestHeader             = "X-CLIProxy-Projection-Digest"
	failureModeHeader                  = "X-CLIProxy-Failure-Mode"
	attemptsHeader                     = "X-CLIProxy-Attempts"
	failoverEvidenceHeader             = "X-CLIProxy-Failover-Evidence"
	allowedAuthIDsMetadataKey          = "cliproxy.model_routing.allowed_auth_ids"
	modelRoutingMetadataKey            = "cliproxy.model_routing.fail_fast"
	routingIndexMetadataKey            = "cliproxy.model_routing.candidate.index"
	routingChannelMetadataKey          = "cliproxy.model_routing.candidate.channel"
	routingModelMetadataKey            = "cliproxy.model_routing.candidate.runtime_model_id"
	routingAliasMetadataKey            = "cliproxy.model_routing.alias"
	routingSelectorMetadataKey         = "cliproxy.model_routing.route_selector"
	routingProjectionDigestMetadataKey = "cliproxy.model_routing.projection_digest"
)

// ModelRoutingResponseHeaderNames returns the trusted local evidence headers
// that browser clients must be allowed to read through CORS.
func ModelRoutingResponseHeaderNames() []string {
	return []string{
		probeRouteSelectorHeader,
		probeVariantIDHeader,
		effectiveModelHeader,
		quotaDomainHeader,
		credentialReferenceHeader,
		projectionDigestHeader,
		failureModeHeader,
		attemptsHeader,
		failoverEvidenceHeader,
	}
}

type modelRoutingTable struct {
	aliases          map[string][]RoutingCandidate
	declaredAliases  map[string]struct{}
	routes           map[string]RoutingCandidate
	variants         map[string]map[string]modelrouting.Variant
	failurePolicy    modelrouting.FailurePolicy
	projectionDigest string
	projected        bool
}

// RoutingCandidate is one immutable option in the already-ranked candidate chain.
// Movement between candidates is permitted only by the projection's typed failure policy.
type RoutingCandidate struct {
	Channel                string
	RuntimeModelID         string
	Alias                  string
	RouteSelector          string
	VariantID              *string
	VariantReasoningOption *string
	CredentialRefs         []modelrouting.CredentialRef
	Protocols              []string
	CatalogProviderID      string
	CanonicalModelID       string
	Pricing                *modelrouting.Pricing
	ProjectionDigest       string
	bootstrap              bool
}

// PreparedModelRouting is an immutable runtime candidate built without changing
// the active request path. Only Service activates a prepared value after durable
// configuration publication succeeds.
type PreparedModelRouting struct {
	table *modelRoutingTable
}

// PrepareModelRouting validates registry/executor/auth facts and builds a new
// runtime table without changing the active table.
func (m *Manager) PrepareModelRouting(projection *modelrouting.Config) (*PreparedModelRouting, error) {
	if m == nil {
		return nil, fmt.Errorf("model routing manager is nil")
	}
	if projection == nil {
		return &PreparedModelRouting{table: compileModelRouting(nil)}, nil
	}
	if errValidate := projection.Validate(); errValidate != nil {
		return nil, fmt.Errorf("validate model routing projection: %w", errValidate)
	}
	if errValidate := validateRoutingVariantExecutors(projection); errValidate != nil {
		return nil, fmt.Errorf("validate model routing variant executors: %w", errValidate)
	}
	if errValidate := m.validateModelRoutingRuntime(projection, time.Now()); errValidate != nil {
		return nil, fmt.Errorf("validate model routing runtime: %w", errValidate)
	}
	return &PreparedModelRouting{table: compileModelRouting(projection)}, nil
}

// ActivatePreparedModelRouting performs the single in-memory swap. Callers must
// have already persisted the exact configuration bytes represented by prepared.
func (m *Manager) ActivatePreparedModelRouting(prepared *PreparedModelRouting) error {
	if m == nil {
		return fmt.Errorf("model routing manager is nil")
	}
	if prepared == nil || prepared.table == nil {
		return fmt.Errorf("prepared model routing is nil")
	}
	m.modelRouting.Store(prepared.table)
	return nil
}

func validateRoutingVariantExecutors(projection *modelrouting.Config) error {
	if projection == nil {
		return nil
	}
	for modelIndex, model := range projection.DirectModels {
		for variantIndex, variant := range model.Variants {
			if variant.ReasoningOption == nil {
				continue
			}
			for _, protocol := range variant.Protocols {
				if _, err := routingThinkingConfig(protocol, *variant.ReasoningOption); err != nil {
					return fmt.Errorf("direct-models[%d].variants[%d] protocol %q: %w", modelIndex, variantIndex, protocol, err)
				}
			}
		}
	}
	return nil
}

func (m *Manager) validateModelRoutingRuntime(projection *modelrouting.Config, now time.Time) error {
	if projection == nil {
		return nil
	}
	registered := registry.GetGlobalRegistry().RegisteredRouteSnapshots()
	for modelIndex, model := range projection.DirectModels {
		for routeIndex, route := range model.Routes {
			if !route.Selectable {
				continue
			}
			path := fmt.Sprintf("direct-models[%d].routes[%d]", modelIndex, routeIndex)
			channel := strings.ToLower(route.RouteKey.RouteChannel)
			if _, exists := m.Executor(channel); !exists {
				return fmt.Errorf("%s.route-key.route-channel: executor %q is not registered", path, channel)
			}
			if route.RouteSelector != modelrouting.SelectorForRoute(route.RouteKey, route.RuntimeModelID) {
				return fmt.Errorf("%s.route-selector: differs from the live route identity", path)
			}

			credentialSet := make(map[string]modelrouting.CredentialRef)
			quotaSet := make(map[string]struct{})
			protocolSet := make(map[string]struct{})
			matchedRoute := false
			for registeredIndex, snapshot := range registered {
				if strings.ToLower(snapshot.RouteChannel) != channel || snapshot.RuntimeModelID != route.RuntimeModelID {
					continue
				}
				matchedRoute = true
				if snapshot.Model == nil {
					return fmt.Errorf("%s: registered route %d has no model facts", path, registeredIndex)
				}
				info := snapshot.Model
				declared := strings.TrimSpace(info.CatalogProviderID) != "" || strings.TrimSpace(info.CatalogModelID) != "" ||
					strings.TrimSpace(info.CatalogRouteProviderID) != "" || strings.TrimSpace(info.CatalogRouteModelID) != ""
				if !declared {
					// Surfaced route: the registry holds no catalog facts, and
					// the projection must carry exactly the channel identity
					// the inventory published for it.
					surfaced := modelrouting.ModelKey{CatalogProviderID: channel, CanonicalModelID: route.RuntimeModelID}
					if route.RouteKey.ModelKey != surfaced || route.CatalogRouteProviderID != channel || route.CatalogRouteModelID != route.RuntimeModelID {
						return fmt.Errorf("%s: registered route %d surfaced facts differ from the projection", path, registeredIndex)
					}
				} else if info.CatalogProviderID != route.RouteKey.ModelKey.CatalogProviderID || info.CatalogModelID != route.RouteKey.ModelKey.CanonicalModelID ||
					info.CatalogRouteProviderID != route.CatalogRouteProviderID || info.CatalogRouteModelID != route.CatalogRouteModelID {
					return fmt.Errorf("%s: registered route %d catalog facts differ from the projection", path, registeredIndex)
				}
				auth, exists := m.GetByID(snapshot.ClientID)
				if !exists || auth == nil {
					return fmt.Errorf("%s: registered route %d has no live credential", path, registeredIndex)
				}
				if executorKeyFromAuth(auth) != channel {
					return fmt.Errorf("%s: registered route %d credential channel differs from the route", path, registeredIndex)
				}
				// The credential state owns quota windows and their exact
				// recovery instant; the registry quota mark is a coarser
				// listing hint and must not outlive that instant.
				if snapshot.SuspensionReason != "" {
					continue
				}
				blocked, _, _ := isAuthBlockedForModel(auth, route.RuntimeModelID, now)
				if blocked {
					continue
				}
				ref := credentialReference(auth)
				if ref.ID == "" || ref.Kind == "" {
					return fmt.Errorf("%s: registered route %d has an invalid credential reference", path, registeredIndex)
				}
				credentialSet[ref.ID+"\x00"+ref.Kind] = ref
				quotaDomain := credentialQuotaDomain(auth)
				if quotaDomain == "" {
					return fmt.Errorf("%s: registered route %d has no explicit quota domain", path, registeredIndex)
				}
				quotaSet[quotaDomain] = struct{}{}
				if len(info.Protocols) == 0 {
					return fmt.Errorf("%s: registered route %d has no explicit protocols", path, registeredIndex)
				}
				for _, protocol := range info.Protocols {
					protocol = strings.TrimSpace(protocol)
					if protocol == "" {
						return fmt.Errorf("%s: registered route %d has an empty protocol", path, registeredIndex)
					}
					protocolSet[protocol] = struct{}{}
				}
			}
			if !matchedRoute {
				return fmt.Errorf("%s: route is absent from the live registry", path)
			}
			credentials := make([]modelrouting.CredentialRef, 0, len(credentialSet))
			for _, ref := range credentialSet {
				credentials = append(credentials, ref)
			}
			sort.Slice(credentials, func(i, j int) bool {
				if credentials[i].ID != credentials[j].ID {
					return credentials[i].ID < credentials[j].ID
				}
				return credentials[i].Kind < credentials[j].Kind
			})
			quotaDomains := sortedRoutingKeys(quotaSet)
			protocols := sortedRoutingKeys(protocolSet)
			if !equalCredentialRefs(credentials, route.CredentialRefs) {
				return fmt.Errorf("%s.credential-refs: differ from selectable live credentials", path)
			}
			if !equalStrings(quotaDomains, route.QuotaDomains) {
				return fmt.Errorf("%s.quota-domains: differ from selectable live quota domains", path)
			}
			if !equalStrings(protocols, route.Protocols) {
				return fmt.Errorf("%s.protocols: differ from executable live protocols", path)
			}
		}
	}
	return nil
}

func sortedRoutingKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
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

func equalCredentialRefs(left, right []modelrouting.CredentialRef) bool {
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

func compileModelRouting(projection *modelrouting.Config) *modelRoutingTable {
	table := &modelRoutingTable{
		aliases:         make(map[string][]RoutingCandidate),
		declaredAliases: make(map[string]struct{}),
		routes:          make(map[string]RoutingCandidate),
		variants:        make(map[string]map[string]modelrouting.Variant),
		projected:       projection != nil,
	}
	if projection == nil {
		return table
	}
	table.failurePolicy = cloneRoutingFailurePolicy(projection.FailurePolicy)
	table.projectionDigest = projection.ProjectionDigest
	for _, model := range projection.DirectModels {
		modelID := routingModelIdentity(model.ModelKey.CatalogProviderID, model.ModelKey.CanonicalModelID)
		variants := make(map[string]modelrouting.Variant, len(model.Variants))
		for _, variant := range model.Variants {
			variants[variant.VariantKey.VariantID] = cloneRoutingVariant(variant)
		}
		table.variants[modelID] = variants
		for _, route := range model.Routes {
			if !model.Active || !route.Selectable || !route.Health.Selectable {
				continue
			}
			candidate := RoutingCandidate{
				Channel:           route.RouteKey.RouteChannel,
				RuntimeModelID:    route.RuntimeModelID,
				RouteSelector:     route.RouteSelector,
				CredentialRefs:    cloneCredentialRefs(route.CredentialRefs),
				Protocols:         append([]string(nil), route.Protocols...),
				CatalogProviderID: route.RouteKey.ModelKey.CatalogProviderID,
				CanonicalModelID:  route.RouteKey.ModelKey.CanonicalModelID,
				Pricing:           modelrouting.ClonePricing(route.Pricing),
				ProjectionDigest:  projection.ProjectionDigest,
			}
			table.routes[route.RouteSelector] = candidate
		}
	}
	for _, alias := range projection.Aliases {
		table.declaredAliases[strings.ToLower(alias.Name)] = struct{}{}
		if !alias.Selectable {
			continue
		}
		candidateCount := 0
		for _, member := range alias.Members {
			candidateCount += len(member.Candidates)
		}
		candidates := make([]RoutingCandidate, 0, candidateCount)
		for routeIndex := 0; len(candidates) < candidateCount; routeIndex++ {
			appended := false
			for _, member := range alias.Members {
				if routeIndex >= len(member.Candidates) {
					continue
				}
				source := member.Candidates[routeIndex]
				candidate := RoutingCandidate{
					Channel:           source.RouteKey.RouteChannel,
					RuntimeModelID:    source.RuntimeModelID,
					Alias:             alias.Name,
					RouteSelector:     source.RouteSelector,
					VariantID:         cloneString(source.VariantID),
					CredentialRefs:    cloneCredentialRefs(source.CredentialRefs),
					Protocols:         append([]string(nil), source.Protocols...),
					CatalogProviderID: source.RouteKey.ModelKey.CatalogProviderID,
					CanonicalModelID:  source.RouteKey.ModelKey.CanonicalModelID,
					Pricing:           modelrouting.ClonePricing(source.Pricing),
					ProjectionDigest:  projection.ProjectionDigest,
				}
				if source.VariantID != nil {
					variant := table.variants[routingModelIdentity(source.RouteKey.ModelKey.CatalogProviderID, source.RouteKey.ModelKey.CanonicalModelID)][*source.VariantID]
					candidate.VariantReasoningOption = cloneString(variant.ReasoningOption)
				}
				candidates = append(candidates, candidate)
				appended = true
			}
			if !appended {
				break
			}
		}
		table.aliases[strings.ToLower(alias.Name)] = candidates
	}
	return table
}

func cloneRoutingFailurePolicy(policy modelrouting.FailurePolicy) modelrouting.FailurePolicy {
	policy.FailoverRules = append([]modelrouting.FailoverRule(nil), policy.FailoverRules...)
	for index := range policy.FailoverRules {
		policy.FailoverRules[index].HTTPStatuses = append([]int(nil), policy.FailoverRules[index].HTTPStatuses...)
		policy.FailoverRules[index].ErrorCodes = append([]string(nil), policy.FailoverRules[index].ErrorCodes...)
		policy.FailoverRules[index].FailureKinds = append([]modelrouting.FailureKind(nil), policy.FailoverRules[index].FailureKinds...)
	}
	return policy
}

func cloneRoutingVariant(value modelrouting.Variant) modelrouting.Variant {
	value.DisplayName = cloneString(value.DisplayName)
	value.ReasoningOption = cloneString(value.ReasoningOption)
	value.Protocols = append([]string(nil), value.Protocols...)
	return value
}

func cloneCredentialRefs(values []modelrouting.CredentialRef) []modelrouting.CredentialRef {
	return append([]modelrouting.CredentialRef(nil), values...)
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func routingModelIdentity(providerID, modelID string) string {
	return providerID + "\x00" + modelID
}

func cloneRoutingCandidate(candidate RoutingCandidate) RoutingCandidate {
	candidate.VariantID = cloneString(candidate.VariantID)
	candidate.VariantReasoningOption = cloneString(candidate.VariantReasoningOption)
	candidate.CredentialRefs = cloneCredentialRefs(candidate.CredentialRefs)
	candidate.Protocols = append([]string(nil), candidate.Protocols...)
	candidate.Pricing = modelrouting.ClonePricing(candidate.Pricing)
	return candidate
}

func (m *Manager) modelRoutingCandidates(requestedModel string, opts cliproxyexecutor.Options) ([]RoutingCandidate, bool, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	if m == nil {
		return nil, false, nil
	}
	table := m.modelRouting.Load()
	if table == nil {
		return nil, false, nil
	}
	selector := strings.TrimSpace(opts.Headers.Get(probeRouteSelectorHeader))
	variantHeader := strings.TrimSpace(opts.Headers.Get(probeVariantIDHeader))
	if selector != "" {
		route, exists := table.routes[selector]
		if !exists && !table.projected {
			var errBootstrap error
			route, exists, errBootstrap = m.bootstrapRoutingCandidate(selector, requestedModel)
			if errBootstrap != nil {
				return nil, true, errBootstrap
			}
		}
		if !exists || route.RuntimeModelID != requestedModel {
			return nil, true, routingContractError("route_not_selectable", "route selector is not bound to the requested runtime model", http.StatusServiceUnavailable)
		}
		if variantHeader != "" {
			modelID := routingModelIdentity(route.CatalogProviderID, route.CanonicalModelID)
			variant, exists := table.variants[modelID][variantHeader]
			if !exists {
				return nil, true, routingContractError("variant_not_executable", "variant is not nested under the selected model", http.StatusUnprocessableEntity)
			}
			route.VariantID = &variantHeader
			route.VariantReasoningOption = cloneString(variant.ReasoningOption)
			if route.VariantReasoningOption == nil {
				return nil, true, routingContractError("variant_not_executable", "variant has no catalog-owned runtime option", http.StatusUnprocessableEntity)
			}
		}
		return []RoutingCandidate{cloneRoutingCandidate(route)}, true, nil
	}
	if variantHeader != "" {
		return nil, true, routingContractError("variant_not_executable", "variant header requires a route selector", http.StatusUnprocessableEntity)
	}
	key := strings.ToLower(requestedModel)
	if candidates, exists := table.aliases[key]; exists {
		return cloneRoutingCandidates(candidates), true, nil
	}
	// The projection owns only the aliases it declares. A declared alias with
	// no selectable member stops its own consumers; any other model name is not
	// the projection's to route and falls through to ordinary selection.
	if _, declared := table.declaredAliases[key]; declared {
		return nil, true, routingContractError("route_not_selectable", "managed alias has no selectable member in the active projection", http.StatusServiceUnavailable)
	}
	return nil, false, nil
}

// bootstrapRoutingCandidate resolves one inventory selector directly from the
// live registry before CCS has published a routing projection. It never creates
// an alias, ranks routes, or permits movement to another route.
func (m *Manager) bootstrapRoutingCandidate(selector, requestedModel string) (RoutingCandidate, bool, error) {
	if m == nil {
		return RoutingCandidate{}, false, nil
	}
	var candidate RoutingCandidate
	var catalogRouteProviderID, catalogRouteModelID string
	credentialRefs := make(map[string]struct{})
	matched := false
	for index, registered := range registry.GetGlobalRegistry().RegisteredRouteSnapshots() {
		if registered.RuntimeModelID != requestedModel {
			continue
		}
		key, errKey := registered.RoutingKey(index)
		if errKey != nil {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", errKey.Error(), http.StatusServiceUnavailable)
		}
		if modelrouting.SelectorForRoute(key, registered.RuntimeModelID) != selector {
			continue
		}
		// The inventory omits a route that serves no wire protocol, so its
		// selector is never a model-routing candidate either.
		if len(registered.RoutingProtocols()) == 0 {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", "registered route serves no wire protocol", http.StatusServiceUnavailable)
		}
		info := registered.Model

		if !matched {
			candidate = RoutingCandidate{
				Channel:           registered.RouteChannel,
				RuntimeModelID:    registered.RuntimeModelID,
				RouteSelector:     selector,
				Protocols:         registered.RoutingProtocols(),
				CatalogProviderID: key.ModelKey.CatalogProviderID,
				CanonicalModelID:  key.ModelKey.CanonicalModelID,
				bootstrap:         true,
			}
			catalogRouteProviderID = info.CatalogRouteProviderID
			catalogRouteModelID = info.CatalogRouteModelID
			matched = true
		} else if candidate.Channel != registered.RouteChannel ||
			candidate.RuntimeModelID != registered.RuntimeModelID ||
			candidate.CatalogProviderID != key.ModelKey.CatalogProviderID ||
			candidate.CanonicalModelID != key.ModelKey.CanonicalModelID ||
			catalogRouteProviderID != info.CatalogRouteProviderID ||
			catalogRouteModelID != info.CatalogRouteModelID ||
			!equalStrings(candidate.Protocols, registered.RoutingProtocols()) {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", "registered routes conflict for the requested selector", http.StatusServiceUnavailable)
		}

		auth, exists := m.GetByID(registered.ClientID)
		if !exists || auth == nil {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", "registered route has no live credential", http.StatusServiceUnavailable)
		}
		if executorKeyFromAuth(auth) != candidate.Channel {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", "registered route channel differs from its credential channel", http.StatusServiceUnavailable)
		}
		ref := credentialReference(auth)
		if ref.Kind != "api_key" && ref.Kind != AuthKindOAuth {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", "registered route has an unsupported credential kind", http.StatusServiceUnavailable)
		}
		identity := ref.ID + "\x00" + ref.Kind
		if _, exists := credentialRefs[identity]; !exists {
			credentialRefs[identity] = struct{}{}
			candidate.CredentialRefs = append(candidate.CredentialRefs, ref)
		}
	}
	sort.Slice(candidate.CredentialRefs, func(i, j int) bool {
		if candidate.CredentialRefs[i].ID != candidate.CredentialRefs[j].ID {
			return candidate.CredentialRefs[i].ID < candidate.CredentialRefs[j].ID
		}
		return candidate.CredentialRefs[i].Kind < candidate.CredentialRefs[j].Kind
	})
	return candidate, matched, nil
}

func cloneRoutingCandidates(values []RoutingCandidate) []RoutingCandidate {
	result := make([]RoutingCandidate, len(values))
	for index := range values {
		result[index] = cloneRoutingCandidate(values[index])
	}
	return result
}

// ModelRoutingProviders resolves only the executor channels already projected by
// CCS. API handlers use it before the legacy registry lookup so managed aliases
// reach the model-routing executor without duplicating selection policy.
func (m *Manager) ModelRoutingProviders(requestedModel string) ([]string, bool, error) {
	candidates, matched, errCandidates := m.modelRoutingCandidates(requestedModel, cliproxyexecutor.Options{})
	if errCandidates != nil {
		return nil, matched, errCandidates
	}
	if !matched {
		return nil, false, nil
	}
	providers := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		provider := strings.ToLower(strings.TrimSpace(candidate.Channel))
		if provider == "" {
			return nil, true, routingContractError("route_not_selectable", "projected candidate has no route channel", http.StatusServiceUnavailable)
		}
		if _, exists := seen[provider]; exists {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	if len(providers) == 0 {
		return nil, true, routingContractError("route_not_selectable", "managed model has no projected executor channel", http.StatusServiceUnavailable)
	}
	return providers, true, nil
}

func (m *Manager) modelRoutingFailurePolicy() modelrouting.FailurePolicy {
	if m == nil {
		return modelrouting.FailurePolicy{}
	}
	table := m.modelRouting.Load()
	if table == nil {
		return modelrouting.FailurePolicy{}
	}
	return table.failurePolicy
}

func credentialReference(auth *Auth) modelrouting.CredentialRef {
	if auth == nil {
		return modelrouting.CredentialRef{}
	}
	kind := auth.AuthKind()
	if kind == AuthKindAPIKey {
		kind = "api_key"
	}
	return modelrouting.CredentialRef{ID: CredentialReferenceID(auth.ID), Kind: kind}
}

// CredentialReferenceID returns the stable secret-free identifier used by the
// management inventory and model-routing allowlists.
func CredentialReferenceID(rawID string) string {
	digest := sha256.Sum256([]byte(rawID))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (m *Manager) routingCandidateKnownSelectable(ctx context.Context, candidate RoutingCandidate, opts cliproxyexecutor.Options) bool {
	if m == nil {
		return false
	}
	if _, exists := m.Executor(strings.ToLower(candidate.Channel)); !exists {
		return false
	}
	allowed := make(map[string]struct{}, len(candidate.CredentialRefs))
	for _, ref := range candidate.CredentialRefs {
		allowed[ref.ID+"\x00"+ref.Kind] = struct{}{}
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	now := time.Now()
	for _, auth := range m.snapshotAuths() {
		if auth == nil || executorKeyFromAuth(auth) != strings.ToLower(candidate.Channel) || !eligibility.allows(auth) {
			continue
		}
		ref := credentialReference(auth)
		if _, exists := allowed[ref.ID+"\x00"+ref.Kind]; !exists {
			continue
		}
		blocked, _, _ := isAuthBlockedForModel(auth, candidate.RuntimeModelID, now)
		if !blocked {
			return true
		}
	}
	return false
}

func (m *Manager) candidateOptions(
	opts cliproxyexecutor.Options,
	index int,
	candidate RoutingCandidate,
	recordSelectedAuth func(string),
) cliproxyexecutor.Options {
	headers := opts.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Del(probeRouteSelectorHeader)
	headers.Del(probeVariantIDHeader)
	opts.Headers = headers

	allowedRefs := make(map[string]struct{}, len(candidate.CredentialRefs))
	for _, ref := range candidate.CredentialRefs {
		allowedRefs[ref.ID+"\x00"+ref.Kind] = struct{}{}
	}
	allowedIDs := make(map[string]struct{})
	for _, auth := range m.snapshotAuths() {
		ref := credentialReference(auth)
		if _, allowed := allowedRefs[ref.ID+"\x00"+ref.Kind]; allowed {
			allowedIDs[auth.ID] = struct{}{}
		}
	}
	meta := make(map[string]any, len(opts.Metadata)+8)
	for key, value := range opts.Metadata {
		meta[key] = value
	}
	meta[allowedAuthIDsMetadataKey] = allowedIDs
	meta[modelRoutingMetadataKey] = true
	meta[routingIndexMetadataKey] = index
	meta[routingChannelMetadataKey] = candidate.Channel
	meta[routingModelMetadataKey] = candidate.RuntimeModelID
	meta[routingAliasMetadataKey] = candidate.Alias
	meta[routingSelectorMetadataKey] = candidate.RouteSelector
	meta[cliproxyexecutor.AuthSelectionModelMetadataKey] = candidate.RuntimeModelID
	priorSelectedAuthCallback, _ := meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
		if priorSelectedAuthCallback != nil {
			priorSelectedAuthCallback(authID)
		}
		if recordSelectedAuth != nil {
			recordSelectedAuth(authID)
		}
	}
	if candidate.VariantReasoningOption != nil {
		meta[cliproxyexecutor.ReasoningEffortMetadataKey] = *candidate.VariantReasoningOption
	}
	opts.Metadata = meta
	return opts
}

func isModelRoutingOptions(opts cliproxyexecutor.Options) bool {
	value, _ := opts.Metadata[modelRoutingMetadataKey].(bool)
	return value
}

func allowedAuthIDsFromMetadata(metadata map[string]any) (map[string]struct{}, bool) {
	if metadata == nil {
		return nil, false
	}
	raw, exists := metadata[allowedAuthIDsMetadataKey]
	if !exists {
		return nil, false
	}
	values, ok := raw.(map[string]struct{})
	return values, ok
}

func routingProtocol(format sdktranslator.Format) (string, bool) {
	switch format {
	case sdktranslator.FormatOpenAI:
		return "openai_chat", true
	case sdktranslator.FormatOpenAIResponse:
		return "openai_responses", true
	case sdktranslator.FormatClaude:
		return "anthropic_messages", true
	default:
		return "", false
	}
}

func prepareRoutingCandidateRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, candidate RoutingCandidate) (cliproxyexecutor.Request, error) {
	protocol, supported := routingProtocol(opts.SourceFormat)
	if !supported || !containsRoutingValue(candidate.Protocols, protocol) {
		return req, routingContractError("protocol_not_supported", fmt.Sprintf("route does not support request protocol %q", opts.SourceFormat), http.StatusBadRequest)
	}
	req.Model = candidate.RuntimeModelID
	metadata := make(map[string]any, len(req.Metadata)+2)
	for key, value := range req.Metadata {
		metadata[key] = value
	}
	metadata[resolvedAPIKeyModelInfoMetadataKey] = &registry.ModelInfo{
		ID:      candidate.RuntimeModelID,
		Pricing: modelrouting.ClonePricing(candidate.Pricing),
	}
	metadata[routingProjectionDigestMetadataKey] = candidate.ProjectionDigest
	req.Metadata = metadata
	if candidate.VariantID == nil {
		return req, nil
	}
	if candidate.VariantReasoningOption == nil {
		return req, routingContractError("variant_not_executable", "variant has no catalog-owned runtime option", http.StatusUnprocessableEntity)
	}
	if len(req.Payload) == 0 || !gjson.ValidBytes(req.Payload) || !gjson.ParseBytes(req.Payload).IsObject() {
		return req, routingContractError("variant_not_executable", "variant requires a JSON object request payload", http.StatusUnprocessableEntity)
	}
	updated, err := applyRoutingReasoningOption(req.Payload, protocol, *candidate.VariantReasoningOption)
	if err != nil {
		return req, routingContractError("variant_not_executable", err.Error(), http.StatusUnprocessableEntity)
	}
	req.Payload = updated
	return req, nil
}

func applyRoutingReasoningOption(payload []byte, protocol, option string) ([]byte, error) {
	config, errConfig := routingThinkingConfig(protocol, option)
	if errConfig != nil {
		return nil, errConfig
	}

	var updated []byte
	var err error
	switch protocol {
	case "openai_chat":
		updated, err = thinkingopenai.NewApplier().Apply(payload, config, nil)
	case "openai_responses":
		updated, err = thinkingcodex.NewApplier().Apply(payload, config, nil)
	case "anthropic_messages":
		updated, err = thinkingclaude.NewApplier().Apply(payload, config, nil)
	default:
		return nil, fmt.Errorf("no variant executor for protocol %q", protocol)
	}
	if err != nil {
		return nil, fmt.Errorf("apply reasoning option for %s: %w", protocol, err)
	}
	return updated, nil
}

func routingThinkingConfig(protocol, option string) (thinking.ThinkingConfig, error) {
	if mode, ok := thinking.ParseSpecialSuffix(option); ok {
		switch mode {
		case thinking.ModeNone:
			return thinking.ThinkingConfig{Mode: thinking.ModeNone}, nil
		case thinking.ModeAuto:
			return thinking.ThinkingConfig{Mode: thinking.ModeAuto, Budget: -1}, nil
		}
	}
	if level, ok := thinking.ParseLevelSuffix(option); ok {
		return thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: level}, nil
	}
	if budget, errBudget := strconv.Atoi(option); errBudget == nil && budget > 0 {
		if protocol != "anthropic_messages" {
			return thinking.ThinkingConfig{}, fmt.Errorf("numeric reasoning option is not executable by protocol %q", protocol)
		}
		return thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: budget}, nil
	}
	return thinking.ThinkingConfig{}, fmt.Errorf("reasoning option %q is not executable by protocol %q", option, protocol)
}

func containsRoutingValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type routingContractFailure struct {
	cause *Error
}

func (e *routingContractFailure) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *routingContractFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *routingContractFailure) StatusCode() int {
	if e == nil || e.cause == nil {
		return 0
	}
	return e.cause.StatusCode()
}

func isRoutingContractFailure(err error) bool {
	var contractErr *routingContractFailure
	return errors.As(err, &contractErr) && contractErr != nil
}

func routingContractError(code, message string, status int) error {
	return &routingContractFailure{cause: &Error{Code: code, Message: message, HTTPStatus: status}}
}

func mergeModelRoutingHeaders(existing, routing http.Header) http.Header {
	result := existing.Clone()
	if result == nil {
		result = make(http.Header)
	}
	for key, values := range routing {
		result.Del(key)
		for _, value := range values {
			result.Add(key, value)
		}
	}
	return result
}

func (m *Manager) modelRoutingResponseHeaders(candidate RoutingCandidate, selectedAuthID string) http.Header {
	headers := make(http.Header)
	headers.Set(probeRouteSelectorHeader, candidate.RouteSelector)
	headers.Set(effectiveModelHeader, candidate.RuntimeModelID)
	if candidate.ProjectionDigest != "" {
		headers.Set(projectionDigestHeader, candidate.ProjectionDigest)
	}
	if candidate.VariantID != nil {
		headers.Set(probeVariantIDHeader, *candidate.VariantID)
	}
	if selectedAuthID == "" || m == nil {
		return headers
	}
	auth, exists := m.GetByID(selectedAuthID)
	if !exists || auth == nil {
		return headers
	}
	ref := credentialReference(auth)
	if ref.ID != "" {
		headers.Set(credentialReferenceHeader, ref.ID)
	}
	if domain := credentialQuotaDomain(auth); domain != "" {
		headers.Set(quotaDomainHeader, domain)
	}
	return headers
}

func credentialQuotaDomain(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return strings.TrimSpace(auth.QuotaDomain)
}

type modelRoutingPointer = atomic.Pointer[modelRoutingTable]
