package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// 本文件为 Kimi 套餐（base_url 含 api.kimi.com/coding 的 auth）增加"主动用量查询 + 精确冷却"能力。
//
// 背景：Kimi 推理接口额度耗尽时只返回 403 + 模糊文案，没有精确重置时间；
// 但专用端点 GET {base_url}/v1/usages 会返回 5h 与 weekly 两个滚动窗口各自的
// limit/remaining/resetTime。这里周期性查询该端点，当某窗口 remaining<=0 时，
// 把该 auth 服务的所有模型冷却到上游报告的真实 resetTime，到点惰性自动恢复。
//
// 说明：冷却状态写的是 model 级 ModelStates（选道门 isAuthBlockedForModel 在有
// model 时只认 model 级状态），三件套 Unavailable+NextRetryAfter+Quota.Exceeded
// 缺一不可；auth 级聚合由 updateAggregatedAvailability 派生。

const (
	// kimiUsageBaseURL 是 Kimi API 的基准地址，探针通过 base_url 前缀匹配识别
	// Kimi auth，不依赖 config provider 类型（claude_key / openai-compatibility 均可）。
	kimiUsageBaseURL = "https://api.kimi.com/coding"
	// kimiUsageReason 是写入 QuotaState.Reason 的统一标识，便于排查与清理。
	kimiUsageReason = "kimi quota exhausted"
	// kimiUsageMaxBody 限制读取用量响应体的大小，防止异常上游打爆内存。
	kimiUsageMaxBody = 1 << 20
)

// kimiUsageDetail 对应 /v1/usages 里单个窗口的数值。
// limit/remaining 用 json.Number 兼容整数与浮点；resetTime 可能是
// ISO8601 字符串、Unix 秒或 Unix 毫秒，统一用 any 接收后交给 parseKimiResetTime。
type kimiUsageDetail struct {
	Limit     json.Number `json:"limit"`
	Remaining json.Number `json:"remaining"`
	ResetTime any         `json:"resetTime"`
}

// kimiUsageResponse 是 /v1/usages 的顶层结构：limits[] 为 5h 窗口（可能多条），
// usage 为周/周期窗口。
type kimiUsageResponse struct {
	Limits []struct {
		Detail kimiUsageDetail `json:"detail"`
	} `json:"limits"`
	Usage kimiUsageDetail `json:"usage"`
}

// kimiUsageWindow 是解析后的单个窗口的可观测状态。
type kimiUsageWindow struct {
	Name     string
	Limit    float64
	Remaining float64
	ResetAt  time.Time
	HasReset bool
}

// parseKimiResetTime 兼容三种 resetTime 格式：ISO8601 字符串、Unix 秒、Unix 毫秒。
// 项目内无现成工具，这里自实现（参考 cc-switch 的 extract_reset_time）。
// 返回 ok=false 表示无法解析（含 0/负数/空值），调用方应跳过该窗口。
func parseKimiResetTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05.000Z",
			"2006-01-02 15:04:05",
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, s); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	case float64:
		if x <= 0 {
			return time.Time{}, false
		}
		// 数值 >= 1e12 视为毫秒，否则秒。
		if x >= 1e12 {
			return time.UnixMilli(int64(x)), true
		}
		return time.Unix(int64(x), 0), true
	case int64:
		if x <= 0 {
			return time.Time{}, false
		}
		if x >= 1e12 {
			return time.UnixMilli(x), true
		}
		return time.Unix(x, 0), true
	case json.Number:
		f, err := x.Float64()
		if err != nil || f <= 0 {
			return time.Time{}, false
		}
		if f >= 1e12 {
			return time.UnixMilli(int64(f)), true
		}
		return time.Unix(int64(f), 0), true
	}
	return time.Time{}, false
}

// windowFromDetail 把单个 detail 解析成可观测窗口。
func windowFromDetail(d kimiUsageDetail, name string) kimiUsageWindow {
	limit, _ := d.Limit.Float64()
	remaining, _ := d.Remaining.Float64()
	resetAt, hasReset := parseKimiResetTime(d.ResetTime)
	return kimiUsageWindow{
		Name:      name,
		Limit:     limit,
		Remaining: remaining,
		ResetAt:   resetAt,
		HasReset:  hasReset,
	}
}

// isKimiUsageAuth 判断一个 auth 是否是可查询用量的 Kimi auth。
// 通过 base_url 前缀匹配，不限定 config provider 类型（claude_key / openai-compatibility 均可）。
func isKimiUsageAuth(auth *Auth) bool {
	if auth == nil || auth.Provider == "" {
		return false
	}
	if auth.Attributes == nil || strings.TrimSpace(auth.Attributes["api_key"]) == "" {
		return false
	}
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if baseURL == "" {
		return false
	}
	// 前缀匹配：base_url 以 kimiUsageBaseURL 开头即视为 Kimi auth。
	return strings.HasPrefix(strings.TrimSuffix(baseURL, "/"), kimiUsageBaseURL)
}

// fetchKimiUsage 查询某 Kimi auth 的用量窗口。复用 Manager.NewHttpRequest/HttpRequest，
// 自动注入 Bearer api_key 并走 per-auth 代理（与推理请求同路径）。失败只返回 error，
// 由调用方决定是否记日志后跳过；不在此处触发任何冷却。
func (m *Manager) fetchKimiUsage(ctx context.Context, auth *Auth) ([]kimiUsageWindow, error) {
	if m == nil || auth == nil {
		return nil, fmt.Errorf("kimi usage: nil manager or auth")
	}
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if baseURL == "" {
		return nil, fmt.Errorf("kimi usage: empty base_url for auth %s", auth.ID)
	}
	targetURL := strings.TrimSuffix(baseURL, "/") + "/v1/usages"

	req, err := m.NewHttpRequest(ctx, auth, http.MethodGet, targetURL, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("kimi usage: build request: %w", err)
	}
	resp, err := m.HttpRequest(ctx, auth, req)
	if err != nil {
		return nil, fmt.Errorf("kimi usage: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, kimiUsageMaxBody))
	if err != nil {
		return nil, fmt.Errorf("kimi usage: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 401/403 通常是 key 失效，这里不当作"额度耗尽"，交给常规错误冷却路径。
		return nil, fmt.Errorf("kimi usage: upstream status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed kimiUsageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("kimi usage: parse: %w", err)
	}

	windows := make([]kimiUsageWindow, 0, len(parsed.Limits)+1)
	for _, lim := range parsed.Limits {
		windows = append(windows, windowFromDetail(lim.Detail, "five_hour"))
	}
	windows = append(windows, windowFromDetail(parsed.Usage, "weekly"))
	return windows, nil
}

// kimiUsageCooldown 计算"账号恢复时刻"。账号可用需要所有窗口都有余量，
// 因此冷却到所有"已耗尽且带有效 resetTime"窗口中最晚的 resetTime。
// 返回 ok=true 表示存在可精确冷却的耗尽窗口；ok=false 表示要么没耗尽、
// 要么耗尽但上游没给 resetTime（后者调用方应跳过，退回常规错误冷却）。
func kimiUsageCooldown(windows []kimiUsageWindow) (recoverAt time.Time, ok bool) {
	for _, w := range windows {
		if w.Remaining > 0 {
			continue
		}
		if !w.HasReset {
			continue
		}
		ok = true
		if w.ResetAt.After(recoverAt) {
			recoverAt = w.ResetAt
		}
	}
	return recoverAt, ok
}

// kimiUsageFullyAvailable 判断是否所有可观测窗口都有余量（用于触发提前恢复清理）。
// 窗口列表为空时返回 false，避免在解析异常时误清。
func kimiUsageFullyAvailable(windows []kimiUsageWindow) bool {
	if len(windows) == 0 {
		return false
	}
	for _, w := range windows {
		// limit<=0 表示该窗口可能不适用（上游未启用），忽略；其余要求 remaining>0。
		if w.Limit <= 0 {
			continue
		}
		if w.Remaining <= 0 {
			return false
		}
	}
	return true
}

// SetAuthQuotaExceeded 把指定 auth 的所有已注册模型标记为"额度耗尽，冷却到 recoverAt"。
// 供后台用量探针调用，不走请求路径。锁内改 model 级状态 + 聚合，锁外做 registry 与持久化，
// 完全照 MarkResult 的并发模式。仅扩展（不缩短已有的更长冷却），避免覆盖其它原因的长冷却。
func (m *Manager) SetAuthQuotaExceeded(ctx context.Context, authID string, recoverAt time.Time, reason string) (*Auth, error) {
	if m == nil {
		return nil, nil
	}
	authID = strings.TrimSpace(authID)
	now := time.Now()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = kimiUsageReason
	}
	// recoverAt 必须是未来时刻，否则无意义（惰性到期会立即放行）。
	if authID == "" || recoverAt.IsZero() || !recoverAt.After(now) {
		return nil, nil
	}

	var snapshot *Auth
	changedModels := make([]string, 0)
	cooldownStateChanged := false

	m.mu.Lock()
	auth, ok := m.auths[authID]
	if !ok || auth == nil {
		m.mu.Unlock()
		return nil, nil
	}
	// 全局关闭冷却时跳过，与 MarkResult 的 disableCooling 语义一致。
	if m.cooldownDisabledForAuth(auth) {
		m.mu.Unlock()
		return nil, nil
	}

	var cooldownRecordsBefore []CooldownStateRecord
	trackCooldownState := m.cooldownStore != nil
	if trackCooldownState {
		cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(auth, now)
	}

	// 模型集合 = registry 注册模型 ∪ 已有 ModelStates 的 key。
	// 注册模型覆盖"尚未触发过失败"的模型，确保账号级耗尽时全部预冷，不漏。
	modelSet := make(map[string]struct{})
	for _, mid := range modelsForRegisteredAuth(authID) {
		modelSet[mid] = struct{}{}
	}
	for mid := range auth.ModelStates {
		modelSet[mid] = struct{}{}
	}

	for model := range modelSet {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		state := ensureModelState(auth, model)
		// 仅扩展：若已有更晚的冷却（如 12h 的 model_not_supported），不要缩短它。
		if !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(recoverAt) {
			continue
		}
		state.Unavailable = true
		state.Status = StatusError
		state.StatusMessage = reason
		state.NextRetryAfter = recoverAt
		state.Quota = QuotaState{Exceeded: true, Reason: reason, NextRecoverAt: recoverAt}
		state.UpdatedAt = now
		changedModels = append(changedModels, model)
	}

	if len(changedModels) > 0 {
		auth.Status = StatusError
		auth.UpdatedAt = now
		updateAggregatedAvailability(auth, now)
	}
	// persist 对 config-api-key auth 是 no-op（冷却状态走 .cds store），此处仍调以保持一致。
	_ = m.persist(ctx, auth)
	snapshot = auth.Clone()
	if trackCooldownState {
		after := m.cooldownStateRecordsForAuthLocked(auth, now)
		cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, after)
	}
	m.mu.Unlock()

	// 锁外：registry 可见性（对齐 MarkResult 的 quota 路径，影响 /models 列表与客户端路由）。
	for _, model := range changedModels {
		registry.GetGlobalRegistry().SetModelQuotaExceeded(authID, model)
		registry.GetGlobalRegistry().SuspendClientModel(authID, model, "quota")
	}
	if m.scheduler != nil && snapshot != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	if snapshot != nil && cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return snapshot, nil
}

// hasAuthQuotaExceeded 在已持锁或快照上判断 auth 是否当前处于额度耗尽冷却。
// 探针用它避免对健康账号反复触发清理。
func hasAuthQuotaExceeded(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Quota.Exceeded && !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now) {
			return true
		}
	}
	return false
}
