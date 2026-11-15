package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
)

const (
	usageStatsDefaultRange    = 30 * 24 * time.Hour
	usageStatsMaxOffset       = 14 * time.Hour
	usageStatsFilterPrefix    = "filter."
	usageStatsSettingsMaxSize = 1 << 20
)

// usageStatsStore returns the open usage store, or writes a 409 and returns nil
// when usage statistics are not being recorded.
func usageStatsStore(c *gin.Context) *usagestats.Store {
	store := usagestats.Current()
	if store == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "usage_statistics_disabled",
			"message": "usage statistics are not being recorded; set usage-statistics-enabled to true",
		})
	}
	return store
}

// parseUsageStatsQuery reads the time range, UTC offset, filters, and limit
// shared by the usage statistics endpoints. Times are Unix milliseconds.
func parseUsageStatsQuery(c *gin.Context, now time.Time) (usagestats.Query, error) {
	query := usagestats.Query{To: now, Filters: map[string]string{}}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		ms, errParse := strconv.ParseInt(raw, 10, 64)
		if errParse != nil {
			return query, errors.New("to must be a Unix timestamp in milliseconds")
		}
		query.To = time.UnixMilli(ms)
	}
	query.From = query.To.Add(-usageStatsDefaultRange)
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		ms, errParse := strconv.ParseInt(raw, 10, 64)
		if errParse != nil {
			return query, errors.New("from must be a Unix timestamp in milliseconds")
		}
		query.From = time.UnixMilli(ms)
	}
	if raw := strings.TrimSpace(c.Query("utc_offset_minutes")); raw != "" {
		minutes, errParse := strconv.Atoi(raw)
		offset := time.Duration(minutes) * time.Minute
		if errParse != nil || offset < -usageStatsMaxOffset || offset > usageStatsMaxOffset {
			return query, errors.New("utc_offset_minutes must be an integer between -840 and 840")
		}
		query.UTCOffset = offset
	}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		limit, errParse := strconv.Atoi(raw)
		if errParse != nil || limit <= 0 {
			return query, errors.New("limit must be a positive integer")
		}
		query.Limit = limit
	}
	for key, values := range c.Request.URL.Query() {
		if name, ok := strings.CutPrefix(key, usageStatsFilterPrefix); ok && len(values) > 0 {
			query.Filters[name] = values[0]
		}
	}
	return query, nil
}

func splitUsageStatsList(raw string) []string {
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// GetUsageStats returns token, request, and API-equivalent cost totals grouped
// by the requested dimensions (group_by=day,model).
func (h *Handler) GetUsageStats(c *gin.Context) {
	store := usageStatsStore(c)
	if store == nil {
		return
	}
	query, errQuery := parseUsageStatsQuery(c, time.Now())
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errQuery.Error()})
		return
	}
	query.GroupBy = splitUsageStatsList(c.Query("group_by"))

	aggregate, errAggregate := store.Aggregate(c.Request.Context(), query)
	if errAggregate != nil {
		writeUsageStatsError(c, errAggregate)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"from":            query.From.UnixMilli(),
		"to":              query.To.UnixMilli(),
		"groups":          aggregate.Groups,
		"truncated":       aggregate.Truncated,
		"unpriced_models": aggregate.UnpricedModels,
	})
}

// GetUsageSpeed returns latency percentiles and output throughput grouped by
// one dimension (group_by=model by default).
func (h *Handler) GetUsageSpeed(c *gin.Context) {
	store := usageStatsStore(c)
	if store == nil {
		return
	}
	query, errQuery := parseUsageStatsQuery(c, time.Now())
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errQuery.Error()})
		return
	}
	dimension := strings.TrimSpace(c.Query("group_by"))
	if dimension == "" {
		dimension = "model"
	}
	groups, errSpeed := store.Speed(c.Request.Context(), query, dimension)
	if errSpeed != nil {
		writeUsageStatsError(c, errSpeed)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"from":   query.From.UnixMilli(),
		"to":     query.To.UnixMilli(),
		"groups": groups,
	})
}

// GetUsageQuotaSamples returns the recorded quota history of each account
// window in the range.
func (h *Handler) GetUsageQuotaSamples(c *gin.Context) {
	store := usageStatsStore(c)
	if store == nil {
		return
	}
	query, errQuery := parseUsageStatsQuery(c, time.Now())
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errQuery.Error()})
		return
	}
	history, errHistory := store.QuotaSamples(c.Request.Context(), query)
	if errHistory != nil {
		writeUsageStatsError(c, errHistory)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"from":      query.From.UnixMilli(),
		"to":        query.To.UnixMilli(),
		"series":    history.Series,
		"bucket_ms": history.BucketMs,
	})
}

// GetUsageLimitValue returns, for each account quota window, what one percent
// of the limit bought at API list prices.
func (h *Handler) GetUsageLimitValue(c *gin.Context) {
	store := usageStatsStore(c)
	if store == nil {
		return
	}
	query, errQuery := parseUsageStatsQuery(c, time.Now())
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errQuery.Error()})
		return
	}
	value, errValue := store.LimitValue(c.Request.Context(), query)
	if errValue != nil {
		writeUsageStatsError(c, errValue)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"from":            query.From.UnixMilli(),
		"to":              query.To.UnixMilli(),
		"series":          value.Series,
		"unpriced_models": value.UnpricedModels,
	})
}

// GetUsageSettings returns the operator settings together with the built-in
// price table they override.
func (h *Handler) GetUsageSettings(c *gin.Context) {
	store := usageStatsStore(c)
	if store == nil {
		return
	}
	settings, errSettings := store.Settings(c.Request.Context())
	if errSettings != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errSettings.Error()})
		return
	}
	defaults, errDefaults := usagestats.DefaultPrices()
	if errDefaults != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errDefaults.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": settings, "default_prices": defaults})
}

// PutUsageSettings replaces the operator settings.
func (h *Handler) PutUsageSettings(c *gin.Context) {
	store := usageStatsStore(c)
	if store == nil {
		return
	}
	var settings usagestats.Settings
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, usageStatsSettingsMaxSize))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&settings); errDecode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid settings: %v", errDecode)})
		return
	}
	if errValidate := settings.Validate(); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}
	if errSave := store.SaveSettings(c.Request.Context(), settings); errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errSave.Error()})
		return
	}
	saved, errSettings := store.Settings(c.Request.Context())
	if errSettings != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errSettings.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": saved})
}

// writeUsageStatsError maps a query error to a status code: malformed queries
// are the caller's fault, anything else is a storage failure.
func writeUsageStatsError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, usagestats.ErrInvalidQuery) {
		status = http.StatusBadRequest
	}
	c.JSON(status, gin.H{"error": err.Error()})
}
