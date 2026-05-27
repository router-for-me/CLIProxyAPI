package management

import (
	"context"
	"net/http"
	"sort"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type aggregatedTokens struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type aggregatedDetail struct {
	Timestamp string           `json:"timestamp"`
	LatencyMs int64            `json:"latency_ms"`
	Source    string           `json:"source"`
	AuthIndex string           `json:"auth_index"`
	AuthType  string           `json:"auth_type"`
	Provider  string           `json:"provider"`
	Failed    bool             `json:"failed"`
	Tokens    aggregatedTokens `json:"tokens"`
}

type aggregatedModel struct {
	Details []aggregatedDetail `json:"details"`
}

type aggregatedAPI struct {
	TotalRequests int64                       `json:"total_requests"`
	TotalTokens   int64                       `json:"total_tokens"`
	Models        map[string]*aggregatedModel `json:"models"`
}

type aggregatedUsageData struct {
	Apis map[string]*aggregatedAPI `json:"apis"`
}

type aggregatedUsageResponse struct {
	Usage aggregatedUsageData `json:"usage"`
}

const (
	aggregatedMaxDetailsPerModel = 5000
	aggregatedUnknownAPIKey      = "unknown"
)

type aggregatedStore struct {
	mu   sync.Mutex
	apis map[string]*aggregatedAPI
}

func newAggregatedStore() *aggregatedStore {
	return &aggregatedStore{apis: make(map[string]*aggregatedAPI)}
}

var aggregatedUsageStore = newAggregatedStore()

func init() {
	usage.RegisterPlugin(aggregatedUsageStore)
}

func (s *aggregatedStore) HandleUsage(_ context.Context, record usage.Record) {
	if s == nil {
		return
	}

	apiKey := record.APIKey
	if apiKey == "" {
		apiKey = aggregatedUnknownAPIKey
	}
	model := record.Model
	if model == "" {
		model = "unknown"
	}

	totalTokens := record.Detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens
		if totalTokens == 0 {
			totalTokens = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens + record.Detail.CachedTokens
		}
	}

	detail := aggregatedDetail{
		Timestamp: record.RequestedAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		LatencyMs: record.Latency.Milliseconds(),
		Source:    record.Source,
		AuthIndex: record.AuthIndex,
		AuthType:  record.AuthType,
		Provider:  record.Provider,
		Failed:    record.Failed,
		Tokens: aggregatedTokens{
			InputTokens:         record.Detail.InputTokens,
			OutputTokens:        record.Detail.OutputTokens,
			ReasoningTokens:     record.Detail.ReasoningTokens,
			CachedTokens:        record.Detail.CachedTokens,
			CacheReadTokens:     record.Detail.CacheReadTokens,
			CacheCreationTokens: record.Detail.CacheCreationTokens,
			TotalTokens:         totalTokens,
		},
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	api, ok := s.apis[apiKey]
	if !ok {
		api = &aggregatedAPI{Models: make(map[string]*aggregatedModel)}
		s.apis[apiKey] = api
	}
	api.TotalRequests++
	api.TotalTokens += totalTokens

	m, ok := api.Models[model]
	if !ok {
		m = &aggregatedModel{Details: make([]aggregatedDetail, 0, 16)}
		api.Models[model] = m
	}
	m.Details = append(m.Details, detail)
	if len(m.Details) > aggregatedMaxDetailsPerModel {
		excess := len(m.Details) - aggregatedMaxDetailsPerModel
		m.Details = m.Details[excess:]
	}
}

func (s *aggregatedStore) snapshot() aggregatedUsageData {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := aggregatedUsageData{Apis: make(map[string]*aggregatedAPI, len(s.apis))}
	for apiKey, api := range s.apis {
		clone := &aggregatedAPI{
			TotalRequests: api.TotalRequests,
			TotalTokens:   api.TotalTokens,
			Models:        make(map[string]*aggregatedModel, len(api.Models)),
		}
		for model, m := range api.Models {
			details := make([]aggregatedDetail, len(m.Details))
			copy(details, m.Details)
			sort.SliceStable(details, func(i, j int) bool {
				return details[i].Timestamp < details[j].Timestamp
			})
			clone.Models[model] = &aggregatedModel{Details: details}
		}
		out.Apis[apiKey] = clone
	}
	return out
}

func (s *aggregatedStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apis = make(map[string]*aggregatedAPI)
}

// GetUsageAggregated returns cumulative per-api-key usage in a shape compatible
// with external leaderboard/dashboard tools that expect the legacy
// /v0/management/usage payload. Records are sourced from the in-process usage
// plugin pipeline and accumulate over the process lifetime.
func (h *Handler) GetUsageAggregated(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	c.JSON(http.StatusOK, aggregatedUsageResponse{Usage: aggregatedUsageStore.snapshot()})
}
