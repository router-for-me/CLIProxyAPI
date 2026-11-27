//go:build proxdemo

package engine

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/panel"
)

// activity weights an hour of day: busy during the day, quiet at night.
func activity(t time.Time) float64 {
	h := t.Local().Hour()
	if h >= 9 && h <= 23 {
		return 1
	}
	return 0.12
}

// cumulative integrates activity over [from, to) in 15-minute steps.
func cumulative(from, to time.Time) float64 {
	var sum float64
	for t := from; t.Before(to); t = t.Add(15 * time.Minute) {
		sum += activity(t)
	}
	return sum
}

// seedHistory writes four days of weekly observations (with the previous
// window's reset when it falls in range) and a day of five-hour observations.
func seedHistory(e *Engine) {
	now := time.Now()
	step := 15 * time.Minute
	start := now.Add(-4 * 24 * time.Hour).Truncate(step)
	for i, a := range demoAccounts {
		// The engine may already have polled; telemetry only takes samples
		// newer than the last one, so start each account from scratch.
		e.tel.ForgetAccount(a.id)
		key := "weekly"
		cur := now.Add(a.weekReset)
		prev := cur.Add(-7 * 24 * time.Hour)
		total := cumulative(prev, now)
		prevEnd := 55 + float64((i*17)%40)
		for t := start; t.Before(now); t = t.Add(step) {
			if t.Before(prev) {
				used := math.Max(5, prevEnd-cumulative(t, prev)*0.18)
				r := prev
				e.tel.Quota(t, a.id, key, used, &r)
				continue
			}
			used := a.weekly
			if total > 0 {
				used = a.weekly * cumulative(prev, t) / total
			}
			r := cur
			e.tel.Quota(t, a.id, key, used, &r)
		}
		if a.provider != "claude" {
			continue
		}
		// Five-hour windows over the last day, each filled during active hours.
		if a.fiveReset == 0 {
			continue
		}
		fr := now.Add(a.fiveReset)
		for t := now.Add(-24 * time.Hour).Truncate(step); t.Before(now); t = t.Add(step) {
			k := math.Ceil(fr.Sub(t).Hours() / 5)
			reset := fr.Add(-time.Duration(k-1) * 5 * time.Hour)
			began := reset.Add(-5 * time.Hour)
			frac := t.Sub(began).Hours() / 5
			peak := 30 + float64((i*13)%60)
			if reset.Equal(fr) {
				peak = a.five / math.Max(0.05, now.Sub(began).Hours()/5)
			}
			e.tel.Quota(t, a.id, "five_hour", math.Min(100, peak*frac*activity(t)), &reset)
		}
	}
}

// mountDemoPanel serves the embedded panel and stubs the Management Center
// endpoints it needs, with provider usage answers built from demoAccounts.
func mountDemoPanel(r *gin.Engine) {
	r.GET("/management.html", func(c *gin.Context) {
		if !panel.Serve(c) {
			c.String(http.StatusNotFound, "panel not built: cd web/panel && bun run build:prox")
		}
	})
	r.GET("/", func(c *gin.Context) { c.Redirect(http.StatusFound, "/management.html") })
	r.Any("/v8/management/*path", func(c *gin.Context) {
		c.Header("X-CPA-Version", "prox-demo")
		path := c.Param("path")
		switch {
		case path == "/credentials" && c.Request.Method == http.MethodGet:
			c.JSON(http.StatusOK, gin.H{"files": demoCredentials()})
		case path == "/requests/api-call":
			body, _ := io.ReadAll(c.Request.Body)
			var req struct {
				AuthIndex string `json:"authIndex"`
				URL       string `json:"url"`
			}
			_ = json.Unmarshal(body, &req)
			status, payload := demoAPICall(req.AuthIndex, req.URL)
			c.JSON(http.StatusOK, gin.H{"status_code": status, "header": gin.H{}, "body": payload})
		case path == "/config":
			c.JSON(http.StatusOK, gin.H{"debug": false, "request-log": false, "plugins": gin.H{"configs": gin.H{"prox": gin.H{"enabled": true, "mode": "active"}}}})
		case c.Request.Method == http.MethodGet:
			c.JSON(http.StatusOK, gin.H{})
		default:
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
		}
	})
}

func demoCredentials() []gin.H {
	out := make([]gin.H, 0, len(demoAccounts))
	for i, a := range demoAccounts {
		f := gin.H{
			"id": a.id, "name": a.id, "type": a.provider, "provider": a.provider, "email": a.email,
			"auth_index": fmt.Sprint(i), "status": "active", "disabled": false, "plan_type": a.plan,
		}
		if a.provider == "codex" {
			f["chatgpt_account_id"] = fmt.Sprintf("acct-%d", i)
		}
		out = append(out, f)
	}
	return out
}

func demoAPICall(authIndex, url string) (int, any) {
	var i int
	if _, err := fmt.Sscan(authIndex, &i); err != nil || i < 0 || i >= len(demoAccounts) {
		return 404, gin.H{"error": "unknown auth"}
	}
	a := demoAccounts[i]
	now := time.Now()
	iso := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	switch {
	case strings.Contains(url, "api.anthropic.com/api/oauth/usage"):
		five := gin.H{"utilization": a.five, "resets_at": nil}
		if a.fiveReset > 0 {
			five["resets_at"] = iso(a.fiveReset)
		}
		return 200, gin.H{
			"five_hour":   five,
			"seven_day":   gin.H{"utilization": a.weekly, "resets_at": iso(a.weekReset)},
			"cedar_ember": gin.H{"eligible": true, "grants": []gin.H{}},
			"limits": []gin.H{{
				"kind": "weekly_scoped", "percent": a.fable, "resets_at": iso(a.weekReset), "is_active": true,
				"scope": gin.H{"model": gin.H{"display_name": "Fable"}},
			}},
		}
	case strings.Contains(url, "api.anthropic.com/api/oauth/profile"):
		tier := "default_claude_max_20x"
		if a.plan == "max_5x" {
			tier = "default_claude_max_5x"
		}
		return 200, gin.H{"account": gin.H{"has_claude_max": true}, "organization": gin.H{"rate_limit_tier": tier}}
	case strings.Contains(url, "wham/rate-limit-reset-credits"):
		credits := []gin.H{}
		for k := 0; k < a.resetCredits; k++ {
			credits = append(credits, gin.H{
				"id": fmt.Sprintf("rc-%d-%d", i, k), "reset_type": "codex_rate_limits", "status": "available",
				"granted_at": iso(-time.Duration(10+k) * 24 * time.Hour), "expires_at": iso(time.Duration(8+14*k) * 24 * time.Hour),
			})
		}
		return 200, gin.H{"available_count": a.resetCredits, "credits": credits}
	case strings.Contains(url, "wham/usage"):
		return 200, gin.H{
			"plan_type": a.plan,
			"rate_limit": gin.H{
				"secondary_window": gin.H{"used_percent": a.weekly, "limit_window_seconds": 604800, "reset_after_seconds": int(a.weekReset.Seconds())},
			},
		}
	case strings.Contains(url, "backend-api/subscriptions"):
		return 200, gin.H{"active_until": iso(time.Duration(5+i*9) * 24 * time.Hour)}
	case strings.Contains(url, "/coding/v1/usages"):
		return 200, gin.H{
			"usage":  gin.H{"used": int(a.weekly * 10), "limit": 1000, "reset_in": int(a.weekReset.Seconds())},
			"limits": []gin.H{{"window": gin.H{"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"}, "detail": gin.H{"limit": 200, "used": int(a.five * 2), "reset_in": int(a.fiveReset.Seconds())}}},
		}
	case strings.Contains(url, "billing"):
		return 200, gin.H{"config": gin.H{"creditUsagePercent": a.weekly, "currentPeriod": gin.H{"type": "WEEKLY", "end": iso(a.weekReset)}}}
	}
	return 404, gin.H{"error": gin.H{"message": "not available in the demo"}}
}
