package management

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type credentialQuotaRequest struct {
	AuthIndexSnake  *string `json:"auth_index"`
	AuthIndexCamel  *string `json:"authIndex"`
	AuthIndexPascal *string `json:"AuthIndex"`
	PluginID        string  `json:"plugin_id"`
	Provider        string  `json:"provider"`
}

func (r credentialQuotaRequest) resolveAuthIndex() string {
	if r.AuthIndexSnake != nil && strings.TrimSpace(*r.AuthIndexSnake) != "" {
		return strings.TrimSpace(*r.AuthIndexSnake)
	}
	if r.AuthIndexCamel != nil && strings.TrimSpace(*r.AuthIndexCamel) != "" {
		return strings.TrimSpace(*r.AuthIndexCamel)
	}
	if r.AuthIndexPascal != nil && strings.TrimSpace(*r.AuthIndexPascal) != "" {
		return strings.TrimSpace(*r.AuthIndexPascal)
	}
	return ""
}

// GetQuotaProviders returns the list of registered quota providers.
func (h *Handler) GetQuotaProviders(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusOK, gin.H{"providers": []any{}})
		return
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if host == nil {
		c.JSON(http.StatusOK, gin.H{"providers": []any{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": host.QuotaProviders(c.Request.Context())})
}

// FetchCredentialQuota retrieves normalized quota for a credential via its quota provider or declarative probe.
func (h *Handler) FetchCredentialQuota(c *gin.Context) {
	var body credentialQuotaRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	authIndex := body.resolveAuthIndex()
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}

	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()

	pluginID := strings.TrimSpace(body.PluginID)
	provider := strings.TrimSpace(body.Provider)
	if provider == "" {
		provider = auth.Provider
	}

	if host != nil {
		req := pluginapi.QuotaFetchRequest{
			AuthIndex:  auth.Index,
			AuthID:     auth.ID,
			Provider:   provider,
			Metadata:   auth.Metadata,
			Attributes: auth.Attributes,
		}
		var quotaResp pluginapi.QuotaFetchResponse
		var handled bool
		var errFetch error
		if pluginID != "" {
			quotaResp, handled, errFetch = host.FetchQuotaByPlugin(c.Request.Context(), pluginID, req)
		} else {
			quotaResp, handled, errFetch = host.FetchQuota(c.Request.Context(), req)
		}
		if handled {
			if errFetch != nil {
				log.WithError(errFetch).Warnf("failed to fetch quota for credential %s", auth.Index)
				c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch quota: %v", errFetch)})
				return
			}
			c.JSON(http.StatusOK, quotaResp)
			return
		}
	}

	// Fallback to declarative quota probe if configured in metadata
	if auth.Metadata != nil {
		if rawProbe, okProbe := auth.Metadata["quota_probe"]; okProbe && rawProbe != nil {
			if probeMap, okMap := rawProbe.(map[string]any); okMap {
				quotaResp, handledProbe, errProbe := h.executeQuotaProbe(c, auth, probeMap)
				if handledProbe {
					if errProbe != nil {
						c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("quota probe failed: %v", errProbe)})
						return
					}
					c.JSON(http.StatusOK, quotaResp)
					return
				}
			}
		}
	}

	c.JSON(http.StatusNotImplemented, gin.H{"error": "no quota provider available for credential"})
}

// ResetCredentialQuota resets quota or usage for a credential.
func (h *Handler) ResetCredentialQuota(c *gin.Context) {
	var body credentialQuotaRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	authIndex := body.resolveAuthIndex()
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}

	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()

	if host == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "plugin host unavailable"})
		return
	}

	pluginID := strings.TrimSpace(body.PluginID)
	provider := strings.TrimSpace(body.Provider)
	if provider == "" {
		provider = auth.Provider
	}

	req := pluginapi.QuotaResetRequest{
		AuthIndex:  auth.Index,
		AuthID:     auth.ID,
		Provider:   provider,
		Metadata:   auth.Metadata,
		Attributes: auth.Attributes,
	}

	var resetResp pluginapi.QuotaResetResponse
	var handled bool
	var errReset error

	if pluginID != "" {
		if !host.HasQuotaProviderForPlugin(pluginID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "quota provider not found for plugin"})
			return
		}
		resetResp, handled, errReset = host.ResetQuotaByPlugin(c.Request.Context(), pluginID, req)
		if !handled {
			c.JSON(http.StatusNotFound, gin.H{"error": "quota provider not found for plugin"})
			return
		}
	} else {
		if !host.HasQuotaProviderContext(c.Request.Context(), provider) {
			c.JSON(http.StatusNotImplemented, gin.H{"error": "no quota provider available for credential to reset"})
			return
		}
		resetResp, handled, errReset = host.ResetQuota(c.Request.Context(), req)
		if !handled {
			c.JSON(http.StatusBadGateway, gin.H{"error": "quota provider did not handle reset request"})
			return
		}
	}

	if errReset != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("plugin quota reset failed: %v", errReset)})
		return
	}
	if !resetResp.Success {
		msg := resetResp.Message
		if msg == "" {
			msg = "quota reset rejected by provider"
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": msg})
		return
	}

	if h.authManager != nil {
		updated, _, errResetCore := h.authManager.ResetQuota(c.Request.Context(), auth.ID)
		if errResetCore != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to reset routing quota: %v", errResetCore)})
			return
		}
		if updated != nil {
			updated.EnsureIndex()
		}
	}

	resp := gin.H{
		"status":     "ok",
		"auth_index": auth.Index,
	}
	if resetResp.Message != "" {
		resp["message"] = resetResp.Message
	}
	c.JSON(http.StatusOK, resp)
}

// GetPluginQuota handles GET /v0/management/plugins/:id/quota?auth_index=...
func (h *Handler) GetPluginQuota(c *gin.Context) {
	pluginID := strings.TrimSpace(c.Param("id"))
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("authIndex"))
	}
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	h.fetchQuotaForPlugin(c, pluginID, authIndex)
}

// FetchPluginQuota handles POST /v0/management/plugins/:id/quota
func (h *Handler) FetchPluginQuota(c *gin.Context) {
	pluginID := strings.TrimSpace(c.Param("id"))
	var body credentialQuotaRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	authIndex := body.resolveAuthIndex()
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	h.fetchQuotaForPlugin(c, pluginID, authIndex)
}

func (h *Handler) fetchQuotaForPlugin(c *gin.Context, pluginID, authIndex string) {
	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}

	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()

	if host == nil || !host.HasQuotaProviderForPlugin(pluginID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "quota provider not found for plugin"})
		return
	}

	quotaResp, handled, errFetch := host.FetchQuotaByPlugin(c.Request.Context(), pluginID, pluginapi.QuotaFetchRequest{
		AuthIndex:  auth.Index,
		AuthID:     auth.ID,
		Provider:   auth.Provider,
		Metadata:   auth.Metadata,
		Attributes: auth.Attributes,
	})
	if !handled {
		c.JSON(http.StatusNotFound, gin.H{"error": "quota provider not found for plugin"})
		return
	}
	if errFetch != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch quota: %v", errFetch)})
		return
	}
	c.JSON(http.StatusOK, quotaResp)
}

// ResetPluginQuota handles DELETE /v0/management/plugins/:id/quota and POST /v0/management/plugins/:id/quota/reset
func (h *Handler) ResetPluginQuota(c *gin.Context) {
	pluginID := strings.TrimSpace(c.Param("id"))
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("authIndex"))
	}
	if authIndex == "" {
		var body credentialQuotaRequest
		_ = c.ShouldBindJSON(&body)
		authIndex = body.resolveAuthIndex()
	}
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}

	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()

	if host == nil || !host.HasQuotaProviderForPlugin(pluginID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "quota provider not found for plugin"})
		return
	}

	resetResp, handled, errReset := host.ResetQuotaByPlugin(c.Request.Context(), pluginID, pluginapi.QuotaResetRequest{
		AuthIndex:  auth.Index,
		AuthID:     auth.ID,
		Provider:   auth.Provider,
		Metadata:   auth.Metadata,
		Attributes: auth.Attributes,
	})
	if !handled {
		c.JSON(http.StatusNotFound, gin.H{"error": "quota provider not found for plugin"})
		return
	}
	if errReset != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to reset quota: %v", errReset)})
		return
	}
	if !resetResp.Success {
		msg := resetResp.Message
		if msg == "" {
			msg = "quota reset rejected by plugin"
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": msg})
		return
	}

	if h.authManager != nil {
		updated, _, errResetCore := h.authManager.ResetQuota(c.Request.Context(), auth.ID)
		if errResetCore != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to reset routing quota: %v", errResetCore)})
			return
		}
		if updated != nil {
			updated.EnsureIndex()
		}
	}

	resp := gin.H{
		"status":     "ok",
		"auth_index": auth.Index,
	}
	if resetResp.Message != "" {
		resp["message"] = resetResp.Message
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) executeQuotaProbe(c *gin.Context, auth *coreauth.Auth, probe map[string]any) (pluginapi.QuotaFetchResponse, bool, error) {
	urlStr, _ := probe["url"].(string)
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}

	method, _ := probe["method"].(string)
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodGet
	}

	needsToken := strings.Contains(urlStr, "$TOKEN$")
	rawData, _ := probe["data"].(string)
	if strings.Contains(rawData, "$TOKEN$") {
		needsToken = true
	}
	headers, _ := probe["header"].(map[string]any)
	if headers == nil {
		headers, _ = probe["headers"].(map[string]any)
	}
	for _, v := range headers {
		if strVal, okVal := v.(string); okVal && strings.Contains(strVal, "$TOKEN$") {
			needsToken = true
			break
		}
	}

	var token string
	if needsToken {
		var errToken error
		token, errToken = h.resolveTokenForAuth(c.Request.Context(), auth, "")
		if errToken != nil {
			return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("probe authentication failed: %w", errToken)
		}
		if token == "" {
			return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("probe authentication token not found for credential")
		}
		urlStr = strings.ReplaceAll(urlStr, "$TOKEN$", token)
		rawData = strings.ReplaceAll(rawData, "$TOKEN$", token)
	}

	var reqBody io.Reader
	if rawData != "" {
		reqBody = strings.NewReader(rawData)
	}

	req, errReq := http.NewRequestWithContext(c.Request.Context(), method, urlStr, reqBody)
	if errReq != nil {
		return pluginapi.QuotaFetchResponse{}, false, fmt.Errorf("build probe request: %w", errReq)
	}

	for k, v := range headers {
		if strVal, okVal := v.(string); okVal {
			if needsToken {
				strVal = strings.ReplaceAll(strVal, "$TOKEN$", token)
			}
			req.Header.Set(k, strVal)
		}
	}

	client := &http.Client{
		Transport: h.apiCallTransport(auth, ""),
	}

	resp, errDo := client.Do(req)
	if errDo != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("probe request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("probe response body close error: %v", errClose)
		}
	}()

	respBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("read probe response: %w", errRead)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("probe returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	if !json.Valid(respBytes) {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("upstream probe response is not valid JSON")
	}

	var serverOffsetMs int64
	if dateHeader := resp.Header.Get("Date"); dateHeader != "" {
		if parsedDate, errDate := http.ParseTime(dateHeader); errDate == nil {
			serverOffsetMs = parsedDate.Sub(time.Now()).Milliseconds()
		}
	}

	if mappingRaw, okMapping := probe["mapping"].(map[string]any); okMapping {
		mappedResp, errMap := mapProbeResponse(respBytes, mappingRaw)
		if errMap != nil {
			return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("probe response mapping failed: %w", errMap)
		}
		if mappedResp.ServerTimeOffsetMs == 0 {
			mappedResp.ServerTimeOffsetMs = serverOffsetMs
		}
		return mappedResp, true, nil
	}

	var quotaResp pluginapi.QuotaFetchResponse
	if errJSON := json.Unmarshal(respBytes, &quotaResp); errJSON == nil {
		hasPlan := quotaResp.Subscription != nil && strings.TrimSpace(quotaResp.Subscription.Plan) != ""
		// Filter groups to retain only buckets with an actual valid numeric remaining fraction in raw JSON
		filteredGroups := make([]pluginapi.QuotaGroup, 0, len(quotaResp.Groups))
		groupsRes := gjson.GetBytes(respBytes, "groups")
		if groupsRes.IsArray() {
			for gIdx, grp := range groupsRes.Array() {
				if gIdx >= len(quotaResp.Groups) {
					break
				}
				bucketsRes := grp.Get("buckets")
				if !bucketsRes.IsArray() {
					continue
				}
				origGroup := quotaResp.Groups[gIdx]
				validBuckets := make([]pluginapi.QuotaBucket, 0, len(origGroup.Buckets))
				for bIdx, bkt := range bucketsRes.Array() {
					if bIdx >= len(origGroup.Buckets) {
						break
					}
					remFrac := bkt.Get("remainingFraction")
					if !remFrac.Exists() {
						remFrac = bkt.Get("remaining_fraction")
					}
					if fracVal, okNum := parseNumericFraction(remFrac); okNum {
						bucket := origGroup.Buckets[bIdx]
						bucket.RemainingFraction = fracVal
						validBuckets = append(validBuckets, bucket)
					}
				}
				if len(validBuckets) > 0 {
					origGroup.Buckets = validBuckets
					filteredGroups = append(filteredGroups, origGroup)
				}
			}
		}
		quotaResp.Groups = filteredGroups
		hasValidBuckets := len(filteredGroups) > 0
		if hasPlan || hasValidBuckets {
			if quotaResp.ServerTimeOffsetMs == 0 {
				quotaResp.ServerTimeOffsetMs = serverOffsetMs
			}
			return quotaResp, true, nil
		}
	}

	return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("upstream probe response does not match normalized quota shape or declared mapping")
}

func parseNumericFraction(res gjson.Result) (float64, bool) {
	if !res.Exists() {
		return 0, false
	}
	switch res.Type {
	case gjson.Number:
		val := res.Float()
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return 0, false
		}
		return val, true
	case gjson.String:
		s := strings.TrimSpace(res.String())
		if s == "" {
			return 0, false
		}
		val, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
			return 0, false
		}
		return val, true
	default:
		return 0, false
	}
}

func mapProbeResponse(respBytes []byte, mapping map[string]any) (pluginapi.QuotaFetchResponse, error) {
	out := pluginapi.QuotaFetchResponse{}
	if planPath, ok := mapping["plan"].(string); ok && planPath != "" {
		if res := gjson.GetBytes(respBytes, planPath); res.Exists() && strings.TrimSpace(res.String()) != "" {
			if out.Subscription == nil {
				out.Subscription = &pluginapi.QuotaSubscription{}
			}
			out.Subscription.Plan = res.String()
		}
	}
	if tierPath, ok := mapping["tier_name"].(string); ok && tierPath != "" {
		if res := gjson.GetBytes(respBytes, tierPath); res.Exists() && strings.TrimSpace(res.String()) != "" {
			if out.Subscription == nil {
				out.Subscription = &pluginapi.QuotaSubscription{}
			}
			out.Subscription.TierName = res.String()
		}
	} else if tierPath, ok := mapping["tierName"].(string); ok && tierPath != "" {
		if res := gjson.GetBytes(respBytes, tierPath); res.Exists() && strings.TrimSpace(res.String()) != "" {
			if out.Subscription == nil {
				out.Subscription = &pluginapi.QuotaSubscription{}
			}
			out.Subscription.TierName = res.String()
		}
	}
	if tierIDPath, ok := mapping["tier_id"].(string); ok && tierIDPath != "" {
		if res := gjson.GetBytes(respBytes, tierIDPath); res.Exists() && strings.TrimSpace(res.String()) != "" {
			if out.Subscription == nil {
				out.Subscription = &pluginapi.QuotaSubscription{}
			}
			out.Subscription.TierID = res.String()
		}
	} else if tierIDPath, ok := mapping["tierId"].(string); ok && tierIDPath != "" {
		if res := gjson.GetBytes(respBytes, tierIDPath); res.Exists() && strings.TrimSpace(res.String()) != "" {
			if out.Subscription == nil {
				out.Subscription = &pluginapi.QuotaSubscription{}
			}
			out.Subscription.TierID = res.String()
		}
	}

	rawGroups, okGroups := mapping["groups"].([]any)
	if okGroups {
		for _, rg := range rawGroups {
			gm, okGM := rg.(map[string]any)
			if !okGM {
				continue
			}
			group := pluginapi.QuotaGroup{}
			if namePath, okName := gm["display_name"].(string); okName {
				if res := gjson.GetBytes(respBytes, namePath); res.Exists() && strings.TrimSpace(res.String()) != "" {
					group.DisplayName = res.String()
				} else {
					group.DisplayName = namePath
				}
			} else if namePath, okName := gm["displayName"].(string); okName {
				if res := gjson.GetBytes(respBytes, namePath); res.Exists() && strings.TrimSpace(res.String()) != "" {
					group.DisplayName = res.String()
				} else {
					group.DisplayName = namePath
				}
			}

			if bucketsPath, okBP := gm["buckets_path"].(string); okBP && bucketsPath != "" {
				arrayRes := gjson.GetBytes(respBytes, bucketsPath)
				if arrayRes.IsArray() && len(arrayRes.Array()) > 0 {
					winKey, _ := gm["window_key"].(string)
					if winKey == "" {
						winKey = "window"
					}
					remFracKey, _ := gm["remaining_fraction_key"].(string)
					if remFracKey == "" {
						remFracKey = "remaining_fraction"
					}
					remAmtKey, _ := gm["remaining_amount_key"].(string)
					totAmtKey, _ := gm["total_amount_key"].(string)
					resetKey, _ := gm["reset_time_key"].(string)
					if resetKey == "" {
						resetKey = "reset_time"
					}
					descKey, _ := gm["description_key"].(string)
					if descKey == "" {
						descKey = "description"
					}

					for _, item := range arrayRes.Array() {
						var hasFraction bool
						var frac float64
						if remFracKey != "" {
							if val, ok := parseNumericFraction(item.Get(remFracKey)); ok {
								frac = val
								hasFraction = true
							}
						}
						if !hasFraction && remAmtKey != "" && totAmtKey != "" {
							remVal, okRem := parseNumericFraction(item.Get(remAmtKey))
							totVal, okTot := parseNumericFraction(item.Get(totAmtKey))
							if okRem && okTot && totVal > 0 {
								frac = remVal / totVal
								hasFraction = true
							}
						}
						if !hasFraction {
							continue
						}
						bucket := pluginapi.QuotaBucket{
							Window:            item.Get(winKey).String(),
							RemainingFraction: frac,
							ResetTime:         item.Get(resetKey).String(),
							Description:       item.Get(descKey).String(),
						}
						group.Buckets = append(group.Buckets, bucket)
					}
				}
			}

			if rawBuckets, okRB := gm["buckets"].([]any); okRB {
				for _, rb := range rawBuckets {
					bm, okBM := rb.(map[string]any)
					if !okBM {
						continue
					}
					var hasFraction bool
					var frac float64
					if rf, ok := bm["remaining_fraction"].(string); ok && rf != "" {
						if val, okNum := parseNumericFraction(gjson.GetBytes(respBytes, rf)); okNum {
							frac = val
							hasFraction = true
						}
					}
					if !hasFraction {
						if rem, okRem := bm["remaining_amount"].(string); okRem && rem != "" {
							if tot, okTot := bm["total_amount"].(string); okTot && tot != "" {
								remVal, okRemNum := parseNumericFraction(gjson.GetBytes(respBytes, rem))
								totVal, okTotNum := parseNumericFraction(gjson.GetBytes(respBytes, tot))
								if okRemNum && okTotNum && totVal > 0 {
									frac = remVal / totVal
									hasFraction = true
								}
							}
						}
					}
					if !hasFraction {
						continue
					}
					bucket := pluginapi.QuotaBucket{
						RemainingFraction: frac,
					}
					if win, ok := bm["window"].(string); ok {
						if res := gjson.GetBytes(respBytes, win); res.Exists() {
							bucket.Window = res.String()
						} else {
							bucket.Window = win
						}
					}
					if desc, ok := bm["description"].(string); ok {
						if res := gjson.GetBytes(respBytes, desc); res.Exists() {
							bucket.Description = res.String()
						} else {
							bucket.Description = desc
						}
					}
					if reset, ok := bm["reset_time"].(string); ok {
						if res := gjson.GetBytes(respBytes, reset); res.Exists() {
							bucket.ResetTime = res.String()
						} else {
							bucket.ResetTime = reset
						}
					}
					group.Buckets = append(group.Buckets, bucket)
				}
			}
			if len(group.Buckets) > 0 {
				out.Groups = append(out.Groups, group)
			}
		}
	}

	totalBuckets := 0
	for _, g := range out.Groups {
		totalBuckets += len(g.Buckets)
	}
	hasPlan := out.Subscription != nil && strings.TrimSpace(out.Subscription.Plan) != ""
	if totalBuckets == 0 && !hasPlan {
		return out, fmt.Errorf("response mapping did not match any valid quota fields in upstream response")
	}
	return out, nil
}
