package zai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

const (
	// StartPlanBaseURL is the OpenAI-compatible root of the Start Plan (free
	// tier) gateway; the Anthropic messages root adds the /anthropic suffix.
	StartPlanBaseURL = "https://zcode.z.ai/api/v1/zcode-plan"
	// StartPlanAnthropicBaseURL is the inference root the official client
	// uses; ClaudeExecutor appends /v1/messages to it.
	StartPlanAnthropicBaseURL = StartPlanBaseURL + "/anthropic"
	// StartPlanAppVersion mirrors the official client version; the balance
	// endpoint gates model capabilities on it. The version must stay in
	// lockstep with the client the system-gate fingerprint blocks were
	// captured from (see claude_executor_zai_startplan.go).
	StartPlanAppVersion = "3.14.4"
)

// ZCodeSourceHeaders mirrors the identity headers the official client sends
// to the zcode.z.ai origin; plan and billing endpoints gate on them. The
// backend rejects balance calls without a device id (HTTP 400 code 3001), so
// callers pass a stable per-account UUID.
func ZCodeSourceHeaders(deviceMid string) map[string]string {
	headers := map[string]string{
		"User-Agent":          "ZCode/" + StartPlanAppVersion,
		"HTTP-Referer":        "https://zcode.z.ai",
		"X-Title":             "Z Code@electron",
		"X-ZCode-App-Version": StartPlanAppVersion,
		"X-Release-Channel":   "production",
		"X-Platform":          runtime.GOOS + "-" + runtime.GOARCH,
		"X-Os-Category":       zcodeOsCategory(runtime.GOOS),
		"X-Client-Language":   "en-US",
		"X-Client-Timezone":   "UTC",
	}
	if osVersion := zcodeOsVersion(); osVersion != "" {
		headers["X-Os-Version"] = osVersion
	}
	if mid := strings.TrimSpace(deviceMid); mid != "" {
		headers["X-Device-Mid"] = mid
	}
	return headers
}

// ApplyZCodeSourceHeaders sets the official client identity headers on req.
func ApplyZCodeSourceHeaders(req *http.Request, deviceMid string) {
	for name, value := range ZCodeSourceHeaders(deviceMid) {
		req.Header.Set(name, value)
	}
}

func zcodeOsCategory(platform string) string {
	switch platform {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

// printableASCII mirrors the official client's header normalization: a value
// is kept only when it is entirely printable ASCII.
func printableASCII(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] < 0x20 || trimmed[i] > 0x7e {
			return ""
		}
	}
	return trimmed
}

// StartPlanDeviceMid resolves the per-account device id sent to the zcode-plan
// endpoints: the device_mid captured at OAuth login, or a UUID derived from
// the auth identity when an older auth file predates the field.
func StartPlanDeviceMid(metadata map[string]any, seed string) string {
	if metadata != nil {
		if mid, ok := metadata["device_mid"].(string); ok {
			if trimmed := strings.TrimSpace(mid); trimmed != "" {
				return trimmed
			}
		}
	}
	if trimmed := strings.TrimSpace(seed); trimmed != "" {
		return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api\x00zai\x00device-mid\x00"+trimmed)).String()
	}
	return ""
}

// ZCodeMetadataUserID builds the Anthropic metadata.user_id value the official
// client attaches to every zcode-plan model request: a JSON string carrying
// the device identity shared with the X-Device-Mid header and the current
// session id (docs/anthropic-request-metadata.md in the ZCode source).
func ZCodeMetadataUserID(deviceMid, sessionID string) string {
	payload := struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}{
		DeviceID:    strings.TrimSpace(deviceMid),
		AccountUUID: "",
		SessionID:   strings.TrimSpace(sessionID),
	}
	encoded, errEncode := json.Marshal(payload)
	if errEncode != nil {
		return ""
	}
	return string(encoded)
}

// StartPlanStatus reports whether the account holds an active Start Plan and
// which models its balance buckets currently allow.
type StartPlanStatus struct {
	Active bool
	Models []string
}

// StartPlanPlan is one Start Plan entitlement row of the balance API.
type StartPlanPlan struct {
	UserPlanID string      `json:"user_plan_id,omitempty"`
	PlanID     string      `json:"plan_id,omitempty"`
	Name       string      `json:"name,omitempty"`
	Status     string      `json:"status,omitempty"`
	EndsAt     json.Number `json:"ends_at,omitempty"`
}

// StartPlanBalanceEntry is one quota bucket of the balance API.
type StartPlanBalanceEntry struct {
	UserPlanID     string      `json:"user_plan_id,omitempty"`
	PlanID         string      `json:"plan_id,omitempty"`
	ShowName       string      `json:"show_name,omitempty"`
	Capabilities   []string    `json:"capabilities,omitempty"`
	TotalUnits     json.Number `json:"total_units,omitempty"`
	RemainingUnits json.Number `json:"remaining_units,omitempty"`
	AvailableUnits json.Number `json:"available_units,omitempty"`
	PeriodStart    json.Number `json:"period_start,omitempty"`
	PeriodEnd      json.Number `json:"period_end,omitempty"`
}

// StartPlanBalance is the decoded data payload of the Start Plan balance API.
type StartPlanBalance struct {
	ServerTime json.Number             `json:"server_time,omitempty"`
	Plans      []StartPlanPlan         `json:"plans,omitempty"`
	Balances   []StartPlanBalanceEntry `json:"balances,omitempty"`
}

// SetStartPlanBaseURL overrides the Start Plan inference base (tests only).
func (s *Service) SetStartPlanBaseURL(baseURL string) {
	if s == nil {
		return
	}
	if trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/"); trimmed != "" {
		s.startPlanBaseURL = trimmed
	}
}

func (s *Service) startPlanBalanceURL() string {
	base := StartPlanBaseURL
	if s != nil && strings.TrimSpace(s.startPlanBaseURL) != "" {
		base = strings.TrimRight(strings.TrimSpace(s.startPlanBaseURL), "/")
	}
	return base + "/billing/balance?app_version=" + url.QueryEscape(StartPlanAppVersion)
}

// FetchStartPlanBalance queries the Start Plan balance endpoint with the
// ZCode JWT issued at OAuth login; deviceMid identifies this installation.
func (s *Service) FetchStartPlanBalance(ctx context.Context, zcodeJWT, deviceMid string) (*StartPlanBalance, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("zai oauth service unavailable")
	}
	token := strings.TrimSpace(zcodeJWT)
	if token == "" {
		return nil, fmt.Errorf("Z.ai Start Plan balance is missing ZCode token")
	}
	raw, errFetch := s.sourceHeaderRequest(ctx, http.MethodGet, s.startPlanBalanceURL(), token, deviceMid, nil)
	if errFetch != nil {
		return nil, errFetch
	}
	var balance StartPlanBalance
	if errDecode := decodeStartPlanEnvelope(raw, &balance); errDecode != nil {
		return nil, errDecode
	}
	return &balance, nil
}

// sourceHeaderRequest executes one request carrying the official client
// identity headers and returns the raw body, failing on non-2xx statuses.
func (s *Service) sourceHeaderRequest(ctx context.Context, method, endpoint, bearer, deviceMid string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, errReq := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if errReq != nil {
		return nil, errReq
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	ApplyZCodeSourceHeaders(req, deviceMid)
	if strings.TrimSpace(bearer) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	}
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("Z.ai Start Plan request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("zai start plan response close failed")
		}
	}()
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, fmt.Errorf("read Z.ai Start Plan response: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Z.ai Start Plan request rejected with status %d: %s", resp.StatusCode, truncateForLog(data))
	}
	return data, nil
}

// decodeStartPlanEnvelope unwraps the {code,msg,data} envelope used by the
// zcode.z.ai plan endpoints.
func decodeStartPlanEnvelope(raw []byte, target any) error {
	var envelope struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Data) > 0 {
		switch envelope.Code.String() {
		case "", "0", "200":
		default:
			if strings.TrimSpace(envelope.Msg) != "" {
				return fmt.Errorf("Z.ai Start Plan rejected: %s", strings.TrimSpace(envelope.Msg))
			}
			return fmt.Errorf("Z.ai Start Plan rejected with code %s", envelope.Code.String())
		}
		if errDecode := json.Unmarshal(envelope.Data, target); errDecode != nil {
			return fmt.Errorf("decode Z.ai Start Plan response: %w (body: %s)", errDecode, truncateForLog(raw))
		}
		return nil
	}
	if errDecode := json.Unmarshal(raw, target); errDecode != nil {
		return fmt.Errorf("decode Z.ai Start Plan response: %w (body: %s)", errDecode, truncateForLog(raw))
	}
	return nil
}

// FetchStartPlanStatus reduces the balance payload to an active flag plus the
// model whitelist, mirroring the official client's availability check.
func (s *Service) FetchStartPlanStatus(ctx context.Context, zcodeJWT, deviceMid string) (*StartPlanStatus, error) {
	balance, errBalance := s.FetchStartPlanBalance(ctx, zcodeJWT, deviceMid)
	if errBalance != nil {
		return nil, errBalance
	}
	return &StartPlanStatus{Active: balance.ActiveStartPlan(), Models: balance.ModelIDs()}, nil
}

// ActiveStartPlan reports whether any Start Plan row is currently active.
func (b *StartPlanBalance) ActiveStartPlan() bool {
	if b == nil {
		return false
	}
	now := b.now()
	for _, plan := range b.Plans {
		if !strings.EqualFold(strings.TrimSpace(plan.Status), "active") || b.planExpired(plan, now) {
			continue
		}
		// The backend identifies Start Plan rows by plan_id/name containing
		// "start-plan"; rows without identity fields are treated as matching
		// so a renamed plan does not break detection.
		if plan.PlanID == "" && plan.Name == "" {
			return true
		}
		if isStartPlanIdentity(plan.PlanID) || isStartPlanIdentity(plan.Name) {
			return true
		}
	}
	return false
}

// ModelIDs extracts the model whitelist from live balance buckets: the
// "model:<id>" capabilities win, otherwise the bucket display name is used.
func (b *StartPlanBalance) ModelIDs() []string {
	if b == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, entry := range b.LiveEntries() {
		candidates := make([]string, 0, len(entry.Capabilities)+1)
		for _, capability := range entry.Capabilities {
			trimmed := strings.TrimSpace(capability)
			if strings.HasPrefix(strings.ToLower(trimmed), "model:") {
				candidates = append(candidates, strings.TrimSpace(trimmed[len("model:"):]))
			}
		}
		if len(candidates) == 0 {
			candidates = append(candidates, strings.TrimSpace(entry.ShowName))
		}
		for _, candidate := range candidates {
			if candidate == "" {
				continue
			}
			key := strings.ToLower(candidate)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			ids = append(ids, key)
		}
	}
	return ids
}

// LiveEntries drops buckets whose owning plan rows have all expired.
func (b *StartPlanBalance) LiveEntries() []StartPlanBalanceEntry {
	now := b.now()
	expired := make(map[string]bool)
	for _, plan := range b.Plans {
		if b.planExpired(plan, now) {
			if plan.UserPlanID != "" {
				expired[plan.UserPlanID] = true
			} else if plan.PlanID != "" {
				expired[plan.PlanID] = true
			}
		}
	}
	entries := make([]StartPlanBalanceEntry, 0, len(b.Balances))
	for _, entry := range b.Balances {
		if ownedByExpired(entry, b.Plans, now) {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// ownedByExpired mirrors the official client: a bucket is dropped only when it
// has owners and every owner plan is expired; unowned buckets are kept.
func ownedByExpired(entry StartPlanBalanceEntry, plans []StartPlanPlan, now float64) bool {
	var owners []StartPlanPlan
	for _, plan := range plans {
		match := false
		if entry.UserPlanID != "" && plan.UserPlanID != "" {
			match = entry.UserPlanID == plan.UserPlanID
		} else {
			match = entry.PlanID != "" && entry.PlanID == plan.PlanID
		}
		if match {
			owners = append(owners, plan)
		}
	}
	if len(owners) == 0 {
		return false
	}
	for _, plan := range owners {
		end, okEnd := numberFloat(plan.EndsAt)
		expired := !strings.EqualFold(strings.TrimSpace(plan.Status), "active") || (okEnd && end > 0 && end <= now)
		if !expired {
			return false
		}
	}
	return true
}

func (b *StartPlanBalance) planExpired(plan StartPlanPlan, now float64) bool {
	end, okEnd := numberFloat(plan.EndsAt)
	return okEnd && end > 0 && end <= now
}

func (b *StartPlanBalance) now() float64 {
	if b != nil {
		if now, ok := numberFloat(b.ServerTime); ok && now > 0 {
			return now
		}
	}
	return float64(time.Now().Unix())
}

func isStartPlanIdentity(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(value, "start-plan") || strings.Contains(value, "start plan")
}

func numberFloat(n json.Number) (float64, bool) {
	if n == "" {
		return 0, false
	}
	value, err := n.Float64()
	if err != nil {
		return 0, false
	}
	return value, true
}
