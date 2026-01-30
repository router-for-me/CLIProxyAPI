package ctxkeys

type key string

const (
	MappedModel     key = "mapped_model"
	FallbackModels  key = "fallback_models"
	RouteCandidates key = "route_candidates"
	RoutingDecision key = "routing_decision"
	MappingApplied  key = "mapping_applied"
)
