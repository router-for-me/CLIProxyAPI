package management

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const quotaHistoryDefaultSpan = 14 * 24 * time.Hour

// quotaHistorySource is swapped by tests.
var quotaHistorySource = coreauth.ActiveQuotaHistory

type quotaHistoryResponse struct {
	Enabled        bool                     `json:"enabled"`
	Now            string                   `json:"now"`
	Since          string                   `json:"since"`
	RetentionHours int                      `json:"retention_hours"`
	Credentials    []quotaHistoryCredential `json:"credentials"`
}

type quotaHistoryCredential struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	AuthIndex string               `json:"auth_index"`
	Provider  string               `json:"provider"`
	Windows   []quotaHistoryWindow `json:"windows"`
}

type quotaHistoryWindow struct {
	Kind          string `json:"kind"`
	Scope         string `json:"scope"`
	PeriodSeconds int64  `json:"period_seconds"`
	// Points are [unix seconds, used 0..1, reset unix seconds or 0].
	Points [][3]float64 `json:"points"`
}

// GetQuotaHistory returns the recorded quota readings of the credentials the
// manager knows. It answers 200 with enabled:false when nothing is recording.
func (h *Handler) GetQuotaHistory(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	store := quotaHistorySource()
	if h.authManager == nil || h.authManager.HomeEnabled() || store == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "now": now.Format(time.RFC3339), "credentials": []quotaHistoryCredential{}})
		return
	}
	since, errSince := parseQuotaHistorySince(c.Query("since"), now)
	if errSince != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errSince.Error()})
		return
	}
	provider := strings.TrimSpace(c.Query("provider"))
	credentials := []quotaHistoryCredential{}
	for _, auth := range h.authManager.List() {
		if provider != "" && !strings.EqualFold(strings.TrimSpace(auth.Provider), provider) {
			continue
		}
		series := store.Series(auth.ID, since)
		if len(series) == 0 {
			continue
		}
		credentials = append(credentials, quotaHistoryCredential{
			ID:        auth.ID,
			Name:      authFileListName(auth),
			AuthIndex: auth.EnsureIndex(),
			Provider:  auth.Provider,
			Windows:   quotaHistoryWindows(series),
		})
	}
	sort.Slice(credentials, func(i, j int) bool {
		left, right := strings.ToLower(credentials[i].Name), strings.ToLower(credentials[j].Name)
		if left != right {
			return left < right
		}
		return credentials[i].ID < credentials[j].ID
	})
	c.JSON(http.StatusOK, quotaHistoryResponse{
		Enabled:        true,
		Now:            now.Format(time.RFC3339),
		Since:          since.Format(time.RFC3339),
		RetentionHours: int(coreauth.QuotaHistoryRetention / time.Hour),
		Credentials:    credentials,
	})
}

// parseQuotaHistorySince reads unix seconds or RFC3339, defaults to
// now-quotaHistoryDefaultSpan and clamps the result to the retention window.
func parseQuotaHistorySince(raw string, now time.Time) (time.Time, error) {
	since := now.Add(-quotaHistoryDefaultSpan)
	if raw = strings.TrimSpace(raw); raw != "" {
		if seconds, errUnix := strconv.ParseInt(raw, 10, 64); errUnix == nil {
			since = time.Unix(seconds, 0)
		} else if parsed, errRFC := time.Parse(time.RFC3339, raw); errRFC == nil {
			since = parsed
		} else {
			return time.Time{}, errors.New("invalid since")
		}
	}
	if earliest := now.Add(-coreauth.QuotaHistoryRetention); since.Before(earliest) {
		since = earliest
	}
	if since.After(now) {
		since = now
	}
	return since.UTC().Truncate(time.Second), nil
}

func quotaHistoryWindows(series []coreauth.QuotaHistorySeries) []quotaHistoryWindow {
	windows := make([]quotaHistoryWindow, 0, len(series))
	for _, s := range series {
		points := make([][3]float64, 0, len(s.Points))
		for _, p := range s.Points {
			var reset float64
			if !p.ResetAt.IsZero() {
				reset = float64(p.ResetAt.Unix())
			}
			points = append(points, [3]float64{float64(p.At.Unix()), p.Used, reset})
		}
		windows = append(windows, quotaHistoryWindow{
			Kind:          s.Kind,
			Scope:         s.Scope,
			PeriodSeconds: int64(s.Period / time.Second),
			Points:        points,
		})
	}
	return windows
}
