package management

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ListErrorEvents returns paged MongoDB records of failed request events.
func (h *Handler) ListErrorEvents(c *gin.Context) {
	store := mongostate.GetGlobalErrorEventStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "error event store unavailable"})
		return
	}

	start, err := parseRFC3339QueryTime(c.Query("start"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time, expected RFC3339"})
		return
	}
	end, err := parseRFC3339QueryTime(c.Query("end"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time, expected RFC3339"})
		return
	}

	statusCode, err := parseOptionalIntQuery(c.Query("status_code"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status_code"})
		return
	}

	query := mongostate.ErrorEventQuery{
		Provider:     strings.ToLower(strings.TrimSpace(c.Query("provider"))),
		AuthID:       strings.TrimSpace(c.Query("auth_id")),
		Model:        strings.TrimSpace(c.Query("model")),
		FailureStage: strings.ToLower(strings.TrimSpace(c.Query("failure_stage"))),
		ErrorCode:    strings.ToLower(strings.TrimSpace(c.Query("error_code"))),
		StatusCode:   statusCode,
		RequestID:    strings.TrimSpace(c.Query("request_id")),
		Start:        start,
		End:          end,
		Page:         parsePositiveIntQuery(c.Query("page"), 1),
		PageSize:     parsePositiveIntQuery(c.Query("page_size"), 20),
	}

	result, err := store.Query(c.Request.Context(), query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.enrichErrorEventProgress(c.Request.Context(), &result); err != nil {
		log.WithError(err).Warn("management: enrich error-event progress")
	}
	c.JSON(http.StatusOK, result)
}

// SummarizeErrorEvents returns aggregated error-event buckets.
func (h *Handler) SummarizeErrorEvents(c *gin.Context) {
	store := mongostate.GetGlobalErrorEventStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "error event store unavailable"})
		return
	}
	summarizer, ok := store.(interface {
		Summarize(ctx context.Context, query mongostate.ErrorEventSummaryQuery) (mongostate.ErrorEventSummaryResult, error)
	})
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "error event summary unavailable"})
		return
	}

	start, err := parseRFC3339QueryTime(c.Query("start"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time, expected RFC3339"})
		return
	}
	end, err := parseRFC3339QueryTime(c.Query("end"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time, expected RFC3339"})
		return
	}
	statusCode, err := parseOptionalIntQuery(c.Query("status_code"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status_code"})
		return
	}
	limit := parsePositiveIntQuery(c.Query("limit"), 100)
	if limit > 500 {
		limit = 500
	}

	query := mongostate.ErrorEventSummaryQuery{
		Provider:     strings.ToLower(strings.TrimSpace(c.Query("provider"))),
		AuthID:       strings.TrimSpace(c.Query("auth_id")),
		Model:        strings.TrimSpace(c.Query("model")),
		FailureStage: strings.ToLower(strings.TrimSpace(c.Query("failure_stage"))),
		ErrorCode:    strings.ToLower(strings.TrimSpace(c.Query("error_code"))),
		StatusCode:   statusCode,
		Start:        start,
		End:          end,
		GroupBy:      parseErrorEventSummaryGroupBy(c.Query("group_by")),
		Limit:        limit,
	}

	result, err := summarizer.Summarize(c.Request.Context(), query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.enrichErrorEventSummaryProgress(c.Request.Context(), &result); err != nil {
		log.WithError(err).Warn("management: enrich error-event summary progress")
	}
	c.JSON(http.StatusOK, result)
}

func parseOptionalIntQuery(raw string) (*int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseErrorEventSummaryGroupBy(raw string) []string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func (h *Handler) enrichErrorEventProgress(ctx context.Context, result *mongostate.ErrorEventQueryResult) error {
	if result == nil || len(result.Items) == 0 {
		return nil
	}

	type scopeInfo struct {
		provider        string
		authID          string
		normalizedModel string
	}

	scopeByKey := make(map[string]scopeInfo, len(result.Items))
	scopeKeys := make([]string, 0, len(result.Items))
	for index := range result.Items {
		item := &result.Items[index]
		scopeKey := buildErrorEventProgressScopeKey(item)
		item.ProgressScopeKey = scopeKey
		if scopeKey == "" {
			continue
		}
		if _, exists := scopeByKey[scopeKey]; exists {
			continue
		}
		scopeByKey[scopeKey] = scopeInfo{
			provider:        strings.TrimSpace(item.Provider),
			authID:          strings.TrimSpace(item.AuthID),
			normalizedModel: resolveErrorEventNormalizedModel(*item),
		}
		scopeKeys = append(scopeKeys, scopeKey)
	}
	if len(scopeByKey) == 0 {
		return nil
	}

	breakerStatuses := registry.GetGlobalRegistry().GetCircuitBreakerStatus()
	latestDeletionRecords := map[string]mongostate.CircuitBreakerDeletionRecord{}
	if deletionStore := mongostate.GetGlobalCircuitBreakerDeletionStore(); deletionStore != nil {
		if finder, ok := deletionStore.(mongostate.CircuitBreakerDeletionLatestFinder); ok {
			records, err := finder.FindLatestByDedupeKeys(ctx, scopeKeys)
			if err != nil {
				return err
			}
			latestDeletionRecords = records
		}
	}

	if result.Meta.ProgressByScope == nil {
		result.Meta.ProgressByScope = make(map[string]mongostate.ErrorEventProgressSnapshot, len(scopeByKey))
	}

	for scopeKey, scope := range scopeByKey {
		breakerThreshold := h.resolveErrorEventBreakerThreshold(scope.authID)
		if breakerThreshold <= 0 {
			breakerThreshold = registry.DefaultCircuitBreakerFailureThreshold
		}

		breakerStatus, breakerFound := lookupErrorEventBreakerStatus(breakerStatuses, scope.authID, scope.normalizedModel)
		breakerState := string(registry.CircuitClosed)
		breakerCurrent := 0
		if breakerFound {
			breakerCurrent = breakerStatus.ConsecutiveFailures
			if strings.TrimSpace(string(breakerStatus.State)) != "" {
				breakerState = string(breakerStatus.State)
			}
		}

		deletionEnabled := true
		deletionThreshold := config.DefaultCircuitBreakerAutoRemoveThreshold
		if h != nil && h.cfg != nil {
			deletionEnabled = h.cfg.CircuitBreakerAutoRemoval.EnabledOrDefault()
			deletionThreshold = h.cfg.CircuitBreakerAutoRemoval.ThresholdOrDefault()
		}

		deletionCurrent := breakerStatus.OpenCycles
		deletionStatus := ""
		if record, ok := latestDeletionRecords[scopeKey]; ok {
			if record.OpenCycles > deletionCurrent {
				deletionCurrent = record.OpenCycles
			}
			deletionStatus = strings.TrimSpace(record.Status)
		}

		result.Meta.ProgressByScope[scopeKey] = mongostate.ErrorEventProgressSnapshot{
			Breaker: mongostate.ErrorEventBreakerProgressSnapshot{
				Current:   breakerCurrent,
				Threshold: breakerThreshold,
				State:     breakerState,
			},
			Deletion: mongostate.ErrorEventDeletionProgressSnapshot{
				Enabled:   deletionEnabled,
				Current:   deletionCurrent,
				Threshold: deletionThreshold,
				Status:    deletionStatus,
			},
		}
	}

	return nil
}

func (h *Handler) enrichErrorEventSummaryProgress(ctx context.Context, result *mongostate.ErrorEventSummaryResult) error {
	if result == nil || len(result.Items) == 0 {
		return nil
	}

	scopeByKey := make(map[string]struct{}, len(result.Items))
	scopeKeys := make([]string, 0, len(result.Items))
	for index := range result.Items {
		item := &result.Items[index]
		scopeKey := h.buildErrorEventSummaryProgressScopeKey(item)
		item.ProgressScopeKey = scopeKey
		if scopeKey == "" {
			continue
		}
		if _, exists := scopeByKey[scopeKey]; exists {
			continue
		}
		scopeByKey[scopeKey] = struct{}{}
		scopeKeys = append(scopeKeys, scopeKey)
	}
	if len(scopeKeys) == 0 {
		return nil
	}

	progressByScope, err := h.buildErrorEventProgressByScope(ctx, scopeKeys)
	if err != nil {
		return err
	}
	if len(progressByScope) == 0 {
		return nil
	}
	if result.Meta.ProgressByScope == nil {
		result.Meta.ProgressByScope = make(map[string]mongostate.ErrorEventProgressSnapshot, len(progressByScope))
	}
	for scopeKey, snapshot := range progressByScope {
		result.Meta.ProgressByScope[scopeKey] = snapshot
	}
	return nil
}

func (h *Handler) buildErrorEventSummaryProgressScopeKey(item *mongostate.ErrorEventSummaryItem) string {
	if item == nil {
		return ""
	}
	provider := strings.TrimSpace(item.Provider)
	authID := strings.TrimSpace(item.AuthID)
	normalizedModel := strings.TrimSpace(item.NormalizedModel)
	if authID == "" || normalizedModel == "" {
		return ""
	}
	if provider == "" {
		if auth, ok := h.lookupErrorEventAuth(authID); ok {
			provider = strings.TrimSpace(auth.Provider)
		}
	}
	if provider == "" {
		return ""
	}
	return mongostate.BuildErrorEventProgressScopeKey(provider, authID, normalizedModel)
}

func (h *Handler) buildErrorEventProgressByScope(ctx context.Context, scopeKeys []string) (map[string]mongostate.ErrorEventProgressSnapshot, error) {
	if len(scopeKeys) == 0 {
		return nil, nil
	}

	breakerStatuses := registry.GetGlobalRegistry().GetCircuitBreakerStatus()
	latestDeletionRecords := map[string]mongostate.CircuitBreakerDeletionRecord{}
	if deletionStore := mongostate.GetGlobalCircuitBreakerDeletionStore(); deletionStore != nil {
		if finder, ok := deletionStore.(mongostate.CircuitBreakerDeletionLatestFinder); ok {
			records, err := finder.FindLatestByDedupeKeys(ctx, scopeKeys)
			if err != nil {
				return nil, err
			}
			latestDeletionRecords = records
		}
	}

	progressByScope := make(map[string]mongostate.ErrorEventProgressSnapshot, len(scopeKeys))
	for _, scopeKey := range scopeKeys {
		scopeProvider, scopeAuthID, scopeModel := parseErrorEventProgressScopeKey(scopeKey)
		if scopeProvider == "" || scopeAuthID == "" || scopeModel == "" {
			continue
		}

		breakerThreshold := h.resolveErrorEventBreakerThreshold(scopeAuthID)
		if breakerThreshold <= 0 {
			breakerThreshold = registry.DefaultCircuitBreakerFailureThreshold
		}

		breakerStatus, breakerFound := lookupErrorEventBreakerStatus(breakerStatuses, scopeAuthID, scopeModel)
		breakerState := string(registry.CircuitClosed)
		breakerCurrent := 0
		if breakerFound {
			breakerCurrent = breakerStatus.ConsecutiveFailures
			if strings.TrimSpace(string(breakerStatus.State)) != "" {
				breakerState = string(breakerStatus.State)
			}
		}

		deletionEnabled := true
		deletionThreshold := config.DefaultCircuitBreakerAutoRemoveThreshold
		if h != nil && h.cfg != nil {
			deletionEnabled = h.cfg.CircuitBreakerAutoRemoval.EnabledOrDefault()
			deletionThreshold = h.cfg.CircuitBreakerAutoRemoval.ThresholdOrDefault()
		}

		deletionCurrent := breakerStatus.OpenCycles
		deletionStatus := ""
		if record, ok := latestDeletionRecords[scopeKey]; ok {
			if record.OpenCycles > deletionCurrent {
				deletionCurrent = record.OpenCycles
			}
			deletionStatus = strings.TrimSpace(record.Status)
		}

		progressByScope[scopeKey] = mongostate.ErrorEventProgressSnapshot{
			Breaker: mongostate.ErrorEventBreakerProgressSnapshot{
				Current:   breakerCurrent,
				Threshold: breakerThreshold,
				State:     breakerState,
			},
			Deletion: mongostate.ErrorEventDeletionProgressSnapshot{
				Enabled:   deletionEnabled,
				Current:   deletionCurrent,
				Threshold: deletionThreshold,
				Status:    deletionStatus,
			},
		}
	}

	return progressByScope, nil
}

func buildErrorEventProgressScopeKey(item *mongostate.ErrorEventItem) string {
	if item == nil {
		return ""
	}
	provider := strings.TrimSpace(item.Provider)
	authID := strings.TrimSpace(item.AuthID)
	normalizedModel := resolveErrorEventNormalizedModel(*item)
	if provider == "" || authID == "" || normalizedModel == "" {
		return ""
	}
	return mongostate.BuildErrorEventProgressScopeKey(provider, authID, normalizedModel)
}

func parseErrorEventProgressScopeKey(scopeKey string) (provider string, authID string, normalizedModel string) {
	parts := strings.SplitN(strings.TrimSpace(scopeKey), "|", 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
}

func resolveErrorEventNormalizedModel(item mongostate.ErrorEventItem) string {
	if normalized := strings.TrimSpace(item.NormalizedModel); normalized != "" {
		return normalized
	}
	return coreauth.NormalizeCircuitBreakerModelID(item.Model)
}

func lookupErrorEventBreakerStatus(statuses map[string]map[string]registry.CircuitBreakerStatus, authID string, normalizedModel string) (registry.CircuitBreakerStatus, bool) {
	if len(statuses) == 0 {
		return registry.CircuitBreakerStatus{}, false
	}
	models, ok := statuses[strings.TrimSpace(authID)]
	if !ok {
		return registry.CircuitBreakerStatus{}, false
	}
	status, ok := models[strings.TrimSpace(normalizedModel)]
	return status, ok
}

func (h *Handler) resolveErrorEventBreakerThreshold(authID string) int {
	auth, ok := h.lookupErrorEventAuth(authID)
	if !ok {
		return registry.DefaultCircuitBreakerFailureThreshold
	}

	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "codex":
		if entry := h.resolveErrorEventCodexConfig(auth); entry != nil && entry.CircuitBreakerFailureThreshold > 0 {
			return entry.CircuitBreakerFailureThreshold
		}
	case "openai-compatibility":
		if entry := h.resolveErrorEventOpenAICompatConfig(auth); entry != nil && entry.CircuitBreakerFailureThreshold > 0 {
			return entry.CircuitBreakerFailureThreshold
		}
	}

	return registry.DefaultCircuitBreakerFailureThreshold
}

func (h *Handler) lookupErrorEventAuth(authID string) (*coreauth.Auth, bool) {
	if h == nil || h.authManager == nil {
		return nil, false
	}
	return h.authManager.GetByID(strings.TrimSpace(authID))
}

func (h *Handler) resolveErrorEventCodexConfig(auth *coreauth.Auth) *config.CodexKey {
	if h == nil || h.cfg == nil || auth == nil {
		return nil
	}
	attrKey := ""
	attrBase := ""
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for index := range h.cfg.CodexKey {
		entry := &h.cfg.CodexKey[index]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	return nil
}

func (h *Handler) resolveErrorEventOpenAICompatConfig(auth *coreauth.Auth) *config.OpenAICompatibility {
	if h == nil || h.cfg == nil || auth == nil {
		return nil
	}

	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			candidates = append(candidates, compatName)
		}
		if providerKey := strings.TrimSpace(auth.Attributes["provider_key"]); providerKey != "" {
			candidates = append(candidates, providerKey)
		}
	}
	if provider := strings.TrimSpace(auth.Provider); provider != "" {
		candidates = append(candidates, provider)
	}

	for index := range h.cfg.OpenAICompatibility {
		entry := &h.cfg.OpenAICompatibility[index]
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(entry.Name)) {
				return entry
			}
		}
	}
	return nil
}
