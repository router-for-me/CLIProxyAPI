package websearch

import (
	"fmt"
	"strings"
)

// Recency is a relative time filter shared by providers that support it.
type Recency string

// Supported recency filters.
const (
	RecencyDay   Recency = "day"
	RecencyWeek  Recency = "week"
	RecencyMonth Recency = "month"
	RecencyYear  Recency = "year"
)

// ParseRecency normalizes a user-supplied recency value.
func ParseRecency(value string) Recency {
	switch Recency(strings.ToLower(strings.TrimSpace(value))) {
	case RecencyDay:
		return RecencyDay
	default:
		return ""
	}
}

// SearchRequest is one web query with shared result shaping options.
type SearchRequest struct {
	// Query is the raw user query, including any Google-style directives.
	Query string
	// Limit caps returned sources. Zero selects the configured default.
	Limit int
	// Recency optionally restricts results to recent content.
	Recency Recency
	// Provider forces a single provider. Empty or "auto" walks the chain.
	Provider string
	// MaxTokens is the provider token cap for LLM-grounded providers.
	MaxTokens int
	// Temperature is forwarded only by providers whose API supports it.
	Temperature float64
	// NumSearchResults is the upstream result-breadth request. Providers
	// without such a field use it as a local cap.
	NumSearchResults int
}

// ResultCount collapses the two result-count concepts the way adapters
// do upstream: NumSearchResults wins over Limit, defaulting to 0.
func (r SearchRequest) ResultCount() int {
	if r.NumSearchResults > 0 {
		return r.NumSearchResults
	}
	return r.Limit
}

// Source is one normalized search hit.
type Source struct {
	Title     string
	URL       string
	Snippet   string
	Published string
}

// Citation is one grounded citation backing generated answer text.
type Citation struct {
	URL       string
	Title     string
	CitedText string
}

// SearchResponse is the unified result all providers return.
type SearchResponse struct {
	Provider  string
	Answer    string
	Sources   []Source
	Citations []Citation
	Related   []string
	// SearchQueries records queries the backend actually executed.
	SearchQueries []string
	// Notes carries leading relaxation notes, e.g. when a query
	// constraint matched nothing and was relaxed.
	Notes []string
	// Model names the grounding model for LLM-backed providers.
	Model string
	// AuthMode records which credential path answered, e.g. "api_key",
	// "oauth", or "anonymous" for keyless providers.
	AuthMode string
	// RequestID carries the upstream request id when the backend reports one.
	RequestID string
	// Usage counts search requests when the backend reports them, e.g.
	// Anthropic's server_tool_use.web_search_requests.
	Usage SearchUsage
}

// SearchUsage reports provider-side search accounting.
type SearchUsage struct {
	// SearchRequests is the number of backend search calls.
	SearchRequests int
	// InputTokens and OutputTokens are set by backends that report tokens.
	InputTokens  int64
	OutputTokens int64
}

// HasRenderableContent reports whether the response carries anything worth
// showing to a model. Empty responses advance the provider chain.
func (r SearchResponse) HasRenderableContent() bool {
	return strings.TrimSpace(r.Answer) != "" ||
		len(r.Sources) > 0 ||
		len(r.Citations) > 0 ||
		len(r.Related) > 0
}

// ProviderError is a tagged provider failure. Status is the upstream HTTP
// status when known, otherwise 0. A synthesized 204 marks an empty but
// otherwise successful response so the chain advances.
type ProviderError struct {
	Provider string
	Message  string
	Status   int
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Status > 0 {
		return fmt.Sprintf("%s search failed (status %d): %s", e.Provider, e.Status, e.Message)
	}
	return fmt.Sprintf("%s search failed: %s", e.Provider, e.Message)
}
