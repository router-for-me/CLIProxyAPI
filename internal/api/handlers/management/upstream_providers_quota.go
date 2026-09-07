package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// openCodeQuotaFetchTimeout bounds the upstream quota fetch. Registered in
// AGENTS.md as an intentional timeout exception (management probe path).
// A var (not const) so tests can shrink it.
var openCodeQuotaFetchTimeout = 8 * time.Second

// defaultOpenCodeQuotaURL is the expected OpenCode Zen Go quota endpoint.
// Per OmniRoute's own docs the public quota API is not live yet (upstream
// issues #16017/#18648/#31084) and this URL returns 404 today; the probe
// fails open with a clear message until OpenCode ships it. Operators can
// override via extra_config["quota_url"].
const defaultOpenCodeQuotaURL = "https://opencode.ai/zen/go/v1/quota"

// openCodeQuotaRawLimit caps the echoed raw body so a chatty upstream can
// never bloat the management response.
const openCodeQuotaRawLimit = 8 * 1024

// upstreamProviderQuotaRequest is the JSON body for POST
// .../quota. EntryID is required — quota is always checked per key entry.
type upstreamProviderQuotaRequest struct {
	EntryID *int64 `json:"entry_id"`
}

// upstreamProviderQuotaWin is one usage window reported by the upstream
// quota endpoint. Percent is clamped to [0,100]; -1 means the upstream did
// not report a usable limit.
type upstreamProviderQuotaWin struct {
	Key     string  `json:"key"`
	Used    float64 `json:"used"`
	Limit   float64 `json:"limit"`
	Percent float64 `json:"percent_used"`
	ResetAt string  `json:"reset_at,omitempty"`
}

// upstreamProviderQuotaResponse reports one manual quota probe for a single
// api_key_entry. Fail-open: any failure yields ok=false + error and never
// mutates entry state or cooldowns.
type upstreamProviderQuotaResponse struct {
	OK      bool                       `json:"ok"`
	EntryID *int64                     `json:"entry_id,omitempty"`
	Windows []upstreamProviderQuotaWin `json:"windows,omitempty"`
	Raw     string                     `json:"raw,omitempty"`
	Error   string                     `json:"error,omitempty"`
}

// openCodeQuotaPayload tolerates the wrapper shapes OmniRoute's fetcher
// accepts: top-level "quota" | "data" | "usage", or the windows directly on
// the root object.
func openCodeQuotaPayload(body []byte) map[string]any {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	for _, wrapper := range []string{"quota", "data", "usage"} {
		if nested, ok := root[wrapper].(map[string]any); ok {
			return nested
		}
	}
	return root
}

// openCodeQuotaWindowKeys maps canonical window names to the aliases the
// upstream has been observed to use. "rolling" (5-hour window) is the
// official live shape (2026-09); the used/limit aliases predate it and are
// kept for any non-official quota_url override that still speaks them.
var openCodeQuotaWindowKeys = []struct {
	canonical string
	aliases   []string
}{
	{"rolling", []string{"rolling", "5h", "hourly", "short"}},
	{"weekly", []string{"weekly", "week", "wk"}},
	{"monthly", []string{"monthly", "month", "mo"}},
}

// openCodeQuotaWindow extracts one window object by alias chain. Missing
// windows are simply absent from the response.
func openCodeQuotaWindow(payload map[string]any, aliases []string) map[string]any {
	if payload == nil {
		return nil
	}
	for _, a := range aliases {
		if w, ok := payload[a].(map[string]any); ok {
			return w
		}
	}
	return nil
}

// openCodeQuotaNumber coerces a decoded JSON number to float64.
func openCodeQuotaNumber(v any) (float64, bool) {
	if n, ok := v.(float64); ok {
		return n, true
	}
	return 0, false
}

// openCodeQuotaResetAt normalizes the window reset timestamp to RFC3339.
// Accepted inputs, in order: resetsAt / reset_at as an RFC3339/ISO string
// (the official live shape), epoch seconds (<1e12) or milliseconds
// (>=1e12), and reset_after_seconds relative to now. Empty when nothing
// parseable exists.
func openCodeQuotaResetAt(w map[string]any, now time.Time) string {
	for _, key := range []string{"resetsAt", "reset_at"} {
		switch v := w[key].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					return t.UTC().Format(time.RFC3339)
				}
				// Tolerate fractional seconds (…T21:01:19.442Z), which
				// time.RFC3339 alone rejects.
				if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
					return t.UTC().Format(time.RFC3339)
				}
			}
		case float64:
			if v > 0 {
				if v < 1e12 {
					return time.Unix(int64(v), 0).UTC().Format(time.RFC3339)
				}
				return time.UnixMilli(int64(v)).UTC().Format(time.RFC3339)
			}
		}
	}
	if v, ok := w["reset_after_seconds"].(float64); ok && v > 0 {
		return now.Add(time.Duration(v * float64(time.Second))).UTC().Format(time.RFC3339)
	}
	return ""
}

// openCodeQuotaWinFields extracts used/limit/percent from one window
// object, tolerating both observed shapes:
//   - official live shape (2026-09): {"status":"ok","percent":85,
//     "resetsAt":"2026-09-25T06:01:44.442Z"} — used/limit stay 0 and the
//     upstream-reported percent is authoritative;
//   - spec shape: {"used":700,"limit":1000,"reset_at":1757500000} —
//     percent derives from used/limit.
func openCodeQuotaWinFields(w map[string]any) (used, limit, percent float64, percentKnown bool) {
	if v, ok := openCodeQuotaNumber(w["percent"]); ok {
		// The upstream percent is clamped to [0,100]; out-of-range values
		// (shouldn't happen) clamp too rather than rendering a broken bar.
		p := v
		if p < 0 {
			p = 0
		}
		if p > 100 {
			p = 100
		}
		return 0, 0, p, true
	}
	u, _ := openCodeQuotaNumber(w["used"])
	l, _ := openCodeQuotaNumber(w["limit"])
	return u, l, 0, false
}

// UpstreamProviderQuota handles POST /v0/management/upstream-providers/:id/quota.
// It fetches the OpenCode quota endpoint with the pinned entry's key and
// reports the usage windows. Fail-open: 404/405 (endpoint not live yet),
// 401/403 (key rejected), timeouts, and malformed responses all yield
// ok=false with a descriptive error; nothing is cached or persisted and no
// cooldown state is touched.
func (h *Handler) UpstreamProviderQuota(c *gin.Context) {
	id, errID := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if errID != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider id"})
		return
	}
	var body upstreamProviderQuotaRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil || body.EntryID == nil || *body.EntryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing or invalid entry_id — quota is checked per api key entry"})
		return
	}
	src, ok := h.upstreamProvidersStore(c)
	if !ok {
		return // 503 already written
	}
	row, errGet := src.Get(c.Request.Context(), id)
	if errGet != nil || row == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	var key string
	for _, e := range row.APIKeyEntries {
		if e.ID == *body.EntryID {
			key = e.APIKey
			break
		}
	}
	if key == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "entry not found on this provider row, or its key is empty"})
		return
	}
	quotaURL := defaultOpenCodeQuotaURL
	if v, ok := row.ExtraConfig["quota_url"].(string); ok && strings.TrimSpace(v) != "" {
		quotaURL = strings.TrimSpace(v)
	}

	out := upstreamProviderQuotaResponse{EntryID: body.EntryID}
	fetchCtx, cancel := context.WithTimeout(c.Request.Context(), openCodeQuotaFetchTimeout)
	defer cancel()
	req, errReq := http.NewRequestWithContext(fetchCtx, http.MethodGet, quotaURL, nil)
	if errReq != nil {
		out.Error = fmt.Sprintf("build quota request: %v", errReq)
		c.JSON(http.StatusOK, out)
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	// The key is never logged; only the URL and status surface in errors.
	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		out.Error = fmt.Sprintf("quota fetch failed: %v", errDo)
		c.JSON(http.StatusOK, out)
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		out.Error = fmt.Sprintf("read quota response: %v", errRead)
		c.JSON(http.StatusOK, out)
		return
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		out.Error = fmt.Sprintf("quota endpoint not available upstream (HTTP %d) — set extra_config.quota_url when OpenCode publishes an official endpoint", resp.StatusCode)
		out.Raw = truncateQuotaRaw(raw)
		c.JSON(http.StatusOK, out)
		return
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		out.Error = fmt.Sprintf("upstream rejected the API key (HTTP %d)", resp.StatusCode)
		c.JSON(http.StatusOK, out)
		return
	case resp.StatusCode != http.StatusOK:
		out.Error = fmt.Sprintf("quota endpoint returned HTTP %d", resp.StatusCode)
		out.Raw = truncateQuotaRaw(raw)
		c.JSON(http.StatusOK, out)
		return
	}

	payload := openCodeQuotaPayload(raw)
	if payload == nil {
		out.Error = "quota endpoint returned malformed JSON"
		out.Raw = truncateQuotaRaw(raw)
		c.JSON(http.StatusOK, out)
		return
	}
	now := time.Now()
	for _, wk := range openCodeQuotaWindowKeys {
		w := openCodeQuotaWindow(payload, wk.aliases)
		if w == nil {
			continue
		}
		win := upstreamProviderQuotaWin{Key: wk.canonical}
		used, limit, percent, percentKnown := openCodeQuotaWinFields(w)
		win.Used = used
		win.Limit = limit
		switch {
		case percentKnown:
			// Official shape: the upstream reports usage as a percent
			// directly; used/limit stay 0 (the dashboard renders the bar).
			win.Percent = percent
		case limit > 0:
			pct := used / limit * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			win.Percent = pct
		default:
			win.Percent = -1
		}
		win.ResetAt = openCodeQuotaResetAt(w, now)
		out.Windows = append(out.Windows, win)
	}
	if len(out.Windows) == 0 {
		out.Error = "quota response contained no recognizable usage windows"
		out.Raw = truncateQuotaRaw(raw)
		c.JSON(http.StatusOK, out)
		return
	}
	out.OK = true
	out.Raw = truncateQuotaRaw(raw)
	c.JSON(http.StatusOK, out)
}

func truncateQuotaRaw(raw []byte) string {
	if len(raw) > openCodeQuotaRawLimit {
		return string(raw[:openCodeQuotaRawLimit]) + "…"
	}
	return string(raw)
}
