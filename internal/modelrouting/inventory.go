package modelrouting

import (
	"time"

	"gopkg.in/yaml.v3"
)

// RoutingYAML renders the versioned routing resource independently of the host configuration.
func RoutingYAML(projection *Config) ([]byte, error) {
	return yaml.Marshal(struct {
		ModelRouting *Config `yaml:"model-routing"`
	}{ModelRouting: projection})
}

// ActiveIdentityV2 is the complete compare-and-swap identity of the active
// runtime configuration. ConfigDigest never appears inside Config itself.
type ActiveIdentityV2 struct {
	Generation       uint64 `json:"generation"`
	SnapshotDigest   string `json:"snapshot_digest"`
	ProjectionDigest string `json:"projection_digest"`
	ConfigDigest     string `json:"config_digest"`
}

// BinaryProvenance identifies the exact CLIProxy binary serving an inventory.
type BinaryProvenance struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

// Inventory is the route-aware, secret-free management response.
type Inventory struct {
	SchemaVersion      int               `json:"schema_version"`
	GeneratedAt        time.Time         `json:"generated_at"`
	Active             *ActiveIdentityV2 `json:"active"`
	ActivationLoadedAt *time.Time        `json:"activation_loaded_at"`
	BinaryProvenance   BinaryProvenance  `json:"binary_provenance"`
	RoutingSchema      RoutingSchemaInfo `json:"routing_schema"`
	DirectModels       []InventoryModel  `json:"direct_models"`
	Aliases            []InventoryAlias  `json:"aliases"`
}

// ActiveStateV2 is the atomically read runtime state backing inventory recovery.
type ActiveStateV2 struct {
	Identity   *ActiveIdentityV2
	LoadedAt   *time.Time
	Projection *Config
}

type RoutingSchemaInfo struct {
	Version int    `json:"version"`
	Digest  string `json:"digest"`
}

// ActivationReceiptV2 attests to runtime adoption of a routing publication.
type ActivationReceiptV2 struct {
	PreviousActive   *ActiveIdentityV2 `json:"previous_active"`
	Active           ActiveIdentityV2  `json:"active"`
	RoutingSchema    RoutingSchemaInfo `json:"routing_schema"`
	BinaryProvenance BinaryProvenance  `json:"binary_provenance"`
	LoadedAt         time.Time         `json:"loaded_at"`
}

type InventoryModel struct {
	ModelKey    ModelKeyJSON       `json:"model_key"`
	DisplayName string             `json:"display_name"`
	Active      bool               `json:"active"`
	Variants    []InventoryVariant `json:"variants"`
	Routes      []InventoryRoute   `json:"routes"`
}

type ModelKeyJSON struct {
	CatalogProviderID string `json:"catalog_provider_id"`
	CanonicalModelID  string `json:"canonical_model_id"`
}

type RouteKeyJSON struct {
	ModelKey     ModelKeyJSON `json:"model_key"`
	RouteChannel string       `json:"route_channel"`
}

type VariantKeyJSON struct {
	ModelKey  ModelKeyJSON `json:"model_key"`
	VariantID string       `json:"variant_id"`
}

type InventoryAlias struct {
	Name       string            `json:"name"`
	TierID     string            `json:"tier_id"`
	Selectable bool              `json:"selectable"`
	Reason     string            `json:"reason"`
	Members    []InventoryMember `json:"members"`
}

type InventoryMember struct {
	ModelKey        ModelKeyJSON         `json:"model_key"`
	MemberRank      int                  `json:"member_rank"`
	ModelScore      string               `json:"model_score"`
	SelectionReason string               `json:"selection_reason"`
	Candidates      []InventoryCandidate `json:"candidates"`
}

type InventoryCandidate struct {
	RouteKey               RouteKeyJSON             `json:"route_key"`
	CatalogRouteProviderID string                   `json:"catalog_route_provider_id"`
	CatalogRouteModelID    string                   `json:"catalog_route_model_id"`
	RuntimeModelID         string                   `json:"runtime_model_id"`
	RouteSelector          string                   `json:"route_selector"`
	VariantID              *string                  `json:"variant_id"`
	RouteRank              int                      `json:"route_rank"`
	QuotaDomains           []string                 `json:"quota_domains"`
	CredentialRefs         []InventoryCredentialRef `json:"credential_refs"`
	Protocols              []string                 `json:"protocols"`
	Health                 InventoryHealth          `json:"health"`
	Restrictions           []InventoryRestriction   `json:"restrictions"`
	Pricing                *InventoryPricing        `json:"pricing"`
	SelectionReason        string                   `json:"selection_reason"`
}

// InventoryPricing is the JSON boundary representation of active projected
// pricing. It is deliberately distinct from the kebab-case routing YAML type.
type InventoryPricing struct {
	Currency string                  `json:"currency"`
	Unit     string                  `json:"unit"`
	SourceID string                  `json:"source_id"`
	Entries  []InventoryPricingEntry `json:"entries"`
}

type InventoryPricingEntry struct {
	Name       string  `json:"name"`
	Amount     string  `json:"amount"`
	TierType   *string `json:"tier_type"`
	TierSize   *int64  `json:"tier_size"`
	ContextKey *string `json:"context_key"`
}

type InventoryVariant struct {
	VariantKey  VariantKeyJSON `json:"variant_key"`
	DisplayName *string        `json:"display_name"`
	Protocols   []string       `json:"protocols"`
}

type InventoryRoute struct {
	RouteKey               RouteKeyJSON           `json:"route_key"`
	CatalogRouteProviderID string                 `json:"catalog_route_provider_id"`
	CatalogRouteModelID    string                 `json:"catalog_route_model_id"`
	RuntimeModelID         string                 `json:"runtime_model_id"`
	RouteSelector          string                 `json:"route_selector"`
	QuotaDomains           []string               `json:"quota_domains"`
	Protocols              []string               `json:"protocols"`
	Restrictions           []InventoryRestriction `json:"restrictions"`
	Health                 InventoryHealth        `json:"health"`
	Selectable             bool                   `json:"selectable"`
	SelectionReason        string                 `json:"selection_reason"`
	Credentials            []InventoryCredential  `json:"credentials"`
}

type InventoryRestriction struct {
	RuleID     string `json:"rule_id"`
	ConfigPath string `json:"config_path"`
	Active     bool   `json:"active"`
	Reason     string `json:"reason"`
}

type InventoryHealth struct {
	Status     string `json:"status"`
	Selectable bool   `json:"selectable"`
	ObservedAt string `json:"observed_at"`
	LatencyMS  *int64 `json:"latency_ms"`
}

type InventoryCredentialRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type InventoryCredential struct {
	Revision      string                 `json:"revision,omitempty"`
	CredentialRef InventoryCredentialRef `json:"credential_ref"`
	QuotaDomain   string                 `json:"quota_domain"`
	Health        InventoryHealth        `json:"health"`
	Quota         InventoryQuota         `json:"quota"`
	Suspension    InventorySuspension    `json:"suspension"`
	Restrictions  []InventoryRestriction `json:"restrictions"`
}

type InventoryQuota struct {
	Status    string  `json:"status"`
	Remaining *string `json:"remaining"`
	ResetsAt  *string `json:"resets_at"`
	Reason    *string `json:"reason"`
}

type InventorySuspension struct {
	Active    bool    `json:"active"`
	Reason    *string `json:"reason"`
	ResumesAt *string `json:"resumes_at"`
}
