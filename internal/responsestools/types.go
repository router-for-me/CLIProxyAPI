// Package responsestools carries the Responses client tool protocol handling
// (tool_search discovery and custom tools) in the CLIProxyAPI core.
//
// The package is pure protocol logic: it depends only on the standard library
// and never on auth, Gin, the plugin host, or network transport. An execution
// attempt owns its Attempt object; attempts are never shared across requests
// and must be rebuilt for every retry from the original client contract.
package responsestools

// ToolKind identifies the protocol kind of one tool declaration.
type ToolKind string

const (
	// ToolKindFunction is an ordinary callable function tool.
	ToolKindFunction ToolKind = "function"
	// ToolKindCustom is a freeform custom tool declaration.
	ToolKindCustom ToolKind = "custom"
	// ToolKindClientSearch is a client-executed tool_search declaration.
	ToolKindClientSearch ToolKind = "client-search"
	// ToolKindServerSearch is a server-executed tool_search declaration.
	ToolKindServerSearch ToolKind = "server-search"
)

// ToolIdentity is the canonical identity of one tool declaration: its
// namespace, local name, and protocol kind. Two declarations that differ in
// any of the three fields are distinct tools even when their flattened
// upstream names collide.
type ToolIdentity struct {
	Namespace string
	Name      string
	Kind      ToolKind
}

// ClientSearchMode controls how a matched route treats client-executed
// tool_search declarations.
type ClientSearchMode string

const (
	// ClientSearchInherit follows the convention policy for the route.
	ClientSearchInherit ClientSearchMode = "inherit"
	// ClientSearchNative passes the native Responses route through untouched.
	ClientSearchNative ClientSearchMode = "native"
	// ClientSearchBridge translates client search to ordinary functions.
	ClientSearchBridge ClientSearchMode = "bridge"
	// ClientSearchDisabled rejects explicit client search requests with 422.
	ClientSearchDisabled ClientSearchMode = "disabled"
)

// CustomToolsMode controls how a matched route treats custom tool declarations.
type CustomToolsMode string

const (
	// CustomToolsInherit leaves custom tools untouched.
	CustomToolsInherit CustomToolsMode = "inherit"
	// CustomToolsNative keeps custom declarations on native Responses routes.
	CustomToolsNative CustomToolsMode = "native"
	// CustomToolsFunction wraps custom tools as single-string functions.
	CustomToolsFunction CustomToolsMode = "function"
	// CustomToolsStrip drops future custom declarations (lossy) while keeping
	// convertible history. Forced calls to stripped tools fail with 422.
	CustomToolsStrip CustomToolsMode = "strip"
	// CustomToolsReject rejects any custom contract with 422.
	CustomToolsReject CustomToolsMode = "reject"
)

// CustomGrammarMode controls how grammar payloads are handled in function mode.
type CustomGrammarMode string

const (
	// CustomGrammarReject rejects grammar payloads that cannot be preserved.
	CustomGrammarReject CustomGrammarMode = "reject"
	// CustomGrammarDescribe keeps the tool callable by folding the grammar
	// syntax and definition into its description. Sampling constraints implied
	// by the grammar are not enforced upstream.
	CustomGrammarDescribe CustomGrammarMode = "describe"
)

// LocalRefsMode controls how local JSON schema references are handled.
type LocalRefsMode string

const (
	// LocalRefsPreserve keeps $ref entries untouched.
	LocalRefsPreserve LocalRefsMode = "preserve"
	// LocalRefsInline expands local references, rejecting recursive, external,
	// missing, or unsupported references instead of deleting constraints.
	LocalRefsInline LocalRefsMode = "inline"
)

// SearchRequiredMode selects how client-search parameter schemas are adapted.
type SearchRequiredMode string

const (
	// SearchRequiredInherit leaves the strategy to the route convention.
	SearchRequiredInherit SearchRequiredMode = ""
	// SearchRequiredComplete completes required fields and widens optional
	// fields to nullable.
	SearchRequiredComplete SearchRequiredMode = "complete"
	// SearchRequiredPreserve leaves the declared schemas untouched.
	SearchRequiredPreserve SearchRequiredMode = "preserve"
)

// SchemaPolicy tunes strict-schema handling for one route.
type SchemaPolicy struct {
	// SearchRequired adapts only client-search parameter schemas.
	SearchRequired SearchRequiredMode
	// LocalRefs selects local $ref handling.
	LocalRefs LocalRefsMode
}

// CompletesSearchSchemas reports whether search parameter schemas get their
// required fields completed and their optional fields widened to nullable.
func (s SchemaPolicy) CompletesSearchSchemas() bool {
	return s.SearchRequired == SearchRequiredComplete
}

// RouteMatch binds a policy to one exact upstream route. All match fields are
// required; wildcards and regex matching are not supported.
type RouteMatch struct {
	Provider       string
	AuthKind       string
	UpstreamModel  string
	UpstreamFormat string
	// BaseURL is required for API-key routes so the same provider/model name
	// on different endpoints cannot share one rule. OAuth routes leave it empty.
	BaseURL string
}

// RoutePolicy is one compiled route rule with its tool handling strategies.
type RoutePolicy struct {
	Match         RouteMatch
	ClientSearch  ClientSearchMode
	CustomTools   CustomToolsMode
	CustomGrammar CustomGrammarMode
	Schema        SchemaPolicy
}

// Limits bounds retained protocol state. All values must be positive; negative
// values never mean unlimited.
type Limits struct {
	MaxActiveToolBytes      int
	MaxActiveAttempts       int
	MaxStateBytes           int
	MaxAttemptBytes         int
	MaxSchemaExpansionBytes int
	MaxSchemaExpansionNodes int
	MaxDepth                int
}

// DefaultLimits returns the reference budget defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxActiveToolBytes:      262144,
		MaxActiveAttempts:       512,
		MaxStateBytes:           33554432,
		MaxAttemptBytes:         1048576,
		MaxSchemaExpansionBytes: 65536,
		MaxSchemaExpansionNodes: 10000,
		MaxDepth:                64,
	}
}

// Policy is the immutable compiled snapshot consulted per attempt.
type Policy struct {
	Enabled bool
	Limits  Limits
	Routes  []RoutePolicy
}

// Route selects the effective policy for one actual routed call.
type Route struct {
	Provider       string
	AuthKind       string
	UpstreamModel  string
	UpstreamFormat string
	BaseURL        string
}
