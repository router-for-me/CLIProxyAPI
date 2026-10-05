package management

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ProviderBudgetRow is one budget row per upstream provider API key entry that
// has a budget_usd set. The dashboard groups by provider then lists each entry's
// budget cap, actual spend, and remaining balance.
type ProviderBudgetRow struct {
	ProviderID        int64   `json:"provider_id"`
	ProviderName      string  `json:"provider_name"`
	ProviderType      string  `json:"provider_type"`
	EntryID           int64   `json:"entry_id"`
	EntryName         string  `json:"entry_name"`
	EntryAPIKeyPrefix string  `json:"entry_api_key_prefix"`
	BudgetUSD         float64 `json:"budget_usd"`
	SpentUSD          float64 `json:"spent_usd"`
	RemainingUSD      float64 `json:"remaining_usd"`
	UsagePct          float64 `json:"usage_pct"`
}

// GetProviderBudget returns every API key entry with a budget_usd set, joined
// with actual spend from usage_events over the requested period.
func (h *Handler) GetProviderBudget(c *gin.Context) {
	pg := h.pgControl
	if pg == nil || pg.DB() == nil {
		h.pgNotConfigured(c)
		return
	}

	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	to := now

	if f := c.Query("from"); f != "" {
		if t, err := time.Parse(time.RFC3339, f); err == nil {
			from = t
		}
	}
	if t := c.Query("to"); t != "" {
		if t2, err := time.Parse(time.RFC3339, t); err == nil {
			to = t2
		}
	}

	upTable := pg.UpstreamProvidersTable()
	entriesTable := pg.UpstreamProviderEntriesTable()
	usageTable := pg.UsageEventsTable()

	query := fmt.Sprintf(`
		SELECT
			p.id,
			COALESCE(p.name, p.provider_type),
			p.provider_type,
			e.id,
			COALESCE(e.name, ''),
			LEFT(e.api_key, 8),
			e.budget_usd,
			COALESCE(SUM(u.cost_usd), 0)
		FROM %s p
		JOIN %s e ON e.provider_id = p.id
		LEFT JOIN %s u ON substring(u.entry_provider_key from ':key-([0-9]+)$')::bigint = e.id
			AND u.requested_at >= $1
			AND u.requested_at <= $2
			AND u.failed = FALSE
		WHERE e.budget_usd IS NOT NULL
		GROUP BY p.id, p.name, p.provider_type, e.id, e.name, e.api_key, e.budget_usd
		ORDER BY COALESCE(p.name, p.provider_type), e.sort_order, e.id
	`, upTable, entriesTable, usageTable)

	rows, err := pg.DB().QueryContext(c.Request.Context(), query, from, to)
	if err != nil {
		_ = c.Error(fmt.Errorf("provider budget query: %w", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type":    "internal_error",
			"message": "failed to query provider budget",
		}})
		return
	}
	defer func() { _ = rows.Close() }()

	var out []ProviderBudgetRow
	for rows.Next() {
		var r ProviderBudgetRow
		if err := rows.Scan(&r.ProviderID, &r.ProviderName, &r.ProviderType,
			&r.EntryID, &r.EntryName, &r.EntryAPIKeyPrefix,
			&r.BudgetUSD, &r.SpentUSD); err != nil {
			_ = c.Error(fmt.Errorf("scan provider budget row: %w", err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": "failed to parse provider budget"}})
			return
		}
		remaining := r.BudgetUSD - r.SpentUSD
		if remaining < 0 {
			remaining = 0
		}
		usagePct := 0.0
		if r.BudgetUSD > 0 {
			usagePct = (r.SpentUSD / r.BudgetUSD) * 100
		}
		r.RemainingUSD = remaining
		r.UsagePct = usagePct
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		_ = c.Error(fmt.Errorf("iterate provider budget rows: %w", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": "failed to read provider budget"}})
		return
	}

	if out == nil {
		out = []ProviderBudgetRow{}
	}

	c.JSON(http.StatusOK, gin.H{
		"rows": out,
		"from": from,
		"to":   to,
	})
}
