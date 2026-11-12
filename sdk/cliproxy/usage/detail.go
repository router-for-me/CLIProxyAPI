package usage

import "maps"

// CacheDetails holds prompt cache configuration and TTL-bucketed write token counts.
type CacheDetails struct {
	Mode                    string           `json:"mode,omitempty"`
	TTL                     string           `json:"ttl,omitempty"`
	Retention               string           `json:"retention,omitempty"`
	Ephemeral5mInputTokens  *int64           `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral30mInputTokens *int64           `json:"ephemeral_30m_input_tokens,omitempty"`
	Ephemeral1hInputTokens  *int64           `json:"ephemeral_1h_input_tokens,omitempty"`
	Ephemeral12hInputTokens *int64           `json:"ephemeral_12h_input_tokens,omitempty"`
	ByTTL                   map[string]int64 `json:"by_ttl,omitempty"`
}

// ToolUsageDetails holds structured server-side tool execution counters.
type ToolUsageDetails struct {
	WebSearchCalls         *int64           `json:"web_search_calls,omitempty"`
	WebSearchQueries       *int64           `json:"web_search_queries,omitempty"`
	WebFetchCalls          *int64           `json:"web_fetch_calls,omitempty"`
	NumSearchResults       *int64           `json:"num_search_results,omitempty"`
	NumSourcesUsed         *int64           `json:"num_sources_used,omitempty"`
	NumServerSideToolsUsed *int64           `json:"num_server_side_tools_used,omitempty"`
	ToolCallsRequested     *int64           `json:"tool_calls_requested,omitempty"`
	ToolCallsExecuted      *int64           `json:"tool_calls_executed,omitempty"`
	XSearchCalls           *int64           `json:"x_search_calls,omitempty"`
	XPostsFetched          *int64           `json:"x_posts_fetched,omitempty"`
	XUsersFetched          *int64           `json:"x_users_fetched,omitempty"`
	CodeExecutionCalls     *int64           `json:"code_execution_calls,omitempty"`
	FileSearchCalls        *int64           `json:"file_search_calls,omitempty"`
	MCPCalls               *int64           `json:"mcp_calls,omitempty"`
	DocumentSearchCalls    *int64           `json:"document_search_calls,omitempty"`
	ImageGenerationCalls   *int64           `json:"image_generation_calls,omitempty"`
	Extra                  map[string]int64 `json:"extra,omitempty"`
}

// IterationUsage holds per-iteration usage reported in usage.iterations[].
type IterationUsage struct {
	Type                     string        `json:"type,omitempty"`
	Model                    string        `json:"model,omitempty"`
	InputTokens              *int64        `json:"input_tokens,omitempty"`
	OutputTokens             *int64        `json:"output_tokens,omitempty"`
	ThinkingTokens           *int64        `json:"thinking_tokens,omitempty"`
	ReasoningTokens          *int64        `json:"reasoning_tokens,omitempty"`
	CacheReadInputTokens     *int64        `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int64        `json:"cache_creation_input_tokens,omitempty"`
	CacheCreation5mTokens    *int64        `json:"cache_creation_5m_tokens,omitempty"`
	CacheCreation1hTokens    *int64        `json:"cache_creation_1h_tokens,omitempty"`
	CacheCreation            *CacheDetails `json:"cache_creation,omitempty"`
}

// ImageDetails holds image/video generation dimension and quality attributes.
type ImageDetails struct {
	Size           string `json:"size,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	ImageSize      string `json:"image_size,omitempty"`
	AspectRatio    string `json:"aspect_ratio,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Background     string `json:"background,omitempty"`
	OutputFormat   string `json:"output_format,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	MimeType       string `json:"mime_type,omitempty"`
	InputFidelity  string `json:"input_fidelity,omitempty"`
}

// Clone returns a deep copy of ImageDetails.
func (d *ImageDetails) Clone() *ImageDetails {
	if d == nil {
		return nil
	}
	out := *d
	return &out
}

// MediaCounts holds prompt/completion media item counts and audio/video duration.
type MediaCounts struct {
	PromptMedia     *int64   `json:"prompt_media,omitempty"`
	CompletionMedia *int64   `json:"completion_media,omitempty"`
	PromptAudio     *int64   `json:"prompt_audio,omitempty"`
	AudioSeconds    *float64 `json:"audio_seconds,omitempty"`
}

// Clone returns a deep copy of MediaCounts.
func (m *MediaCounts) Clone() *MediaCounts {
	if m == nil {
		return nil
	}
	return &MediaCounts{
		PromptMedia:     cloneInt64Ptr(m.PromptMedia),
		CompletionMedia: cloneInt64Ptr(m.CompletionMedia),
		PromptAudio:     cloneInt64Ptr(m.PromptAudio),
		AudioSeconds:    cloneFloat64Ptr(m.AudioSeconds),
	}
}

// CostDetails holds optional upstream-reported cost information (e.g. OpenRouter).
type CostDetails struct {
	Cost                             *float64 `json:"cost,omitempty"`
	CostInUSDTicks                   *int64   `json:"cost_in_usd_ticks,omitempty"`
	IsBYOK                           *bool    `json:"is_byok,omitempty"`
	UpstreamInferenceCost            *float64 `json:"upstream_inference_cost,omitempty"`
	UpstreamInferencePromptCost      *float64 `json:"upstream_inference_prompt_cost,omitempty"`
	UpstreamInferenceCompletionsCost *float64 `json:"upstream_inference_completions_cost,omitempty"`
}

// Int64Ptr returns a pointer to v.
func Int64Ptr(v int64) *int64 {
	return &v
}

// Float64Ptr returns a pointer to v.
func Float64Ptr(v float64) *float64 {
	return &v
}

// BoolPtr returns a pointer to v.
func BoolPtr(v bool) *bool {
	return &v
}

func cloneInt64Ptr(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneFloat64Ptr(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// CloneInt64Ptr returns a shallow copy of p when non-nil.
func CloneInt64Ptr(p *int64) *int64 {
	return cloneInt64Ptr(p)
}

// Clone returns a deep copy of CacheDetails.
func (c *CacheDetails) Clone() *CacheDetails {
	if c == nil {
		return nil
	}
	out := &CacheDetails{
		Mode:                    c.Mode,
		TTL:                     c.TTL,
		Retention:               c.Retention,
		Ephemeral5mInputTokens:  cloneInt64Ptr(c.Ephemeral5mInputTokens),
		Ephemeral30mInputTokens: cloneInt64Ptr(c.Ephemeral30mInputTokens),
		Ephemeral1hInputTokens:  cloneInt64Ptr(c.Ephemeral1hInputTokens),
		Ephemeral12hInputTokens: cloneInt64Ptr(c.Ephemeral12hInputTokens),
	}
	if len(c.ByTTL) > 0 {
		out.ByTTL = maps.Clone(c.ByTTL)
	}
	return out
}

// SumTokens returns the sum of TTL-bucketed tokens and whether at least one TTL bucket was present.
func (c *CacheDetails) SumTokens() (int64, bool) {
	if c == nil || len(c.ByTTL) == 0 {
		return 0, false
	}
	var sum int64
	for _, v := range c.ByTTL {
		if v < 0 || sum > int64(^uint64(0)>>1)-v {
			return 0, false
		}
		sum += v
	}
	return sum, true
}

// Clone returns a deep copy of ToolUsageDetails.
func (t *ToolUsageDetails) Clone() *ToolUsageDetails {
	if t == nil {
		return nil
	}
	out := &ToolUsageDetails{
		WebSearchCalls:         cloneInt64Ptr(t.WebSearchCalls),
		WebSearchQueries:       cloneInt64Ptr(t.WebSearchQueries),
		WebFetchCalls:          cloneInt64Ptr(t.WebFetchCalls),
		NumSearchResults:       cloneInt64Ptr(t.NumSearchResults),
		NumSourcesUsed:         cloneInt64Ptr(t.NumSourcesUsed),
		NumServerSideToolsUsed: cloneInt64Ptr(t.NumServerSideToolsUsed),
		ToolCallsRequested:     cloneInt64Ptr(t.ToolCallsRequested),
		ToolCallsExecuted:      cloneInt64Ptr(t.ToolCallsExecuted),
		XSearchCalls:           cloneInt64Ptr(t.XSearchCalls),
		XPostsFetched:          cloneInt64Ptr(t.XPostsFetched),
		XUsersFetched:          cloneInt64Ptr(t.XUsersFetched),
		CodeExecutionCalls:     cloneInt64Ptr(t.CodeExecutionCalls),
		FileSearchCalls:        cloneInt64Ptr(t.FileSearchCalls),
		MCPCalls:               cloneInt64Ptr(t.MCPCalls),
		DocumentSearchCalls:    cloneInt64Ptr(t.DocumentSearchCalls),
		ImageGenerationCalls:   cloneInt64Ptr(t.ImageGenerationCalls),
	}
	if len(t.Extra) > 0 {
		out.Extra = maps.Clone(t.Extra)
	}
	return out
}

// Clone returns a deep copy of IterationUsage.
func (it IterationUsage) Clone() IterationUsage {
	return IterationUsage{
		Type:                     it.Type,
		Model:                    it.Model,
		InputTokens:              cloneInt64Ptr(it.InputTokens),
		OutputTokens:             cloneInt64Ptr(it.OutputTokens),
		ThinkingTokens:           cloneInt64Ptr(it.ThinkingTokens),
		ReasoningTokens:          cloneInt64Ptr(it.ReasoningTokens),
		CacheReadInputTokens:     cloneInt64Ptr(it.CacheReadInputTokens),
		CacheCreationInputTokens: cloneInt64Ptr(it.CacheCreationInputTokens),
		CacheCreation5mTokens:    cloneInt64Ptr(it.CacheCreation5mTokens),
		CacheCreation1hTokens:    cloneInt64Ptr(it.CacheCreation1hTokens),
		CacheCreation:            it.CacheCreation.Clone(),
	}
}

// CloneIterations returns a deep copy of a slice of IterationUsage.
func CloneIterations(items []IterationUsage) []IterationUsage {
	if len(items) == 0 {
		return nil
	}
	out := make([]IterationUsage, len(items))
	for i := range items {
		out[i] = items[i].Clone()
	}
	return out
}

// IterationsPtr returns a pointer to a cloned non-empty IterationUsage slice, or nil when empty.
func IterationsPtr(items []IterationUsage) *[]IterationUsage {
	cloned := CloneIterations(items)
	if len(cloned) == 0 {
		return nil
	}
	return &cloned
}

// IterationSlice returns a deep copy of Detail.Iterations when present.
func (d Detail) IterationSlice() []IterationUsage {
	if d.Iterations == nil {
		return nil
	}
	return CloneIterations(*d.Iterations)
}

// Clone returns a deep copy of CostDetails.
func (c *CostDetails) Clone() *CostDetails {
	if c == nil {
		return nil
	}
	return &CostDetails{
		Cost:                             cloneFloat64Ptr(c.Cost),
		CostInUSDTicks:                   cloneInt64Ptr(c.CostInUSDTicks),
		IsBYOK:                           cloneBoolPtr(c.IsBYOK),
		UpstreamInferenceCost:            cloneFloat64Ptr(c.UpstreamInferenceCost),
		UpstreamInferencePromptCost:      cloneFloat64Ptr(c.UpstreamInferencePromptCost),
		UpstreamInferenceCompletionsCost: cloneFloat64Ptr(c.UpstreamInferenceCompletionsCost),
	}
}
