package management

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

// GetProviderStatistics returns recent usage aggregates without consuming usage records.
func (h *Handler) GetProviderStatistics(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	options, errOptions := parseProviderStatisticsOptions(c)
	if errOptions != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errOptions.Error()})
		return
	}

	store := h.usageStats
	if store == nil {
		store = usagestats.DefaultStore()
	}
	c.JSON(http.StatusOK, store.Report(options, time.Now()))
}

func parseProviderStatisticsOptions(c *gin.Context) (usagestats.QueryOptions, error) {
	if c == nil {
		return usagestats.QueryOptions{}, errors.New("request unavailable")
	}

	days, errDays := parseBoundedPositiveInt(c.Query("days"), usagestats.DefaultDays, usagestats.MaxDays, "days")
	if errDays != nil {
		return usagestats.QueryOptions{}, errDays
	}
	modelLimit, errModelLimit := parseBoundedPositiveInt(c.Query("model_limit"), usagestats.DefaultModelLimit, usagestats.MaxModelLimit, "model_limit")
	if errModelLimit != nil {
		return usagestats.QueryOptions{}, errModelLimit
	}
	recentLimit, errRecentLimit := parseBoundedPositiveInt(c.Query("recent_limit"), usagestats.DefaultRecentLimit, usagestats.MaxRecentLimit, "recent_limit")
	if errRecentLimit != nil {
		return usagestats.QueryOptions{}, errRecentLimit
	}

	return usagestats.QueryOptions{
		Days:        days,
		Provider:    strings.TrimSpace(c.Query("provider")),
		ModelLimit:  modelLimit,
		RecentLimit: recentLimit,
	}, nil
}

func parseBoundedPositiveInt(value string, defaultValue, maxValue int, name string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}

	parsed, errParse := strconv.Atoi(value)
	if errParse != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	if parsed > maxValue {
		return 0, fmt.Errorf("%s must not exceed %d", name, maxValue)
	}
	return parsed, nil
}
