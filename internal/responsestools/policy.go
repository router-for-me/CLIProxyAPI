package responsestools

import (
	"errors"
	"fmt"
	"strings"
)

func errSharedCapacityFull() error {
	return errors.New("shared responses-tools capacity is full")
}

func errAttemptBudgetExceeded() error {
	return errors.New("attempt budget exceeded")
}

// ValidateLimits rejects non-positive or unreasonable budgets. Negative
// values never mean unlimited.
func ValidateLimits(limits Limits) error {
	const maxBytes = 1 << 30
	const maxCount = 1 << 20
	const maxDepth = 1024
	checks := []struct {
		name  string
		value int
		max   int
	}{
		{"max-active-tool-bytes", limits.MaxActiveToolBytes, maxBytes},
		{"max-active-attempts", limits.MaxActiveAttempts, maxCount},
		{"max-state-bytes", limits.MaxStateBytes, maxBytes},
		{"max-attempt-bytes", limits.MaxAttemptBytes, maxBytes},
		{"max-schema-expansion-bytes", limits.MaxSchemaExpansionBytes, maxBytes},
		{"max-schema-expansion-nodes", limits.MaxSchemaExpansionNodes, maxCount},
		{"max-depth", limits.MaxDepth, maxDepth},
	}
	for _, check := range checks {
		if check.value <= 0 {
			return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("%s must be positive", check.name))
		}
		if check.value > check.max {
			return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("%s exceeds reasonable bound", check.name))
		}
	}
	if limits.MaxAttemptBytes > limits.MaxStateBytes {
		return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("max-attempt-bytes must not exceed max-state-bytes"))
	}
	return nil
}

// ValidateRoutePolicy rejects unknown enums and illegal combinations. An empty
// strategy is valid and means "follow the convention for this route", which is
// what applyConventionDefaults resolves it to; a rule that only wants to change
// one strategy must not have to restate the others.
func ValidateRoutePolicy(route RoutePolicy) error {
	switch route.ClientSearch {
	case "", ClientSearchInherit, ClientSearchNative, ClientSearchBridge, ClientSearchDisabled:
	default:
		return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("unknown client-search mode %q", string(route.ClientSearch)))
	}
	switch route.CustomTools {
	case "", CustomToolsInherit, CustomToolsNative, CustomToolsFunction, CustomToolsStrip, CustomToolsReject:
	default:
		return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("unknown custom-tools mode %q", string(route.CustomTools)))
	}
	switch route.CustomGrammar {
	case "", CustomGrammarReject, CustomGrammarDescribe:
	default:
		return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("unknown custom-grammar mode %q", string(route.CustomGrammar)))
	}
	switch route.Schema.LocalRefs {
	case "", LocalRefsPreserve, LocalRefsInline, LocalRefsFlatten:
	default:
		return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("unknown local-refs mode %q", string(route.Schema.LocalRefs)))
	}
	switch route.Schema.SearchRequired {
	case SearchRequiredInherit, SearchRequiredComplete, SearchRequiredPreserve:
	default:
		return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("unknown search-required mode %q", string(route.Schema.SearchRequired)))
	}
	match := route.Match
	if strings.TrimSpace(match.Provider) == "" || strings.TrimSpace(match.AuthKind) == "" ||
		strings.TrimSpace(match.UpstreamModel) == "" || strings.TrimSpace(match.UpstreamFormat) == "" {
		return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("route match requires provider, auth-kind, upstream-model, and upstream-format"))
	}
	if strings.EqualFold(strings.TrimSpace(match.AuthKind), "api-key") && strings.TrimSpace(match.BaseURL) == "" {
		return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("api-key routes require base-url"))
	}
	if trimmed := strings.TrimSpace(match.BaseURL); trimmed != "" {
		if strings.Contains(trimmed, "@") || strings.Contains(trimmed, "?") || strings.Contains(trimmed, "#") {
			return unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("base-url must not carry userinfo, query, or fragment"))
		}
	}
	return nil
}

// NormalizeBaseURL canonicalizes endpoint spellings for comparison: lowercase
// scheme and host, dropped default ports, no trailing slash. The path keeps
// its case.
func NormalizeBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	scheme := ""
	rest := trimmed
	restLower := lower
	if index := strings.Index(lower, "://"); index >= 0 {
		scheme = lower[:index]
		rest = trimmed[index+3:]
		restLower = lower[index+3:]
	}
	host := rest
	path := ""
	if index := strings.Index(rest, "/"); index >= 0 {
		host = rest[:index]
		path = rest[index:]
	}
	hostLower := strings.ToLower(host)
	if (scheme == "https" && strings.HasSuffix(hostLower, ":443")) || (scheme == "http" && strings.HasSuffix(hostLower, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
		hostLower = strings.ToLower(host)
		_ = restLower
	}
	_ = hostLower
	path = strings.TrimRight(path, "/")
	if scheme != "" {
		return scheme + "://" + strings.ToLower(host) + path
	}
	return strings.ToLower(host) + path
}

// MatchRoute reports whether a compiled rule matches one actual routed call.
func MatchRoute(rule RouteMatch, route Route) bool {
	if !strings.EqualFold(strings.TrimSpace(rule.Provider), strings.TrimSpace(route.Provider)) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(rule.AuthKind), strings.TrimSpace(route.AuthKind)) {
		return false
	}
	if strings.TrimSpace(rule.UpstreamModel) != strings.TrimSpace(route.UpstreamModel) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(rule.UpstreamFormat), strings.TrimSpace(route.UpstreamFormat)) {
		return false
	}
	if strings.TrimSpace(rule.BaseURL) != "" {
		return NormalizeBaseURL(rule.BaseURL) == NormalizeBaseURL(route.BaseURL)
	}
	return true
}

// ResolvePolicy finds the effective route rule for one actual routed call.
// Statically conflicting overlaps are rejected at compile time; a dynamic
// conflict surfaces here as an explicit configuration error, never as a
// silent first-match.
func ResolvePolicy(policy Policy, route Route) (RoutePolicy, bool, error) {
	var matched *RoutePolicy
	for index := range policy.Routes {
		rule := &policy.Routes[index]
		if !MatchRoute(rule.Match, route) {
			continue
		}
		if matched != nil && *matched != *rule {
			return RoutePolicy{}, true, unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("conflicting responses-tools routes match one call"))
		}
		candidate := *rule
		matched = &candidate
	}
	if matched == nil {
		return RoutePolicy{}, false, nil
	}
	return *matched, true, nil
}

// EffectivePolicy returns the policy for one actual routed call: the explicit
// rule when one matches, otherwise the convention default. Every route has an
// effective policy; a route without a rule is a supported configuration.
func EffectivePolicy(policy Policy, route Route) (RoutePolicy, error) {
	routePolicy, matched, err := ResolvePolicy(policy, route)
	if err != nil {
		return RoutePolicy{}, err
	}
	if !matched {
		routePolicy = RoutePolicy{}
	}
	// A rule may leave individual strategies on inherit, which means "follow
	// the convention for this route".
	return applyConventionDefaults(routePolicy, route), nil
}

func applyConventionDefaults(routePolicy RoutePolicy, route Route) RoutePolicy {
	convention := ConventionPolicy(route)
	if routePolicy.ClientSearch == "" {
		routePolicy.ClientSearch = convention.ClientSearch
	}
	if routePolicy.ClientSearch == ClientSearchInherit {
		routePolicy.ClientSearch = convention.ClientSearch
	}
	if routePolicy.CustomTools == "" {
		routePolicy.CustomTools = convention.CustomTools
	}
	if routePolicy.CustomGrammar == "" {
		routePolicy.CustomGrammar = convention.CustomGrammar
	}
	if routePolicy.Schema.SearchRequired == SearchRequiredInherit {
		routePolicy.Schema.SearchRequired = convention.Schema.SearchRequired
	}
	if routePolicy.Schema.LocalRefs == "" {
		routePolicy.Schema.LocalRefs = convention.Schema.LocalRefs
	}
	return routePolicy
}

// ConventionPolicy derives the default route policy from what the runtime
// already knows about the call. Convention over configuration: the format
// and the provider together decide client search handling, and no lossy
// strategy is ever inferred. Speaking the native Responses protocol is
// necessary but not sufficient: some executors rewrite or strip the
// tool_search built-in before sending. Only providers that forward it
// unchanged keep the native convention; every other upstream gets the client
// search bridge, which stays inert until the client actually declares
// tool_search.
func ConventionPolicy(route Route) RoutePolicy {
	policy := RoutePolicy{
		CustomTools:   CustomToolsInherit,
		CustomGrammar: CustomGrammarReject,
		Schema:        SchemaPolicy{LocalRefs: LocalRefsPreserve},
	}
	if IsNativeResponsesFormat(route.UpstreamFormat) && ProviderForwardsToolSearch(route.Provider) {
		policy.ClientSearch = ClientSearchNative
	} else {
		policy.ClientSearch = ClientSearchBridge
	}
	if ProviderNeedsPortableToolSurface(route.Provider) {
		policy.CustomTools = CustomToolsFunction
		policy.CustomGrammar = CustomGrammarDescribe
		policy.Schema.SearchRequired = SearchRequiredComplete
		// The same upstream rejects recursive schemas, so cyclic references
		// are flattened instead of reaching it untouched. Flattening rewrites
		// only the edges that close a cycle and preserves acyclic $defs, so
		// it stays compatible with the convention of never expanding a
		// client-authored reference.
		policy.Schema.LocalRefs = LocalRefsFlatten
	}
	return policy
}

// ProviderForwardsToolSearch reports whether the provider's executor is known
// to pass the client tool_search built-in through without rewriting it.
// Providers that are not listed default to the bridge even when they speak the
// Responses wire format, because a native passthrough would silently drop the
// discovery entry point. A routes[] rule can still override the convention
// per exact route.
func ProviderForwardsToolSearch(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "meta":
		return true
	default:
		return false
	}
}

// ProviderNeedsPortableToolSurface reports whether the provider's upstream
// rejects the native tool surface: it refuses custom tool declarations and
// requires every property to be listed in required. The search convention stays
// native because the upstream does forward tool_search, but the tools must be
// folded into portable form. A routes[] rule can still override the convention
// per exact route.
func ProviderNeedsPortableToolSurface(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "meta":
		return true
	default:
		return false
	}
}

// IsNativeResponsesFormat reports whether an upstream format speaks the
// Responses protocol natively, so tool declarations need no rewriting.
func IsNativeResponsesFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai-response", "codex":
		return true
	default:
		return false
	}
}

// CompilePolicy validates limits and every route rule and returns the
// immutable snapshot consulted per attempt. Overlapping rules with different
// strategies are rejected instead of resolved by order.
func CompilePolicy(policy Policy) (Policy, error) {
	if err := ValidateLimits(policy.Limits); err != nil {
		return Policy{}, err
	}
	for index := range policy.Routes {
		if err := ValidateRoutePolicy(policy.Routes[index]); err != nil {
			return Policy{}, err
		}
	}
	for left := 0; left < len(policy.Routes); left++ {
		for right := left + 1; right < len(policy.Routes); right++ {
			if routeMatchesOverlap(policy.Routes[left].Match, policy.Routes[right].Match) &&
				policy.Routes[left] != policy.Routes[right] {
				return Policy{}, unprocessableError(ReasonInvalidConfiguration, fmt.Errorf("overlapping responses-tools routes use conflicting strategies"))
			}
		}
	}
	compiled := policy
	compiled.Routes = append([]RoutePolicy(nil), policy.Routes...)
	return compiled, nil
}

func routeMatchesOverlap(left, right RouteMatch) bool {
	if !strings.EqualFold(strings.TrimSpace(left.Provider), strings.TrimSpace(right.Provider)) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(left.AuthKind), strings.TrimSpace(right.AuthKind)) {
		return false
	}
	if strings.TrimSpace(left.UpstreamModel) != strings.TrimSpace(right.UpstreamModel) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(left.UpstreamFormat), strings.TrimSpace(right.UpstreamFormat)) {
		return false
	}
	leftBase := NormalizeBaseURL(left.BaseURL)
	rightBase := NormalizeBaseURL(right.BaseURL)
	if leftBase == "" || rightBase == "" {
		return true
	}
	return leftBase == rightBase
}
