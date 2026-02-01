package routing

// RouteType represents the type of routing decision made for a request.
type RouteType string

const (
	// RouteTypeLocalProvider indicates the request is handled by a local OAuth provider (free).
	RouteTypeLocalProvider RouteType = "LOCAL_PROVIDER"
	// RouteTypeModelMapping indicates the request was remapped to another available model (free).
	RouteTypeModelMapping RouteType = "MODEL_MAPPING"
	// RouteTypeAmpCredits indicates the request is forwarded to ampcode.com (uses Amp credits).
	RouteTypeAmpCredits RouteType = "AMP_CREDITS"
	// RouteTypeNoProvider indicates no provider or fallback available.
	RouteTypeNoProvider RouteType = "NO_PROVIDER"
)

// RoutingRequest contains the information needed to make a routing decision.
type RoutingRequest struct {
	// RequestedModel is the model name from the incoming request.
	RequestedModel string
	// PreferLocalProvider indicates whether to prefer local providers over mappings.
	// When true, check local providers first before applying model mappings.
	PreferLocalProvider bool
	// ForceModelMapping indicates whether to force model mapping even if local provider exists.
	// When true, apply model mappings first and skip local provider checks.
	ForceModelMapping bool
}

// RoutingDecision contains the result of a routing decision.
type RoutingDecision struct {
	// RouteType indicates the type of routing decision.
	RouteType RouteType
	// ResolvedModel is the final model name after any mappings.
	ResolvedModel string
	// ProviderName is the name of the selected provider (if any).
	ProviderName string
	// FallbackModels is a list of alternative models to try if the primary fails.
	FallbackModels []string
	// ShouldProxy indicates whether the request should be proxied to ampcode.com.
	ShouldProxy bool
}

// NewRoutingDecision creates a new RoutingDecision with the given parameters.
func NewRoutingDecision(routeType RouteType, resolvedModel, providerName string, fallbackModels []string, shouldProxy bool) *RoutingDecision {
	return &RoutingDecision{
		RouteType:      routeType,
		ResolvedModel:  resolvedModel,
		ProviderName:   providerName,
		FallbackModels: fallbackModels,
		ShouldProxy:    shouldProxy,
	}
}

// IsLocal returns true if the decision routes to a local provider.
func (d *RoutingDecision) IsLocal() bool {
	return d.RouteType == RouteTypeLocalProvider || d.RouteType == RouteTypeModelMapping
}

// HasFallbacks returns true if there are fallback models available.
func (d *RoutingDecision) HasFallbacks() bool {
	return len(d.FallbackModels) > 0
}
