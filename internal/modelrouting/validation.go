package modelrouting

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	credentialRevisionPattern = regexp.MustCompile(`^[0-9]+:[0-9]+$`)
	digestPattern             = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	decimalPattern            = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)
	signedDecimalPattern      = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
)

// UnmarshalYAML rejects unknown projection fields recursively. The rest of the
// application config remains backward-compatible; this versioned boundary does not.
func (cfg *Config) UnmarshalYAML(node *yaml.Node) error {
	type plainConfig Config
	if err := rejectUnknownFields(node, reflect.TypeOf(plainConfig{}), "model-routing"); err != nil {
		return err
	}
	var decoded plainConfig
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*cfg = Config(decoded)
	return nil
}

func rejectUnknownFields(node *yaml.Node, target reflect.Type, path string) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if node == nil {
		return nil
	}
	switch target.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return nil
		}
		type fieldSpec struct {
			typeOf   reflect.Type
			optional bool
		}
		fields := make(map[string]fieldSpec, target.NumField())
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			tagParts := strings.Split(field.Tag.Get("yaml"), ",")
			name := tagParts[0]
			if name == "" {
				name = strings.ToLower(field.Name)
			}
			if name == "-" {
				continue
			}
			optional := false
			for _, option := range tagParts[1:] {
				optional = optional || option == "omitempty"
			}
			fields[name] = fieldSpec{typeOf: field.Type, optional: optional}
		}
		seen := make(map[string]struct{}, len(fields))
		for index := 0; index+1 < len(node.Content); index += 2 {
			name := node.Content[index].Value
			field, ok := fields[name]
			if !ok {
				return fmt.Errorf("%s.%s: unknown field", path, name)
			}
			seen[name] = struct{}{}
			if err := rejectUnknownFields(node.Content[index+1], field.typeOf, path+"."+name); err != nil {
				return err
			}
		}
		for name, field := range fields {
			if _, exists := seen[name]; !exists && !field.optional {
				return fmt.Errorf("%s.%s: required field is missing", path, name)
			}
		}
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return nil
		}
		for index, child := range node.Content {
			if err := rejectUnknownFields(child, target.Elem(), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate checks projection integrity without applying business policy.
func (cfg *Config) Validate() error {
	if cfg == nil {
		return fmt.Errorf("model-routing: projection is required")
	}
	if cfg.SchemaVersion != SchemaVersion {
		return fmt.Errorf("model-routing.schema-version: unsupported value %d", cfg.SchemaVersion)
	}
	if cfg.Generation == 0 {
		return fmt.Errorf("model-routing.generation: must be positive")
	}
	if !digestPattern.MatchString(cfg.SnapshotDigest) {
		return fmt.Errorf("model-routing.snapshot-digest: must be sha256:<64 lowercase hex>")
	}
	if !digestPattern.MatchString(cfg.ProjectionDigest) {
		return fmt.Errorf("model-routing.projection-digest: must be sha256:<64 lowercase hex>")
	}
	computedDigest, errDigest := ProjectionDigest(cfg)
	if errDigest != nil {
		return fmt.Errorf("model-routing.projection-digest: recompute canonical projection: %w", errDigest)
	}
	if cfg.ProjectionDigest != computedDigest {
		return fmt.Errorf("model-routing.projection-digest: declared %s differs from computed %s", cfg.ProjectionDigest, computedDigest)
	}
	if err := cfg.validateFailurePolicy(); err != nil {
		return err
	}
	// ADR-0023: a projection with zero direct models (every lane empty with
	// selectable=false) is a publishable fleet state, not an invalid one.
	// Execution answers such requests with 503 route_not_selectable; rejecting
	// the publication here would invalidate the whole generation and
	// reintroduce the v2 hold-the-generation defect.

	models := make(map[string]DirectModel, len(cfg.DirectModels))
	routes := make(map[string]DirectRoute)
	variants := make(map[string]struct{})
	for index, model := range cfg.DirectModels {
		path := fmt.Sprintf("model-routing.direct-models[%d]", index)
		if err := validateModelKey(path+".model-key", model.ModelKey); err != nil {
			return err
		}
		modelID := modelKeyID(model.ModelKey)
		if _, duplicate := models[modelID]; duplicate {
			return fmt.Errorf("%s.model-key: duplicate ModelKey", path)
		}
		if err := requireCanonical(path+".display-name", model.DisplayName); err != nil {
			return err
		}
		if model.Active && len(model.Routes) == 0 {
			return fmt.Errorf("%s.routes: active direct model must have a route", path)
		}
		models[modelID] = model
		for variantIndex, variant := range model.Variants {
			variantPath := fmt.Sprintf("%s.variants[%d]", path, variantIndex)
			if err := validateVariant(variantPath, model.ModelKey, variant); err != nil {
				return err
			}
			key := variantKeyID(variant.VariantKey)
			if _, duplicate := variants[key]; duplicate {
				return fmt.Errorf("%s.variant-key: duplicate VariantKey", variantPath)
			}
			variants[key] = struct{}{}
		}
		for routeIndex, route := range model.Routes {
			routePath := fmt.Sprintf("%s.routes[%d]", path, routeIndex)
			if err := validateRoute(routePath, model.ModelKey, route); err != nil {
				return err
			}
			if _, duplicate := routes[route.RouteSelector]; duplicate {
				return fmt.Errorf("%s.route-selector: duplicate execution selector", routePath)
			}
			routes[route.RouteSelector] = route
		}
	}

	aliases := make(map[string]struct{}, len(cfg.Aliases))
	tiers := make(map[string]struct{}, len(cfg.Aliases))
	for aliasIndex, alias := range cfg.Aliases {
		path := fmt.Sprintf("model-routing.aliases[%d]", aliasIndex)
		if err := requireCanonical(path+".name", alias.Name); err != nil {
			return err
		}
		if err := requireCanonical(path+".tier-id", alias.TierID); err != nil {
			return err
		}
		if _, duplicate := aliases[alias.Name]; duplicate {
			return fmt.Errorf("%s.name: duplicate alias", path)
		}
		aliases[alias.Name] = struct{}{}
		if _, duplicate := tiers[alias.TierID]; duplicate {
			return fmt.Errorf("%s.tier-id: duplicate tier", path)
		}
		tiers[alias.TierID] = struct{}{}
		if alias.Selectable != (len(alias.Members) > 0) {
			return fmt.Errorf("%s.selectable: must be true exactly when members are present", path)
		}
		if err := requireCanonical(path+".reason", alias.Reason); err != nil {
			return err
		}
		memberModels := make(map[string]struct{}, len(alias.Members))
		for memberIndex, member := range alias.Members {
			memberPath := fmt.Sprintf("%s.members[%d]", path, memberIndex)
			if member.MemberRank != memberIndex+1 {
				return fmt.Errorf("%s.member-rank: must be contiguous and match member order", memberPath)
			}
			if err := validateModelKey(memberPath+".model-key", member.ModelKey); err != nil {
				return err
			}
			modelID := modelKeyID(member.ModelKey)
			model, exists := models[modelID]
			if !exists || !model.Active {
				return fmt.Errorf("%s.model-key: member does not reference an active direct model", memberPath)
			}
			if _, exists := memberModels[modelID]; exists {
				return fmt.Errorf("%s.model-key: duplicate member in tier %q", memberPath, alias.TierID)
			}
			memberModels[modelID] = struct{}{}
			if !signedDecimalPattern.MatchString(member.ModelScore) || member.ModelScore == "-0" {
				return fmt.Errorf("%s.model-score: must be a canonical signed decimal string", memberPath)
			}
			if err := requireCanonical(memberPath+".selection-reason", member.SelectionReason); err != nil {
				return err
			}
			if len(member.Candidates) == 0 {
				return fmt.Errorf("%s.candidates: allocated member must have executable candidates", memberPath)
			}
			candidateKeys := make(map[string]struct{}, len(member.Candidates))
			for candidateIndex, candidate := range member.Candidates {
				candidatePath := fmt.Sprintf("%s.candidates[%d]", memberPath, candidateIndex)
				if candidate.RouteRank != candidateIndex+1 {
					return fmt.Errorf("%s.route-rank: must be contiguous and match candidate order", candidatePath)
				}
				if err := validateCandidate(candidatePath, member.ModelKey, candidate, model, routes, variants); err != nil {
					return err
				}
				candidateID := candidate.RouteSelector + "\x00" + optionalString(candidate.VariantID)
				if _, duplicate := candidateKeys[candidateID]; duplicate {
					return fmt.Errorf("%s: duplicate CandidateKey", candidatePath)
				}
				candidateKeys[candidateID] = struct{}{}
			}
		}
	}
	return nil
}

func (cfg *Config) validateFailurePolicy() error {
	policy := cfg.FailurePolicy
	if policy.Mode != "classified_candidate_failover" {
		return fmt.Errorf("model-routing.failure-policy.mode: must be classified_candidate_failover")
	}
	if policy.CredentialAcquisitionTimeoutSeconds < 1 {
		return fmt.Errorf("model-routing.failure-policy.credential-acquisition-timeout-seconds: must be positive")
	}
	if int64(policy.CredentialAcquisitionTimeoutSeconds) > math.MaxInt64/int64(time.Second) {
		return fmt.Errorf("model-routing.failure-policy.credential-acquisition-timeout-seconds: exceeds time.Duration")
	}
	if policy.AutomaticRetry {
		return fmt.Errorf("model-routing.failure-policy.automatic-retry: must be false")
	}
	if !policy.AutomaticFailover {
		return fmt.Errorf("model-routing.failure-policy.automatic-failover: must be true")
	}
	if err := validateFailoverRules(policy.FailoverRules); err != nil {
		return err
	}
	if policy.ServeStaleOnError {
		return fmt.Errorf("model-routing.failure-policy.serve-stale-on-error: must be false")
	}
	if !policy.PreserveFirstError {
		return fmt.Errorf("model-routing.failure-policy.preserve-first-error: must be true")
	}
	if !policy.TerminateOwnedRequestOnCancel {
		return fmt.Errorf("model-routing.failure-policy.terminate-owned-request-on-cancel: must be true")
	}
	return nil
}

func validateFailoverRules(rules []FailoverRule) error {
	if len(rules) == 0 {
		return fmt.Errorf("model-routing.failure-policy.failover-rules: must not be empty")
	}
	ruleIDs := make(map[string]struct{}, len(rules))
	matchers := make(map[string]struct{}, len(rules))
	for index, rule := range rules {
		path := fmt.Sprintf("model-routing.failure-policy.failover-rules[%d]", index)
		if err := requireCanonical(path+".rule-id", rule.RuleID); err != nil {
			return err
		}
		if _, duplicate := ruleIDs[rule.RuleID]; duplicate {
			return fmt.Errorf("%s.rule-id: duplicate rule", path)
		}
		ruleIDs[rule.RuleID] = struct{}{}
		if len(rule.HTTPStatuses) == 0 && len(rule.ErrorCodes) == 0 && len(rule.FailureKinds) == 0 {
			return fmt.Errorf("%s: at least one matcher is required", path)
		}
		for statusIndex, status := range rule.HTTPStatuses {
			if status < 100 || status > 599 {
				return fmt.Errorf("%s.http-statuses[%d]: must be between 100 and 599", path, statusIndex)
			}
			if statusIndex > 0 && rule.HTTPStatuses[statusIndex-1] >= status {
				return fmt.Errorf("%s.http-statuses: must be sorted and unique", path)
			}
		}
		if err := validateSortedStrings(path+".error-codes", rule.ErrorCodes, true); err != nil {
			return err
		}
		for kindIndex, kind := range rule.FailureKinds {
			switch kind {
			case FailureKindCredential, FailureKindTransport, FailureKindUpstreamTimeout, FailureKindEmptyPreResponse:
			default:
				return fmt.Errorf("%s.failure-kinds[%d]: unsupported failure kind %q", path, kindIndex, kind)
			}
			if kindIndex > 0 && rule.FailureKinds[kindIndex-1] >= kind {
				return fmt.Errorf("%s.failure-kinds: must be sorted and unique", path)
			}
		}
		matcherID := fmt.Sprintf("%v\x00%v\x00%v", rule.HTTPStatuses, rule.ErrorCodes, rule.FailureKinds)
		if _, duplicate := matchers[matcherID]; duplicate {
			return fmt.Errorf("%s: duplicate matcher", path)
		}
		matchers[matcherID] = struct{}{}
	}
	return nil
}

func validateCandidate(path string, parent ModelKey, candidate Candidate, model DirectModel, routes map[string]DirectRoute, variants map[string]struct{}) error {
	if err := validateRouteKey(path+".route-key", candidate.RouteKey); err != nil {
		return err
	}
	if candidate.RouteKey.ModelKey != parent {
		return fmt.Errorf("%s.route-key: candidate must be nested under its member ModelKey", path)
	}
	if err := requireCanonical(path+".runtime-model-id", candidate.RuntimeModelID); err != nil {
		return err
	}
	if err := requireCanonical(path+".catalog-route-provider-id", candidate.CatalogRouteProviderID); err != nil {
		return err
	}
	if err := requireCanonical(path+".catalog-route-model-id", candidate.CatalogRouteModelID); err != nil {
		return err
	}
	if !digestPattern.MatchString(candidate.RouteSelector) {
		return fmt.Errorf("%s.route-selector: must be an opaque sha256 selector", path)
	}
	route, exists := routes[candidate.RouteSelector]
	if !exists || route.RouteKey != candidate.RouteKey || !route.Selectable || !route.Health.Selectable {
		return fmt.Errorf("%s.route-key: candidate does not reference a selectable direct route", path)
	}
	if candidate.RuntimeModelID != route.RuntimeModelID {
		return fmt.Errorf("%s.runtime-model-id: differs from the referenced direct route", path)
	}
	if candidate.CatalogRouteProviderID != route.CatalogRouteProviderID || candidate.CatalogRouteModelID != route.CatalogRouteModelID {
		return fmt.Errorf("%s: candidate catalog route facts differ from its direct route", path)
	}
	if candidate.VariantID != nil {
		if err := requireCanonical(path+".variant-id", *candidate.VariantID); err != nil {
			return err
		}
		key := variantKeyID(VariantKey{
			ModelKey:  candidate.RouteKey.ModelKey,
			VariantID: *candidate.VariantID,
		})
		if _, exists := variants[key]; !exists {
			return fmt.Errorf("%s.variant-id: candidate does not reference a nested model variant", path)
		}
		variant := findVariant(model.Variants, *candidate.VariantID)
		if variant == nil || !stringsSubset(candidate.Protocols, variant.Protocols) {
			return fmt.Errorf("%s.protocols: candidate protocol is not executable by its variant", path)
		}
		if variant.ReasoningOption == nil {
			return fmt.Errorf("%s.variant-id: referenced variant has no executable reasoning option", path)
		}
	}
	if err := validateSortedStrings(path+".quota-domains", candidate.QuotaDomains, false); err != nil {
		return err
	}
	if err := validateCredentialRefs(path+".credential-refs", candidate.CredentialRefs); err != nil {
		return err
	}
	for _, ref := range candidate.CredentialRefs {
		if !slices.Contains(route.CredentialRefs, ref) {
			return fmt.Errorf("%s.credential-refs: outside the referenced direct route", path)
		}
	}
	if err := validateSortedStrings(path+".protocols", candidate.Protocols, false); err != nil {
		return err
	}
	if !stringsSubset(candidate.QuotaDomains, route.QuotaDomains) || !stringsSubset(candidate.Protocols, route.Protocols) {
		return fmt.Errorf("%s: candidate quota/protocol facts differ from its direct route", path)
	}
	if err := validateHealth(path+".health", candidate.Health); err != nil {
		return err
	}
	if !candidate.Health.Selectable {
		return fmt.Errorf("%s.health.selectable: candidate must be selectable", path)
	}
	if err := validateRestrictions(path+".restrictions", candidate.Restrictions); err != nil {
		return err
	}
	if !reflect.DeepEqual(candidate.Health, route.Health) || !reflect.DeepEqual(candidate.Restrictions, route.Restrictions) {
		return fmt.Errorf("%s: candidate health/restriction facts differ from its direct route", path)
	}
	for _, restriction := range candidate.Restrictions {
		if restriction.Active {
			return fmt.Errorf("%s.restrictions: selectable candidate has an active restriction", path)
		}
	}
	if err := validateOptionalPricing(path+".pricing", candidate.Pricing); err != nil {
		return err
	}
	if !reflect.DeepEqual(candidate.Pricing, route.Pricing) {
		return fmt.Errorf("%s.pricing: differs from the referenced direct route", path)
	}
	return requireCanonical(path+".selection-reason", candidate.SelectionReason)
}

func validateRoute(path string, parent ModelKey, route DirectRoute) error {
	if err := validateRouteKey(path+".route-key", route.RouteKey); err != nil {
		return err
	}
	if route.RouteKey.ModelKey != parent {
		return fmt.Errorf("%s.route-key: route must be nested under its ModelKey", path)
	}
	if err := requireCanonical(path+".catalog-route-provider-id", route.CatalogRouteProviderID); err != nil {
		return err
	}
	if err := requireCanonical(path+".catalog-route-model-id", route.CatalogRouteModelID); err != nil {
		return err
	}
	if err := requireCanonical(path+".runtime-model-id", route.RuntimeModelID); err != nil {
		return err
	}
	if !digestPattern.MatchString(route.RouteSelector) {
		return fmt.Errorf("%s.route-selector: must be an opaque sha256 selector", path)
	}
	if err := validateSortedStrings(path+".quota-domains", route.QuotaDomains, false); err != nil {
		return err
	}
	if err := validateCredentialRefs(path+".credential-refs", route.CredentialRefs); err != nil {
		return err
	}
	if err := validateSortedStrings(path+".protocols", route.Protocols, false); err != nil {
		return err
	}
	if err := validateRestrictions(path+".restrictions", route.Restrictions); err != nil {
		return err
	}
	if err := validateHealth(path+".health", route.Health); err != nil {
		return err
	}
	if err := validateOptionalPricing(path+".pricing", route.Pricing); err != nil {
		return err
	}
	if route.Selectable && !route.Health.Selectable {
		return fmt.Errorf("%s.selectable: route cannot be selectable while health is not selectable", path)
	}
	if route.Selectable {
		for _, restriction := range route.Restrictions {
			if restriction.Active {
				return fmt.Errorf("%s.restrictions: selectable route has an active restriction", path)
			}
		}
	}
	return requireCanonical(path+".selection-reason", route.SelectionReason)
}

func validateVariant(path string, parent ModelKey, variant Variant) error {
	if err := validateVariantKey(path+".variant-key", variant.VariantKey); err != nil {
		return err
	}
	if variant.VariantKey.ModelKey != parent {
		return fmt.Errorf("%s.variant-key: variant must be nested under its ModelKey", path)
	}
	if variant.DisplayName != nil {
		if err := requireCanonical(path+".display-name", *variant.DisplayName); err != nil {
			return err
		}
	}
	if variant.ReasoningOption != nil {
		if err := requireCanonical(path+".reasoning-option", *variant.ReasoningOption); err != nil {
			return err
		}
	}
	return validateSortedStrings(path+".protocols", variant.Protocols, false)
}

func findVariant(variants []Variant, variantID string) *Variant {
	for index := range variants {
		if variants[index].VariantKey.VariantID == variantID {
			return &variants[index]
		}
	}
	return nil
}

func validateOptionalPricing(path string, pricing *Pricing) error {
	if pricing == nil {
		return nil
	}
	return validatePricing(path, *pricing)
}

func validatePricing(path string, pricing Pricing) error {
	for name, value := range map[string]string{"currency": pricing.Currency, "unit": pricing.Unit, "source-id": pricing.SourceID} {
		if err := requireCanonical(path+"."+name, value); err != nil {
			return err
		}
	}
	if len(pricing.Entries) == 0 {
		return fmt.Errorf("%s.entries: must not be empty", path)
	}
	seen := make(map[string]struct{}, len(pricing.Entries))
	for index, entry := range pricing.Entries {
		entryPath := fmt.Sprintf("%s.entries[%d]", path, index)
		if err := requireCanonical(entryPath+".name", entry.Name); err != nil {
			return err
		}
		if !decimalPattern.MatchString(entry.Amount) {
			return fmt.Errorf("%s.amount: must be a non-negative plain decimal string", entryPath)
		}
		if entry.TierType != nil {
			if err := requireCanonical(entryPath+".tier-type", *entry.TierType); err != nil {
				return err
			}
		}
		if entry.TierSize != nil && *entry.TierSize <= 0 {
			return fmt.Errorf("%s.tier-size: must be positive", entryPath)
		}
		if entry.TierSize != nil && entry.TierType == nil {
			return fmt.Errorf("%s.tier-size: requires tier-type", entryPath)
		}
		if entry.ContextKey != nil {
			if err := requireCanonical(entryPath+".context-key", *entry.ContextKey); err != nil {
				return err
			}
		}
		identity := entry.Name + "\x00" + optionalString(entry.TierType) + "\x00" + optionalInt(entry.TierSize) + "\x00" + optionalString(entry.ContextKey)
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("%s: duplicate pricing dimension", entryPath)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func validateHealth(path string, health Health) error {
	switch health.Status {
	case "healthy", "degraded", "blocked", "unknown":
	default:
		return fmt.Errorf("%s.status: unsupported health status %q", path, health.Status)
	}
	if err := requireCanonical(path+".observed-at", health.ObservedAt); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, health.ObservedAt); err != nil {
		return fmt.Errorf("%s.observed-at: must be RFC3339: %w", path, err)
	}
	if health.LatencyMS != nil && *health.LatencyMS < 0 {
		return fmt.Errorf("%s.latency-ms: must be non-negative", path)
	}
	return nil
}

func validateRestrictions(path string, restrictions []Restriction) error {
	seen := make(map[string]struct{}, len(restrictions))
	for index, restriction := range restrictions {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if err := requireCanonical(itemPath+".rule-id", restriction.RuleID); err != nil {
			return err
		}
		if _, duplicate := seen[restriction.RuleID]; duplicate {
			return fmt.Errorf("%s.rule-id: duplicate restriction", itemPath)
		}
		seen[restriction.RuleID] = struct{}{}
		if err := requireCanonical(itemPath+".config-path", restriction.ConfigPath); err != nil {
			return err
		}
		if err := requireCanonical(itemPath+".reason", restriction.Reason); err != nil {
			return err
		}
	}
	return nil
}

func validateCredentialRefs(path string, refs []CredentialRef) error {
	if len(refs) == 0 {
		return fmt.Errorf("%s: must not be empty", path)
	}
	seen := make(map[string]struct{}, len(refs))
	for index, ref := range refs {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if !digestPattern.MatchString(ref.ID) {
			return fmt.Errorf("%s.id: must be an opaque sha256 credential reference", itemPath)
		}
		if err := requireCanonical(itemPath+".kind", ref.Kind); err != nil {
			return err
		}
		if ref.Revision != "" && !credentialRevisionPattern.MatchString(ref.Revision) {
			return fmt.Errorf("%s.revision: invalid registration and credential revision", itemPath)
		}
		identity := ref.ID + "\x00" + ref.Kind
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("%s: duplicate credential reference", itemPath)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func stringsSubset(values, superset []string) bool {
	allowed := make(map[string]struct{}, len(superset))
	for _, value := range superset {
		allowed[value] = struct{}{}
	}
	for _, value := range values {
		if _, exists := allowed[value]; !exists {
			return false
		}
	}
	return true
}

func validateModelKey(path string, key ModelKey) error {
	if err := requireCanonical(path+".catalog-provider-id", key.CatalogProviderID); err != nil {
		return err
	}
	return requireCanonical(path+".canonical-model-id", key.CanonicalModelID)
}

func validateRouteKey(path string, key RouteKey) error {
	if err := validateModelKey(path+".model-key", key.ModelKey); err != nil {
		return err
	}
	return requireCanonical(path+".route-channel", key.RouteChannel)
}

func validateVariantKey(path string, key VariantKey) error {
	if err := validateModelKey(path+".model-key", key.ModelKey); err != nil {
		return err
	}
	return requireCanonical(path+".variant-id", key.VariantID)
}

func requireCanonical(path, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s: must be a non-empty canonical string", path)
	}
	return nil
}

func validateSortedStrings(path string, values []string, allowEmpty bool) error {
	if !allowEmpty && len(values) == 0 {
		return fmt.Errorf("%s: must not be empty", path)
	}
	for index, value := range values {
		if err := requireCanonical(fmt.Sprintf("%s[%d]", path, index), value); err != nil {
			return err
		}
		if index > 0 && values[index-1] >= value {
			return fmt.Errorf("%s: must be sorted and unique", path)
		}
	}
	return nil
}

func modelKeyID(key ModelKey) string {
	return key.CatalogProviderID + "\x00" + key.CanonicalModelID
}

// SelectorForRoute returns the stable, secret-free selector bound to one route
// and its exact executor model ID. Length prefixes prevent ambiguous joins.
func SelectorForRoute(key RouteKey, runtimeModelID string) string {
	parts := []string{key.ModelKey.CatalogProviderID, key.ModelKey.CanonicalModelID, key.RouteChannel, runtimeModelID}
	hasher := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hasher, "%d:", len(part))
		_, _ = hasher.Write([]byte(part))
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func variantKeyID(key VariantKey) string {
	return modelKeyID(key.ModelKey) + "\x00" + key.VariantID
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalInt(value *int64) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%d", *value)
}
