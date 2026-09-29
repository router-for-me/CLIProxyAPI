package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
)

const (
	defaultTimeoutSeconds       = 10
	defaultEndpoint             = "/api/pricing"
	maxConcurrentFetches        = 8
	maxRatioConfigBytes         = 10 << 20 // 10MB
	floatEpsilon                = 1e-9
	officialRatioPresetID       = -100
	officialRatioPresetName     = "官方倍率预设"
	officialRatioPresetBaseURL  = "https://basellm.github.io"
	modelsDevPresetID           = -101
	modelsDevPresetName         = "models.dev 价格预设"
	modelsDevPresetBaseURL      = "https://models.dev"
	modelsDevHost               = "models.dev"
	modelsDevPath               = "/api.json"
	modelsDevInputCostRatioBase = 1000.0
)

func nearlyEqual(a, b float64) bool {
	if a > b {
		return a-b < floatEpsilon
	}
	return b-a < floatEpsilon
}

func valuesEqual(a, b any) bool {
	af, aok := a.(float64)
	bf, bok := b.(float64)
	if aok && bok {
		return nearlyEqual(af, bf)
	}
	return a == b
}

var pricingSyncFields = []string{
	"model_ratio",
	"completion_ratio",
	"cache_ratio",
	"create_cache_ratio",
	"image_ratio",
	"audio_ratio",
	"audio_completion_ratio",
	"model_price",
	billing_setting.BillingModeField,
	billing_setting.BillingExprField,
}

var numericPricingSyncFields = map[string]bool{
	"model_ratio":            true,
	"completion_ratio":       true,
	"cache_ratio":            true,
	"create_cache_ratio":     true,
	"image_ratio":            true,
	"audio_ratio":            true,
	"audio_completion_ratio": true,
	"model_price":            true,
}

type upstreamResult struct {
	Name string         `json:"name"`
	Data map[string]any `json:"data,omitempty"`
	Err  string         `json:"err,omitempty"`
}

func valueMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[string]float64:
		return lo.MapValues(typed, func(value float64, _ string) any { return value })
	case map[string]string:
		return lo.MapValues(typed, func(value string, _ string) any { return value })
	default:
		return nil
	}
}

func asFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func normalizeSyncValue(field string, value any) any {
	if numericPricingSyncFields[field] {
		if parsed, ok := asFloat64(value); ok {
			return parsed
		}
	}
	return value
}

func getLocalPricingSyncData() map[string]any {
	data := billing_setting.GetPricingSyncData(map[string]any(ratio_setting.GetExposedData()))
	data["image_ratio"] = ratio_setting.GetImageRatioCopy()
	data["audio_ratio"] = ratio_setting.GetAudioRatioCopy()
	data["audio_completion_ratio"] = ratio_setting.GetAudioCompletionRatioCopy()
	return data
}

// effectivePricingSyncData follows the billing engine's mode precedence. An
// inactive expression and numeric settings covered by an active expression
// are not separate prices and must not appear as synchronization differences.
func effectivePricingSyncData(data map[string]any) map[string]any {
	result := make(map[string]any, len(pricingSyncFields))
	names := make(map[string]struct{})
	for _, field := range pricingSyncFields {
		entries := make(map[string]any)
		for name, raw := range valueMap(data[field]) {
			value := normalizeSyncValue(field, raw)
			if numericPricingSyncFields[field] {
				number, ok := value.(float64)
				if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
					continue
				}
			}
			entries[name] = value
			names[name] = struct{}{}
		}
		result[field] = entries
	}
	modes := valueMap(result[billing_setting.BillingModeField])
	expressions := valueMap(result[billing_setting.BillingExprField])
	for name := range names {
		expression, _ := expressions[name].(string)
		if modes[name] == billing_setting.BillingModeTieredExpr {
			if strings.TrimSpace(expression) == "" {
				for _, field := range pricingSyncFields {
					delete(valueMap(result[field]), name)
				}
				continue
			}
			expressions[name] = strings.TrimSpace(expression)
			for field := range numericPricingSyncFields {
				delete(valueMap(result[field]), name)
			}
			continue
		}
		delete(expressions, name)
		modes[name] = billing_setting.BillingModeRatio
		_, fixed := valueMap(result["model_price"])[name]
		_, token := valueMap(result["model_ratio"])[name]
		if !fixed && !token {
			for _, field := range pricingSyncFields {
				delete(valueMap(result[field]), name)
			}
			continue
		}
		if fixed {
			for field := range numericPricingSyncFields {
				if field != "model_price" {
					delete(valueMap(result[field]), name)
				}
			}
		}
	}
	return result
}

func modelPricingSyncValues(data map[string]any, name string) map[string]any {
	values := make(map[string]any)
	for _, field := range pricingSyncFields {
		if value, exists := valueMap(data[field])[name]; exists {
			values[field] = value
		}
	}
	return values
}

func FetchUpstreamRatios(c *gin.Context) {
	var req dto.UpstreamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.SysError("failed to bind upstream request: " + err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请求参数格式错误"})
		return
	}

	if req.Timeout <= 0 {
		req.Timeout = defaultTimeoutSeconds
	}

	var upstreams []dto.UpstreamDTO

	if len(req.Upstreams) > 0 {
		for _, u := range req.Upstreams {
			if strings.HasPrefix(u.BaseURL, "http") {
				if u.Endpoint == "" {
					u.Endpoint = defaultEndpoint
				}
				u.BaseURL = strings.TrimRight(u.BaseURL, "/")
				upstreams = append(upstreams, u)
			}
		}
	} else if len(req.ChannelIDs) > 0 {
		intIds := make([]int, 0, len(req.ChannelIDs))
		for _, id64 := range req.ChannelIDs {
			intIds = append(intIds, int(id64))
		}
		dbChannels, err := model.GetChannelsByIds(intIds)
		if err != nil {
			logger.LogError(c.Request.Context(), "failed to query channels: "+err.Error())
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "查询渠道失败"})
			return
		}
		for _, ch := range dbChannels {
			if base := ch.GetBaseURL(); strings.HasPrefix(base, "http") {
				upstreams = append(upstreams, dto.UpstreamDTO{
					ID:       ch.Id,
					Name:     ch.Name,
					BaseURL:  strings.TrimRight(base, "/"),
					Endpoint: "",
				})
			}
		}
	}

	if len(upstreams) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "无有效上游渠道"})
		return
	}

	var wg sync.WaitGroup
	ch := make(chan upstreamResult, len(upstreams))

	sem := make(chan struct{}, maxConcurrentFetches)

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: 1 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	if common.TLSInsecureSkipVerify {
		transport.TLSClientConfig = common.InsecureTLSConfig
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		// 对 github.io 优先尝试 IPv4，失败则回退 IPv6
		if strings.HasSuffix(host, "github.io") {
			if conn, err := dialer.DialContext(ctx, "tcp4", addr); err == nil {
				return conn, nil
			}
			return dialer.DialContext(ctx, "tcp6", addr)
		}
		return dialer.DialContext(ctx, network, addr)
	}
	client := &http.Client{Transport: transport}

	for _, chn := range upstreams {
		wg.Add(1)
		go func(chItem dto.UpstreamDTO) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			isOpenRouter := chItem.Endpoint == "openrouter"

			endpoint := chItem.Endpoint
			var fullURL string
			if isOpenRouter {
				fullURL = chItem.BaseURL + "/v1/models"
			} else if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
				fullURL = endpoint
			} else {
				if endpoint == "" {
					endpoint = defaultEndpoint
				} else if !strings.HasPrefix(endpoint, "/") {
					endpoint = "/" + endpoint
				}
				fullURL = chItem.BaseURL + endpoint
			}
			isModelsDev := isModelsDevAPIEndpoint(fullURL)

			uniqueName := chItem.Name
			if chItem.ID != 0 {
				uniqueName = fmt.Sprintf("%s(%d)", chItem.Name, chItem.ID)
			}

			ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(req.Timeout)*time.Second)
			defer cancel()

			httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
			if err != nil {
				logger.LogWarn(c.Request.Context(), "build request failed: "+err.Error())
				ch <- upstreamResult{Name: uniqueName, Err: err.Error()}
				return
			}

			// OpenRouter requires Bearer token auth
			if isOpenRouter && chItem.ID != 0 {
				dbCh, err := model.GetChannelById(chItem.ID, true)
				if err != nil {
					ch <- upstreamResult{Name: uniqueName, Err: "failed to get channel key: " + err.Error()}
					return
				}
				key, _, apiErr := dbCh.GetNextEnabledKey()
				if apiErr != nil {
					ch <- upstreamResult{Name: uniqueName, Err: "failed to get enabled channel key: " + apiErr.Error()}
					return
				}
				if strings.TrimSpace(key) == "" {
					ch <- upstreamResult{Name: uniqueName, Err: "no API key configured for this channel"}
					return
				}
				httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
			} else if isOpenRouter {
				ch <- upstreamResult{Name: uniqueName, Err: "OpenRouter requires a valid channel with API key"}
				return
			}

			// 简单重试：最多 3 次，指数退避
			var resp *http.Response
			var lastErr error
			for attempt := range 3 {
				resp, lastErr = client.Do(httpReq)
				if lastErr == nil {
					break
				}
				time.Sleep(time.Duration(200*(1<<attempt)) * time.Millisecond)
			}
			if lastErr != nil {
				logger.LogWarn(c.Request.Context(), "http error on "+chItem.Name+": "+lastErr.Error())
				ch <- upstreamResult{Name: uniqueName, Err: lastErr.Error()}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				logger.LogWarn(c.Request.Context(), "non-200 from "+chItem.Name+": "+resp.Status)
				ch <- upstreamResult{Name: uniqueName, Err: resp.Status}
				return
			}

			// Content-Type 和响应体大小校验
			if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "application/json") {
				logger.LogWarn(c.Request.Context(), "unexpected content-type from "+chItem.Name+": "+ct)
			}
			limited := io.LimitReader(resp.Body, maxRatioConfigBytes)
			bodyBytes, err := io.ReadAll(limited)
			if err != nil {
				logger.LogWarn(c.Request.Context(), "read response failed from "+chItem.Name+": "+err.Error())
				ch <- upstreamResult{Name: uniqueName, Err: err.Error()}
				return
			}

			// type3: OpenRouter /v1/models -> convert per-token pricing to ratios
			if isOpenRouter {
				converted, err := convertOpenRouterToRatioData(bytes.NewReader(bodyBytes))
				if err != nil {
					logger.LogWarn(c.Request.Context(), "OpenRouter parse failed from "+chItem.Name+": "+err.Error())
					ch <- upstreamResult{Name: uniqueName, Err: err.Error()}
					return
				}
				ch <- upstreamResult{Name: uniqueName, Data: converted}
				return
			}

			// type4: models.dev /api.json -> convert provider model pricing to ratios
			if isModelsDev {
				converted, err := convertModelsDevToRatioData(bytes.NewReader(bodyBytes))
				if err != nil {
					logger.LogWarn(c.Request.Context(), "models.dev parse failed from "+chItem.Name+": "+err.Error())
					ch <- upstreamResult{Name: uniqueName, Err: err.Error()}
					return
				}
				ch <- upstreamResult{Name: uniqueName, Data: converted}
				return
			}

			// 兼容两种上游接口格式：
			//  type1: /api/ratio_config -> data 为 map[string]any，包含 model_ratio/completion_ratio/cache_ratio/model_price
			//  type2: /api/pricing      -> data 为 []Pricing 列表，需要转换为与 type1 相同的 map 格式
			var body struct {
				Success bool            `json:"success"`
				Data    json.RawMessage `json:"data"`
				Message string          `json:"message"`
			}

			if err := common.DecodeJson(bytes.NewReader(bodyBytes), &body); err != nil {
				logger.LogWarn(c.Request.Context(), "json decode failed from "+chItem.Name+": "+err.Error())
				ch <- upstreamResult{Name: uniqueName, Err: err.Error()}
				return
			}

			if !body.Success {
				ch <- upstreamResult{Name: uniqueName, Err: body.Message}
				return
			}

			// 若 Data 为空，将继续按 type1 尝试解析（与多数静态 ratio_config 兼容）

			// 尝试按 type1 解析
			var type1Data map[string]any
			if err := common.Unmarshal(body.Data, &type1Data); err == nil {
				// 如果包含至少一个 ratioTypes 字段，则认为是 type1
				isType1 := false
				for _, rt := range pricingSyncFields {
					if _, ok := type1Data[rt]; ok {
						isType1 = true
						break
					}
				}
				if isType1 {
					ch <- upstreamResult{Name: uniqueName, Data: type1Data}
					return
				}
			}

			// 如果不是 type1，则尝试按 type2 (/api/pricing) 解析
			var pricingItems []struct {
				ModelName            string   `json:"model_name"`
				QuotaType            int      `json:"quota_type"`
				ModelRatio           *float64 `json:"model_ratio"`
				ModelPrice           *float64 `json:"model_price"`
				CompletionRatio      *float64 `json:"completion_ratio"`
				CacheRatio           *float64 `json:"cache_ratio"`
				CreateCacheRatio     *float64 `json:"create_cache_ratio"`
				ImageRatio           *float64 `json:"image_ratio"`
				AudioRatio           *float64 `json:"audio_ratio"`
				AudioCompletionRatio *float64 `json:"audio_completion_ratio"`
				BillingMode          string   `json:"billing_mode"`
				BillingExpr          string   `json:"billing_expr"`
			}
			if err := common.Unmarshal(body.Data, &pricingItems); err != nil {
				logger.LogWarn(c.Request.Context(), "unrecognized data format from "+chItem.Name+": "+err.Error())
				ch <- upstreamResult{Name: uniqueName, Err: "无法解析上游返回数据"}
				return
			}

			modelRatioMap := make(map[string]float64)
			completionRatioMap := make(map[string]float64)
			cacheRatioMap := make(map[string]float64)
			createCacheRatioMap := make(map[string]float64)
			imageRatioMap := make(map[string]float64)
			audioRatioMap := make(map[string]float64)
			audioCompletionRatioMap := make(map[string]float64)
			modelPriceMap := make(map[string]float64)
			billingModeMap := make(map[string]string)
			billingExprMap := make(map[string]string)

			for _, item := range pricingItems {
				if item.ModelName == "" {
					continue
				}
				if item.BillingMode == billing_setting.BillingModeTieredExpr {
					billingModeMap[item.ModelName] = billing_setting.BillingModeTieredExpr
					billingExprMap[item.ModelName] = item.BillingExpr
					continue
				}
				if item.QuotaType == 1 {
					if item.ModelPrice != nil {
						modelPriceMap[item.ModelName] = *item.ModelPrice
					}
				} else {
					if item.ModelRatio != nil {
						modelRatioMap[item.ModelName] = *item.ModelRatio
					}
					if item.CompletionRatio != nil {
						completionRatioMap[item.ModelName] = *item.CompletionRatio
					}
				}
				if item.CacheRatio != nil {
					cacheRatioMap[item.ModelName] = *item.CacheRatio
				}
				if item.CreateCacheRatio != nil {
					createCacheRatioMap[item.ModelName] = *item.CreateCacheRatio
				}
				if item.ImageRatio != nil {
					imageRatioMap[item.ModelName] = *item.ImageRatio
				}
				if item.AudioRatio != nil {
					audioRatioMap[item.ModelName] = *item.AudioRatio
				}
				if item.AudioCompletionRatio != nil {
					audioCompletionRatioMap[item.ModelName] = *item.AudioCompletionRatio
				}
			}

			converted := make(map[string]any)

			if len(modelRatioMap) > 0 {
				ratioAny := make(map[string]any, len(modelRatioMap))
				for k, v := range modelRatioMap {
					ratioAny[k] = v
				}
				converted["model_ratio"] = ratioAny
			}

			if len(completionRatioMap) > 0 {
				compAny := make(map[string]any, len(completionRatioMap))
				for k, v := range completionRatioMap {
					compAny[k] = v
				}
				converted["completion_ratio"] = compAny
			}
			if len(cacheRatioMap) > 0 {
				converted["cache_ratio"] = valueMap(cacheRatioMap)
			}
			if len(createCacheRatioMap) > 0 {
				converted["create_cache_ratio"] = valueMap(createCacheRatioMap)
			}
			if len(imageRatioMap) > 0 {
				converted["image_ratio"] = valueMap(imageRatioMap)
			}
			if len(audioRatioMap) > 0 {
				converted["audio_ratio"] = valueMap(audioRatioMap)
			}
			if len(audioCompletionRatioMap) > 0 {
				converted["audio_completion_ratio"] = valueMap(audioCompletionRatioMap)
			}

			if len(modelPriceMap) > 0 {
				priceAny := make(map[string]any, len(modelPriceMap))
				for k, v := range modelPriceMap {
					priceAny[k] = v
				}
				converted["model_price"] = priceAny
			}
			if len(billingModeMap) > 0 {
				converted[billing_setting.BillingModeField] = valueMap(billingModeMap)
			}
			if len(billingExprMap) > 0 {
				converted[billing_setting.BillingExprField] = valueMap(billingExprMap)
			}

			ch <- upstreamResult{Name: uniqueName, Data: converted}
		}(chn)
	}

	wg.Wait()
	close(ch)

	localData := effectivePricingSyncData(getLocalPricingSyncData())

	var testResults []dto.TestResult
	var successfulChannels []struct {
		name string
		data map[string]any
	}

	for r := range ch {
		if r.Err != "" {
			testResults = append(testResults, dto.TestResult{
				Name:   r.Name,
				Status: "error",
				Error:  r.Err,
			})
		} else {
			testResults = append(testResults, dto.TestResult{
				Name:   r.Name,
				Status: "success",
			})
			successfulChannels = append(successfulChannels, struct {
				name string
				data map[string]any
			}{name: r.Name, data: effectivePricingSyncData(r.Data)})
		}
	}

	differences := buildDifferences(localData, successfulChannels)
	type modelSyncPrices struct {
		Current   map[string]any            `json:"current"`
		Upstreams map[string]map[string]any `json:"upstreams"`
	}
	prices := make(map[string]modelSyncPrices, len(differences))
	for name, fields := range differences {
		row := modelSyncPrices{Current: modelPricingSyncValues(localData, name), Upstreams: make(map[string]map[string]any)}
		_, expressionPriority := fields[billing_setting.BillingExprField]
		for _, channel := range successfulChannels {
			candidate := modelPricingSyncValues(channel.data, name)
			if expressionPriority && candidate[billing_setting.BillingModeField] != billing_setting.BillingModeTieredExpr {
				continue
			}
			_, hasRatio := candidate["model_ratio"]
			_, hasPrice := candidate["model_price"]
			_, hasExpression := candidate[billing_setting.BillingExprField]
			if hasRatio || hasPrice || hasExpression {
				row.Upstreams[channel.name] = candidate
			}
		}
		prices[name] = row
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"differences":  differences,
			"prices":       prices,
			"test_results": testResults,
		},
	})
}

func buildDifferences(localData map[string]any, successfulChannels []struct {
	name string
	data map[string]any
}) map[string]map[string]dto.DifferenceItem {
	differences := make(map[string]map[string]dto.DifferenceItem)
	localData = effectivePricingSyncData(localData)
	normalizedChannels := make([]struct {
		name string
		data map[string]any
	}, 0, len(successfulChannels))
	for _, channel := range successfulChannels {
		channel.data = effectivePricingSyncData(channel.data)
		normalizedChannels = append(normalizedChannels, channel)
	}
	successfulChannels = normalizedChannels

	allModels := make(map[string]struct{})

	for _, field := range pricingSyncFields {
		for modelName := range valueMap(localData[field]) {
			allModels[modelName] = struct{}{}
		}
	}

	for _, channel := range successfulChannels {
		for _, field := range pricingSyncFields {
			for modelName := range valueMap(channel.data[field]) {
				allModels[modelName] = struct{}{}
			}
		}
	}

	confidenceMap := make(map[string]map[string]bool)

	// 预处理阶段：检查pricing接口的可信度
	for _, channel := range successfulChannels {
		confidenceMap[channel.name] = make(map[string]bool)

		modelRatios := valueMap(channel.data["model_ratio"])
		completionRatios := valueMap(channel.data["completion_ratio"])

		if len(modelRatios) > 0 && len(completionRatios) > 0 {
			// 遍历所有模型，检查是否满足不可信条件
			for modelName := range allModels {
				// 默认为可信
				confidenceMap[channel.name][modelName] = true

				// 检查是否满足不可信条件：model_ratio为37.5且completion_ratio为1
				if modelRatioVal, ok := modelRatios[modelName]; ok {
					if completionRatioVal, ok := completionRatios[modelName]; ok {
						// 转换为float64进行比较
						modelRatioFloat, modelRatioOK := asFloat64(modelRatioVal)
						completionRatioFloat, completionRatioOK := asFloat64(completionRatioVal)
						if modelRatioOK && completionRatioOK && nearlyEqual(modelRatioFloat, 37.5) && nearlyEqual(completionRatioFloat, 1.0) {
							confidenceMap[channel.name][modelName] = false
						}
					}
				}
			}
		} else {
			// 如果不是从pricing接口获取的数据，则全部标记为可信
			for modelName := range allModels {
				confidenceMap[channel.name][modelName] = true
			}
		}
	}

	for modelName := range allModels {
		expressionPriority := valueMap(localData[billing_setting.BillingModeField])[modelName] == billing_setting.BillingModeTieredExpr
		for _, channel := range successfulChannels {
			if valueMap(channel.data[billing_setting.BillingModeField])[modelName] == billing_setting.BillingModeTieredExpr {
				expressionPriority = true
			}
		}
		for _, ratioType := range pricingSyncFields {
			if expressionPriority && numericPricingSyncFields[ratioType] {
				continue
			}
			var localValue any = nil
			if val, exists := valueMap(localData[ratioType])[modelName]; exists {
				localValue = normalizeSyncValue(ratioType, val)
			}

			upstreamValues := make(map[string]any)
			confidenceValues := make(map[string]bool)
			hasUpstreamValue := false
			hasDifference := false

			for _, channel := range successfulChannels {
				if expressionPriority && valueMap(channel.data[billing_setting.BillingModeField])[modelName] != billing_setting.BillingModeTieredExpr {
					continue
				}
				var upstreamValue any = nil

				if val, exists := valueMap(channel.data[ratioType])[modelName]; exists {
					upstreamValue = normalizeSyncValue(ratioType, val)
					hasUpstreamValue = true

					if localValue != nil && !valuesEqual(localValue, upstreamValue) {
						hasDifference = true
					} else if valuesEqual(localValue, upstreamValue) {
						upstreamValue = "same"
					}
				}
				if upstreamValue == nil && localValue == nil {
					upstreamValue = "same"
				}

				if localValue == nil && upstreamValue != nil && upstreamValue != "same" {
					hasDifference = true
				}

				upstreamValues[channel.name] = upstreamValue

				confidenceValues[channel.name] = confidenceMap[channel.name][modelName]
			}

			shouldInclude := false

			if localValue != nil {
				if hasDifference {
					shouldInclude = true
				}
			} else {
				if hasUpstreamValue {
					shouldInclude = true
				}
			}

			if shouldInclude {
				if differences[modelName] == nil {
					differences[modelName] = make(map[string]dto.DifferenceItem)
				}
				differences[modelName][ratioType] = dto.DifferenceItem{
					Current:    localValue,
					Upstreams:  upstreamValues,
					Confidence: confidenceValues,
				}
			}
		}
	}

	channelHasDiff := make(map[string]bool)
	for _, ratioMap := range differences {
		for _, item := range ratioMap {
			for chName, val := range item.Upstreams {
				if val != nil && val != "same" {
					channelHasDiff[chName] = true
				}
			}
		}
	}

	for modelName, ratioMap := range differences {
		for ratioType, item := range ratioMap {
			for chName := range item.Upstreams {
				if !channelHasDiff[chName] {
					delete(item.Upstreams, chName)
					delete(item.Confidence, chName)
				}
			}

			allSame := true
			for _, v := range item.Upstreams {
				if v != "same" {
					allSame = false
					break
				}
			}
			if len(item.Upstreams) == 0 || allSame {
				delete(ratioMap, ratioType)
			} else {
				differences[modelName][ratioType] = item
			}
		}

		if len(ratioMap) == 0 {
			delete(differences, modelName)
		}
	}

	return differences
}

func roundRatioValue(value float64) float64 {
	return math.Round(value*1e6) / 1e6
}

func isModelsDevAPIEndpoint(rawURL string) bool {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if strings.ToLower(parsedURL.Hostname()) != modelsDevHost {
		return false
	}
	path := strings.TrimSuffix(parsedURL.Path, "/")
	if path == "" {
		path = "/"
	}
	return path == modelsDevPath
}

// convertOpenRouterToRatioData parses OpenRouter's /v1/models response and converts
// per-token USD pricing into the local ratio format.
// model_ratio = prompt_price_per_token * 1_000_000 * (USD / 1000)
//
//	since 1 ratio unit = $0.002/1K tokens and USD=500, the factor is 500_000
//
// completion_ratio = completion_price / prompt_price (output/input multiplier)
func convertOpenRouterToRatioData(reader io.Reader) (map[string]any, error) {
	var orResp struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt         string `json:"prompt"`
				Completion     string `json:"completion"`
				InputCacheRead string `json:"input_cache_read"`
			} `json:"pricing"`
		} `json:"data"`
	}

	if err := common.DecodeJson(reader, &orResp); err != nil {
		return nil, fmt.Errorf("failed to decode OpenRouter response: %w", err)
	}

	modelRatioMap := make(map[string]any)
	completionRatioMap := make(map[string]any)
	cacheRatioMap := make(map[string]any)

	for _, m := range orResp.Data {
		promptPrice, promptErr := strconv.ParseFloat(m.Pricing.Prompt, 64)
		completionPrice, compErr := strconv.ParseFloat(m.Pricing.Completion, 64)

		if promptErr != nil && compErr != nil {
			// Both unparseable — skip this model
			continue
		}

		// Treat parse errors as 0
		if promptErr != nil {
			promptPrice = 0
		}
		if compErr != nil {
			completionPrice = 0
		}

		// Negative values are sentinel values (e.g., -1 for dynamic/variable pricing) — skip
		if promptPrice < 0 || completionPrice < 0 {
			continue
		}

		if promptPrice == 0 && completionPrice == 0 {
			// Free model
			modelRatioMap[m.ID] = 0.0
			continue
		}
		if promptPrice <= 0 {
			// No meaningful prompt baseline, cannot derive ratios safely.
			continue
		}

		// Normal case: promptPrice > 0
		ratio := promptPrice * 1000 * ratio_setting.USD
		ratio = roundRatioValue(ratio)
		modelRatioMap[m.ID] = ratio

		compRatio := completionPrice / promptPrice
		compRatio = roundRatioValue(compRatio)
		completionRatioMap[m.ID] = compRatio

		// Convert input_cache_read to cache_ratio (= cache_read_price / prompt_price)
		if m.Pricing.InputCacheRead != "" {
			if cachePrice, err := strconv.ParseFloat(m.Pricing.InputCacheRead, 64); err == nil && cachePrice >= 0 {
				cacheRatio := cachePrice / promptPrice
				cacheRatio = roundRatioValue(cacheRatio)
				cacheRatioMap[m.ID] = cacheRatio
			}
		}
	}

	converted := make(map[string]any)
	if len(modelRatioMap) > 0 {
		converted["model_ratio"] = modelRatioMap
	}
	if len(completionRatioMap) > 0 {
		converted["completion_ratio"] = completionRatioMap
	}
	if len(cacheRatioMap) > 0 {
		converted["cache_ratio"] = cacheRatioMap
	}

	return converted, nil
}

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	Cost modelsDevCost `json:"cost"`
}

type modelsDevCost struct {
	Input     *float64 `json:"input"`
	Output    *float64 `json:"output"`
	CacheRead *float64 `json:"cache_read"`
}

type modelsDevCandidate struct {
	Provider  string
	Input     float64
	Output    *float64
	CacheRead *float64
}

func cloneFloatPtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func isValidNonNegativeCost(v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	return v >= 0
}

func buildModelsDevCandidate(provider string, cost modelsDevCost) (modelsDevCandidate, bool) {
	if cost.Input == nil {
		return modelsDevCandidate{}, false
	}

	input := *cost.Input
	if !isValidNonNegativeCost(input) {
		return modelsDevCandidate{}, false
	}

	var output *float64
	if cost.Output != nil {
		if !isValidNonNegativeCost(*cost.Output) {
			return modelsDevCandidate{}, false
		}
		output = cloneFloatPtr(cost.Output)
	}

	// input=0/output>0 cannot be transformed into local ratio.
	if input == 0 && output != nil && *output > 0 {
		return modelsDevCandidate{}, false
	}

	var cacheRead *float64
	if cost.CacheRead != nil && isValidNonNegativeCost(*cost.CacheRead) {
		cacheRead = cloneFloatPtr(cost.CacheRead)
	}

	return modelsDevCandidate{
		Provider:  provider,
		Input:     input,
		Output:    output,
		CacheRead: cacheRead,
	}, true
}

func shouldReplaceModelsDevCandidate(current, next modelsDevCandidate) bool {
	currentNonZero := current.Input > 0
	nextNonZero := next.Input > 0
	if currentNonZero != nextNonZero {
		// Prefer non-zero pricing data; this matches "cheapest non-zero" conflict policy.
		return nextNonZero
	}
	if nextNonZero && !nearlyEqual(next.Input, current.Input) {
		return next.Input < current.Input
	}
	// Stable tie-breaker for deterministic result.
	return next.Provider < current.Provider
}

// convertModelsDevToRatioData parses models.dev /api.json and converts
// provider pricing metadata into local ratio format.
// models.dev costs are USD per 1M tokens:
//
//	model_ratio = input_cost_per_1M / 2
//	completion_ratio = output_cost / input_cost
//	cache_ratio = cache_read_cost / input_cost
//
// Duplicate model keys across providers are resolved by selecting the
// cheapest non-zero input cost. If only zero-priced candidates exist,
// a zero ratio is kept.
func convertModelsDevToRatioData(reader io.Reader) (map[string]any, error) {
	var upstreamData map[string]modelsDevProvider
	if err := common.DecodeJson(reader, &upstreamData); err != nil {
		return nil, fmt.Errorf("failed to decode models.dev response: %w", err)
	}
	if len(upstreamData) == 0 {
		return nil, fmt.Errorf("empty models.dev response")
	}

	providers := make([]string, 0, len(upstreamData))
	for provider := range upstreamData {
		providers = append(providers, provider)
	}
	sort.Strings(providers)

	selectedCandidates := make(map[string]modelsDevCandidate)
	for _, provider := range providers {
		providerData := upstreamData[provider]
		if len(providerData.Models) == 0 {
			continue
		}

		modelNames := make([]string, 0, len(providerData.Models))
		for modelName := range providerData.Models {
			modelNames = append(modelNames, modelName)
		}
		sort.Strings(modelNames)

		for _, modelName := range modelNames {
			candidate, ok := buildModelsDevCandidate(provider, providerData.Models[modelName].Cost)
			if !ok {
				continue
			}
			current, exists := selectedCandidates[modelName]
			if !exists || shouldReplaceModelsDevCandidate(current, candidate) {
				selectedCandidates[modelName] = candidate
			}
		}
	}

	if len(selectedCandidates) == 0 {
		return nil, fmt.Errorf("no valid models.dev pricing entries found")
	}

	modelRatioMap := make(map[string]any)
	completionRatioMap := make(map[string]any)
	cacheRatioMap := make(map[string]any)

	for modelName, candidate := range selectedCandidates {
		if candidate.Input == 0 {
			modelRatioMap[modelName] = 0.0
			continue
		}

		modelRatio := candidate.Input * float64(ratio_setting.USD) / modelsDevInputCostRatioBase
		modelRatioMap[modelName] = roundRatioValue(modelRatio)

		if candidate.Output != nil {
			completionRatio := *candidate.Output / candidate.Input
			completionRatioMap[modelName] = roundRatioValue(completionRatio)
		}

		if candidate.CacheRead != nil {
			cacheRatio := *candidate.CacheRead / candidate.Input
			cacheRatioMap[modelName] = roundRatioValue(cacheRatio)
		}
	}

	converted := make(map[string]any)
	if len(modelRatioMap) > 0 {
		converted["model_ratio"] = modelRatioMap
	}
	if len(completionRatioMap) > 0 {
		converted["completion_ratio"] = completionRatioMap
	}
	if len(cacheRatioMap) > 0 {
		converted["cache_ratio"] = cacheRatioMap
	}
	return converted, nil
}

func GetSyncableChannels(c *gin.Context) {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	var syncableChannels []dto.SyncableChannel
	for _, channel := range channels {
		if channel.GetBaseURL() != "" {
			syncableChannels = append(syncableChannels, dto.SyncableChannel{
				ID:      channel.Id,
				Name:    channel.Name,
				BaseURL: channel.GetBaseURL(),
				Status:  channel.Status,
				Type:    channel.Type,
			})
		}
	}

	syncableChannels = append(syncableChannels, dto.SyncableChannel{
		ID:      officialRatioPresetID,
		Name:    officialRatioPresetName,
		BaseURL: officialRatioPresetBaseURL,
		Status:  1,
	})

	syncableChannels = append(syncableChannels, dto.SyncableChannel{
		ID:      modelsDevPresetID,
		Name:    modelsDevPresetName,
		BaseURL: modelsDevPresetBaseURL,
		Status:  1,
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    syncableChannels,
	})
}
