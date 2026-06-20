package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	subscriptionAccountsCheckURL = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"
	subscriptionsURL             = "https://chatgpt.com/backend-api/subscriptions"
	chatGPTWebReferer            = "https://chatgpt.com/"
	chatGPTWebUserAgent          = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
)

// SubscriptionSnapshot is the normalized ChatGPT subscription status for a Codex account.
type SubscriptionSnapshot struct {
	AccountID   string
	PlanType    string
	ActiveUntil string
}

type subscriptionAccountRecord struct {
	key  string
	node any
}

// EnrichSubscriptionMetadata adds ChatGPT subscription fields to metadata using values already in the auth JSON.
func EnrichSubscriptionMetadata(ctx context.Context, metadata map[string]any, client *http.Client) (bool, error) {
	if metadata == nil {
		return false, nil
	}
	return EnrichSubscriptionMetadataForTokens(
		ctx,
		metadata,
		stringMetadata(metadata, "id_token"),
		stringMetadata(metadata, "access_token"),
		firstNonEmptyString(
			stringMetadata(metadata, "chatgpt_account_id"),
			stringMetadata(metadata, "account_id"),
		),
		client,
	)
}

// EnrichSubscriptionMetadata adds ChatGPT subscription fields using this auth service HTTP client.
func (o *CodexAuth) EnrichSubscriptionMetadata(ctx context.Context, metadata map[string]any, idToken, accessToken, accountID string) (bool, error) {
	var client *http.Client
	if o != nil {
		client = o.httpClient
	}
	return EnrichSubscriptionMetadataForTokens(ctx, metadata, idToken, accessToken, accountID, client)
}

// EnrichSubscriptionMetadataForTokens fills metadata from JWT claims first, then falls back to ChatGPT backend APIs.
func EnrichSubscriptionMetadataForTokens(ctx context.Context, metadata map[string]any, idToken, accessToken, accountID string, client *http.Client) (bool, error) {
	if metadata == nil {
		return false, nil
	}
	changed := false
	log.Debugf("Codex subscription metadata enrichment attempt: has_id_token=%t has_access_token=%t account_id=%s", strings.TrimSpace(idToken) != "", strings.TrimSpace(accessToken) != "", strings.TrimSpace(accountID))

	if claims, err := ParseJWTToken(strings.TrimSpace(idToken)); err == nil && claims != nil {
		if setStringMetadata(metadata, "chatgpt_account_id", strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID)) {
			changed = true
		}
		if setStringMetadata(metadata, "account_id", strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID)) {
			changed = true
		}
		if setStringMetadata(metadata, "plan_type", normalizeSubscriptionPlan(claims.CodexAuthInfo.ChatgptPlanType)) {
			changed = true
		}
		if activeUntil := normalizeSubscriptionScalar(claims.CodexAuthInfo.ChatgptSubscriptionActiveUntil); activeUntil != "" {
			if setStringMetadata(metadata, "chatgpt_subscription_active_until", activeUntil) {
				changed = true
			}
			if setStringMetadata(metadata, "subscription_active_until", activeUntil) {
				changed = true
			}
		}
	}

	currentActiveUntil := firstNonEmptyString(
		stringMetadata(metadata, "subscription_active_until"),
		stringMetadata(metadata, "chatgpt_subscription_active_until"),
	)
	if !subscriptionMissingOrExpired(currentActiveUntil) {
		log.Debugf("Codex subscription metadata enrichment using existing expiry: account_id=%s active_until=%s", firstNonEmptyString(accountID, stringMetadata(metadata, "account_id")), currentActiveUntil)
		if updateSubscriptionExpiredMetadata(metadata, currentActiveUntil) {
			changed = true
		}
		return changed, nil
	}

	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		log.Debugf("Codex subscription metadata enrichment skipped backend fallback: missing access token account_id=%s", firstNonEmptyString(accountID, stringMetadata(metadata, "account_id")))
		if currentActiveUntil != "" && updateSubscriptionExpiredMetadata(metadata, currentActiveUntil) {
			changed = true
		}
		return changed, nil
	}

	preferredAccountID := firstNonEmptyString(
		strings.TrimSpace(accountID),
		stringMetadata(metadata, "chatgpt_account_id"),
		stringMetadata(metadata, "account_id"),
	)
	snapshot, err := FetchSubscriptionStatus(ctx, accessToken, preferredAccountID, client)
	if err != nil {
		log.Debugf("Codex subscription metadata backend fallback failed: account_id=%s error=%v", preferredAccountID, err)
		if currentActiveUntil != "" && updateSubscriptionExpiredMetadata(metadata, currentActiveUntil) {
			changed = true
		}
		return changed, err
	}

	if setStringMetadata(metadata, "chatgpt_account_id", snapshot.AccountID) {
		changed = true
	}
	if setStringMetadata(metadata, "account_id", snapshot.AccountID) {
		changed = true
	}
	if setStringMetadata(metadata, "plan_type", normalizeSubscriptionPlan(snapshot.PlanType)) {
		changed = true
	}
	if setStringMetadata(metadata, "chatgpt_subscription_active_until", snapshot.ActiveUntil) {
		changed = true
	}
	if setStringMetadata(metadata, "subscription_active_until", snapshot.ActiveUntil) {
		changed = true
	}
	if setStringMetadata(metadata, "chatgpt_subscription_last_checked", time.Now().UTC().Format(time.RFC3339)) {
		changed = true
	}
	if updateSubscriptionExpiredMetadata(metadata, snapshot.ActiveUntil) {
		changed = true
	}

	return changed, nil
}

// FetchSubscriptionStatus returns ChatGPT subscription state using accounts/check with subscriptions fallback.
func FetchSubscriptionStatus(ctx context.Context, accessToken, preferredAccountID string, client *http.Client) (*SubscriptionSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = http.DefaultClient
	}
	log.Debugf("Codex subscription status fetch attempt: account_id=%s", strings.TrimSpace(preferredAccountID))

	snapshot, err := fetchAccountsCheckSnapshot(ctx, client, accessToken, preferredAccountID)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, fmt.Errorf("accounts/check returned no account records")
	}
	if !subscriptionMissingOrExpired(snapshot.ActiveUntil) {
		log.Debugf("Codex subscription status fetched from accounts/check: account_id=%s active_until=%s", snapshot.AccountID, snapshot.ActiveUntil)
		return snapshot, nil
	}

	accountID := firstNonEmptyString(snapshot.AccountID, strings.TrimSpace(preferredAccountID))
	if accountID == "" {
		log.Debug("Codex subscription subscriptions fallback skipped: missing account_id")
		return snapshot, nil
	}
	log.Debugf("Codex subscription subscriptions fallback attempt: account_id=%s", accountID)
	subscriptionSnapshot, err := fetchSubscriptionsSnapshot(ctx, client, accessToken, accountID)
	if err != nil {
		log.Debugf("Codex subscription subscriptions fallback failed: account_id=%s error=%v", accountID, err)
		return snapshot, nil
	}
	if subscriptionSnapshot.PlanType != "" {
		snapshot.PlanType = subscriptionSnapshot.PlanType
	}
	if subscriptionSnapshot.ActiveUntil != "" {
		snapshot.ActiveUntil = subscriptionSnapshot.ActiveUntil
	}
	if subscriptionSnapshot.AccountID != "" {
		snapshot.AccountID = subscriptionSnapshot.AccountID
	}
	return snapshot, nil
}

func fetchAccountsCheckSnapshot(ctx context.Context, client *http.Client, accessToken, preferredAccountID string) (*SubscriptionSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, subscriptionAccountsCheckURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create accounts/check request: %w", err)
	}
	q := req.URL.Query()
	q.Set("timezone_offset_min", "0")
	req.URL.RawQuery = q.Encode()
	setSubscriptionHeaders(req, accessToken, "/backend-api/accounts/check/v4-2023-04-27")
	log.Debugf("Codex subscription accounts/check request attempt: account_id=%s", strings.TrimSpace(preferredAccountID))

	payload, err := doSubscriptionJSON(client, req)
	if err != nil {
		return nil, err
	}
	return parseAccountsCheckSnapshot(payload, preferredAccountID), nil
}

func fetchSubscriptionsSnapshot(ctx context.Context, client *http.Client, accessToken, accountID string) (*SubscriptionSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, subscriptionsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create subscriptions request: %w", err)
	}
	q := req.URL.Query()
	q.Set("account_id", accountID)
	req.URL.RawQuery = q.Encode()
	setSubscriptionHeaders(req, accessToken, "/backend-api/subscriptions")
	log.Debugf("Codex subscription subscriptions request attempt: account_id=%s", strings.TrimSpace(accountID))

	payload, err := doSubscriptionJSON(client, req)
	if err != nil {
		return nil, err
	}
	return &SubscriptionSnapshot{
		AccountID:   strings.TrimSpace(accountID),
		PlanType:    firstJSONScalar(payload, "subscription_plan", "plan_type"),
		ActiveUntil: firstJSONScalar(payload, "active_until", "expires_at"),
	}, nil
}

func setSubscriptionHeaders(req *http.Request, accessToken, targetPath string) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", chatGPTWebReferer)
	req.Header.Set("User-Agent", chatGPTWebUserAgent)
	req.Header.Set("x-openai-target-path", targetPath)
	req.Header.Set("x-openai-target-route", targetPath)
}

func doSubscriptionJSON(client *http.Client, req *http.Request) (map[string]any, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscription request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read subscription response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription request failed with status %d body_len=%d", resp.StatusCode, len(body))
	}
	var payload map[string]any
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse subscription response: %w", err)
	}
	return payload, nil
}

func parseAccountsCheckSnapshot(payload map[string]any, preferredAccountID string) *SubscriptionSnapshot {
	records := collectSubscriptionAccountRecords(payload)
	if len(records) == 0 {
		return nil
	}

	preferredAccountID = strings.TrimSpace(preferredAccountID)
	selected := records[0]
	if preferredAccountID != "" {
		for _, record := range records {
			accountRecord := accountObjectFromRecord(record.node)
			candidateID := firstJSONScalar(accountRecord, "account_id", "id", "chatgpt_account_id", "workspace_id")
			// Object-keyed responses carry the account id as the map key, which
			// may not be repeated inside the value; match on it too.
			if candidateID == preferredAccountID || strings.TrimSpace(record.key) == preferredAccountID {
				selected = record
				break
			}
		}
	}

	node, _ := selected.node.(map[string]any)
	if node == nil {
		return nil
	}
	accountRecord := accountObjectFromRecord(node)
	entitlement, _ := node["entitlement"].(map[string]any)

	return &SubscriptionSnapshot{
		AccountID: firstNonEmptyString(
			firstJSONScalar(accountRecord, "account_id", "id", "chatgpt_account_id", "workspace_id"),
			strings.TrimSpace(selected.key),
		),
		PlanType: firstNonEmptyString(
			firstJSONScalar(entitlement, "subscription_plan"),
			firstJSONScalar(accountRecord, "plan_type", "planType"),
		),
		ActiveUntil: firstNonEmptyString(
			firstJSONScalar(entitlement, "expires_at"),
			firstJSONScalar(accountRecord, "expires_at"),
		),
	}
}

func collectSubscriptionAccountRecords(payload map[string]any) []subscriptionAccountRecord {
	var records []subscriptionAccountRecord
	for _, key := range []string{"accounts", "account_items", "items", "data"} {
		value := payload[key]
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				records = append(records, subscriptionAccountRecord{node: item})
			}
		case map[string]any:
			for recordKey, item := range typed {
				records = append(records, subscriptionAccountRecord{key: recordKey, node: item})
			}
		}
	}
	return records
}

func accountObjectFromRecord(record any) map[string]any {
	node, _ := record.(map[string]any)
	if node == nil {
		return nil
	}
	if account, ok := node["account"].(map[string]any); ok && account != nil {
		return account
	}
	return node
}

func firstJSONScalar(obj map[string]any, keys ...string) string {
	if obj == nil {
		return ""
	}
	for _, key := range keys {
		if value := normalizeSubscriptionScalar(obj[key]); value != "" {
			return value
		}
	}
	return ""
}

func normalizeSubscriptionScalar(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	case float64:
		if math.Trunc(typed) == typed {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		asFloat := float64(typed)
		if math.Trunc(asFloat) == asFloat {
			return strconv.FormatInt(int64(asFloat), 10)
		}
		return strconv.FormatFloat(asFloat, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func parseSubscriptionTime(value any) (time.Time, bool) {
	raw := normalizeSubscriptionScalar(value)
	if raw == "" {
		return time.Time{}, false
	}
	if isDigits(raw) {
		timestamp, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		if timestamp > 1_000_000_000_000 {
			timestamp /= 1000
		}
		return time.Unix(timestamp, 0).UTC(), true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err == nil {
		return parsed.UTC(), true
	}
	return time.Time{}, false
}

func subscriptionMissingOrExpired(value any) bool {
	parsed, ok := parseSubscriptionTime(value)
	return !ok || !parsed.After(time.Now().UTC())
}

// IsSubscriptionExpired reports whether a subscription expiry value is missing,
// unparseable, or already in the past relative to now (UTC). Callers can use it
// to derive the expired state at response time instead of trusting a cached
// boolean that may have gone stale since the last enrichment.
func IsSubscriptionExpired(activeUntil any) bool {
	return subscriptionMissingOrExpired(activeUntil)
}

func updateSubscriptionExpiredMetadata(metadata map[string]any, activeUntil string) bool {
	parsed, ok := parseSubscriptionTime(activeUntil)
	if !ok {
		return false
	}
	expired := !parsed.After(time.Now().UTC())
	return setBoolMetadata(metadata, "subscription_expired", expired)
}

func stringMetadata(metadata map[string]any, key string) string {
	return normalizeSubscriptionScalar(metadata[key])
}

func setStringMetadata(metadata map[string]any, key, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if stringMetadata(metadata, key) == value {
		return false
	}
	metadata[key] = value
	return true
}

func setBoolMetadata(metadata map[string]any, key string, value bool) bool {
	if current, ok := metadata[key].(bool); ok && current == value {
		return false
	}
	metadata[key] = value
	return true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// normalizeSubscriptionPlan maps web subscription_plan values (e.g.
// "chatgptplusplan", "chatgpt_free_plan", "ChatGPT Pro") to the canonical plan
// tokens the rest of the app expects (e.g. "free", "plus", "pro"); values that
// are already canonical pass through unchanged. The free-plan check and model
// registration read this normalized form from Attributes["plan_type"].
func normalizeSubscriptionPlan(plan string) string {
	trimmed := strings.TrimSpace(plan)
	if trimmed == "" {
		return ""
	}
	// Collapse to lowercase alphanumerics so separators/casing don't matter.
	collapsed := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		default:
			return -1
		}
	}, trimmed)
	stripped := strings.TrimSuffix(strings.TrimPrefix(collapsed, "chatgpt"), "plan")
	if stripped == "" {
		// Degenerate input like "chatgpt" or "plan"; keep the collapsed form.
		return collapsed
	}
	return stripped
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
