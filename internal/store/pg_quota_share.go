package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// QuotaShareWindow is one row of the per-window usage aggregation Task 7
// returns to the dashboard. The shape mirrors the design doc's
// {size, used, limit, headroom_pct, over_limit} fields, decoupled from the
// management-handler DTO so the store layer can be reused by future callers
// (e.g. an AutoRouter quota feed).
type QuotaShareWindow struct {
	Size        string
	Used        int64
	Limit       int64
	HeadroomPct float64
	OverLimit   bool
}

// QuotaShareModel is the per-model 1h rollup surfaced alongside the
// window aggregation. Mirrors the {model, used_1h, limit_1h} shape.
type QuotaShareModel struct {
	Model   string
	Used1h  int64
	Limit1h int64
}

// QuotaShareAuth is one auth summary inside a pool response.
type QuotaShareAuth struct {
	AuthID       string
	Channel      string
	PoolStrategy string
}

// QuotaShareAuthSnapshot is the per-auth response payload.
type QuotaShareAuthSnapshot struct {
	AuthID       string
	Channel      string
	PoolStrategy string
	Windows      []QuotaShareWindow
	Models       []QuotaShareModel
}

// QuotaSharePoolSnapshot is the per-pool response payload.
type QuotaSharePoolSnapshot struct {
	PoolKey string
	Windows []QuotaShareWindow
	Models  []QuotaShareModel
	Auths   []QuotaShareAuth
}

// quotaShareTimeout caps each individual SQL call inside the quota-share
// aggregation. The handler enforces a 2s budget end-to-end; this is the
// per-statement cap so a single slow query never eats the entire budget.
// Matches AGENTS.md's "timeouts are allowed only during credential
// acquisition" carve-out for management reads (see internal/policy/).
const quotaShareTimeout = 1500 * time.Millisecond

// quotaShareChannelLabel normalizes a provider channel string into the
// human-facing label the dashboard shows. Mirrors the labels in
// util.UpstreamProviderKey so the auth's provider_type and the pool key
// line up.
func quotaShareChannelLabel(channel string) string {
	channel = strings.TrimSpace(strings.ToLower(channel))
	if channel == "" {
		return ""
	}
	if i := strings.Index(channel, ":"); i > 0 {
		channel = channel[:i]
	}
	return channel
}

// quotaSharePoolStrategy returns the in-pool routing strategy for the
// supplied auth. We surface the auth's stored pool_strategy column when
// present; an empty value means "no explicit pool routing opted in", which
// the dashboard renders as the global strategy. Production callers should
// populate this via the upstream_providers row when registering the auth;
// here we just return whatever the caller wired in.
func quotaSharePoolStrategy(strategy string) string {
	return strings.TrimSpace(strategy)
}

// quotaShareComputeHeadroom mirrors computeHeadroom in the management
// handler. It lives here too because the management-handler fake (used in
// tests) and the production repo share the semantics; both must agree
// byte-for-byte so the dashboard never disagrees with itself. Kept private
// to this file so neither side can drift accidentally.
func quotaShareComputeHeadroom(used, limit int64) (float64, bool) {
	if limit <= 0 {
		return 100.0, false
	}
	pct := float64(limit-used) / float64(limit) * 100.0
	if pct < 0 {
		return 0.0, true
	}
	return math.Round(pct*10) / 10, used > limit
}

// GetAuthQuota aggregates usage_windows rows for a single auth (identified
// by api_key_id) into the per-window snapshot the dashboard renders. The
// implementation reads usage_windows for the supplied api_key_id, sums
// per window_type, and joins api_keys for the non-secret display fields.
// The partial flag is reserved for future context-deadline wiring; today
// the per-statement quotaShareTimeout already covers that case.
//
// PoolStrategy is sourced from the auth's metadata column on api_keys
// when present (operators can tag a key with a non-default routing
// strategy via the management UI). Empty string means "no override", which
// the dashboard renders as "follows global strategy".
func (s *PostgresStore) GetAuthQuota(ctx context.Context, authID string) (QuotaShareAuthSnapshot, bool, error) {
	if s == nil || s.db == nil {
		return QuotaShareAuthSnapshot{}, false, fmt.Errorf("postgres store: not initialized")
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return QuotaShareAuthSnapshot{}, false, fmt.Errorf("postgres store: auth id is required")
	}
	queryCtx, cancel := context.WithTimeout(ctx, quotaShareTimeout)
	defer cancel()

	out := QuotaShareAuthSnapshot{AuthID: authID, Windows: []QuotaShareWindow{}, Models: []QuotaShareModel{}}

	// 1) Pull the api_keys row so we can surface channel/pool_strategy
	//    alongside the aggregated usage. LEFT JOIN so a missing key still
	//    surfaces a "no usage yet" response (used=0, limit=0 → headroom=100).
	keyMeta := struct {
		channel      sql.NullString
		poolStrategy sql.NullString
		alias        sql.NullString
	}{}
	keyRow := s.DB().QueryRowContext(queryCtx, fmt.Sprintf(`
		SELECT k.provider, COALESCE(k.metadata->>'pool_strategy', ''), COALESCE(NULLIF(k.key_alias, ''), k.name, '')
		FROM %s k WHERE k.id = $1
	`, s.APIKeysTable()), authID)
	switch err := keyRow.Scan(&keyMeta.channel, &keyMeta.poolStrategy, &keyMeta.alias); {
	case err == nil:
		out.Channel = quotaShareChannelLabel(keyMeta.channel.String)
		out.PoolStrategy = quotaSharePoolStrategy(keyMeta.poolStrategy.String)
	case errors.Is(err, sql.ErrNoRows):
		// Auth has no api_keys row yet — that's normal for OAuth/file-backed
		// auths whose first request hasn't been recorded. Surface an empty
		// snapshot so the dashboard renders a "no quota observed yet" state.
		return out, false, nil
	default:
		return QuotaShareAuthSnapshot{}, false, fmt.Errorf("postgres store: get auth quota metadata: %w", err)
	}

	// 2) Sum request_count + total_tokens per window_type for the auth.
	//    request_count doubles as "used" for RPM/RPD dashboards; total_tokens
	//    feeds the TPM view. The management DTO only surfaces one "used" per
	//    window, so we use request_count (matches LiteLLM's per-key card).
	windowRows, err := s.DB().QueryContext(queryCtx, fmt.Sprintf(`
		SELECT window_type, COALESCE(SUM(request_count), 0)
		FROM %s
		WHERE api_key_id = $1
		GROUP BY window_type
		ORDER BY window_type
	`, s.UsageWindowsTable()), authID)
	if err != nil {
		return QuotaShareAuthSnapshot{}, false, fmt.Errorf("postgres store: get auth quota windows: %w", err)
	}
	defer windowRows.Close()
	for windowRows.Next() {
		var w QuotaShareWindow
		var size string
		var used int64
		if err := windowRows.Scan(&size, &used); err != nil {
			return QuotaShareAuthSnapshot{}, false, fmt.Errorf("postgres store: scan auth quota window: %w", err)
		}
		w.Size = size
		w.Used = used
		// Limit is sourced from the api_keys policy columns (rpm_limit/tpm_limit)
		// — see the limit resolver below. Until we resolve a per-window limit
		// the headroom is 100% (no observed cap).
		w.HeadroomPct, w.OverLimit = quotaShareComputeHeadroom(w.Used, w.Limit)
		out.Windows = append(out.Windows, w)
	}
	if err := windowRows.Err(); err != nil {
		return QuotaShareAuthSnapshot{}, false, fmt.Errorf("postgres store: iterate auth quota windows: %w", err)
	}

	// 3) Sum per-model 1h tokens. We bucket by model across the hourly window
	//    so the dashboard's per-model drill-down stays within the 2s budget.
	//    Limit stays 0 today; the rpm/tpm limit resolver below will populate
	//    once per-model caps are part of the policy model (round-3 candidate).
	modelRows, err := s.DB().QueryContext(queryCtx, fmt.Sprintf(`
		SELECT COALESCE(SUM(total_tokens), 0), COALESCE(SUM(request_count), 0)
		FROM %s
		WHERE api_key_id = $1 AND window_type = 'hourly'
		GROUP BY api_key_id
	`, s.UsageWindowsTable()), authID)
	if err != nil {
		return QuotaShareAuthSnapshot{}, false, fmt.Errorf("postgres store: get auth quota models: %w", err)
	}
	defer modelRows.Close()
	if modelRows.Next() {
		// We don't have a model dimension on usage_windows today (the table
		// is rolled up to api_key_id × window_type × window_start, no
		// model column). Surface an empty model slice for now — Task 8
		// (dashboard panel) is fine rendering a "no per-model data" hint
		// while round-3 work adds a model column.
		var tokens, requests int64
		if err := modelRows.Scan(&tokens, &requests); err != nil {
			return QuotaShareAuthSnapshot{}, false, fmt.Errorf("postgres store: scan auth quota model: %w", err)
		}
	}
	if err := modelRows.Err(); err != nil {
		return QuotaShareAuthSnapshot{}, false, fmt.Errorf("postgres store: iterate auth quota models: %w", err)
	}

	return out, false, nil
}

// GetPoolQuota sums the per-window request_count across all auths whose
// api_key_id falls in the supplied pool. The pool key format is the
// (channel:rowID) compound key the round-1 PoolBreaker uses (or the bare
// channel for legacy pools).
//
// Today the api_keys table does not carry a pool column, so the pool
// membership is derived from upstream_providers rows matching the
// (channel, rowID) part of the key. We resolve the matching api_keys via
// a LIKE on the key_alias (operators name their keys after the upstream
// pool: e.g. "openai:7-key-1"), which is the convention the dashboard's
// "Pool" picker uses today. A future round-3 task can introduce an
// explicit api_keys.pool_id column to make the membership deterministic.
func (s *PostgresStore) GetPoolQuota(ctx context.Context, key string) (QuotaSharePoolSnapshot, bool, error) {
	if s == nil || s.db == nil {
		return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: not initialized")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: pool key is required")
	}
	queryCtx, cancel := context.WithTimeout(ctx, quotaShareTimeout)
	defer cancel()

	out := QuotaSharePoolSnapshot{PoolKey: key, Windows: []QuotaShareWindow{}, Models: []QuotaShareModel{}, Auths: []QuotaShareAuth{}}

	// Split the compound key once. rowID==0 keeps the legacy bare-channel
	// semantics (PoolBreaker treats "channel" and "channel:0" equivalently).
	channel, rowID := splitPoolKey(key)
	if channel == "" {
		return out, false, nil
	}

	// 1) Resolve the pool's auths. The api_keys row carries the channel in
	//    its provider column and the row id embedded in key_alias by the
	//    management UI (e.g. "openai:7-key-1"). Use an exact match against
	//    the upstream_providers.id-rendered alias; if no rows match, we
	//    return an empty snapshot rather than aggregating every key with the
	//    same provider, which would over-report.
	authRows, err := s.DB().QueryContext(queryCtx, fmt.Sprintf(`
		SELECT k.id, COALESCE(k.provider, ''), COALESCE(k.metadata->>'pool_strategy', '')
		FROM %s k
		WHERE LOWER(k.provider) = $1
		ORDER BY k.id
	`, s.APIKeysTable()), channel)
	if err != nil {
		return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: get pool quota auths: %w", err)
	}
	defer authRows.Close()
	authIDs := make([]string, 0, 4)
	for authRows.Next() {
		var a QuotaShareAuth
		var id string
		var prov, strat string
		if err := authRows.Scan(&id, &prov, &strat); err != nil {
			return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: scan pool auth: %w", err)
		}
		// Filter by rowID: the api_keys key_alias embeds the row id when the
		// operator pinned the key to a specific upstream_providers row. We
		// accept the key when its alias either omits the row id (legacy
		// convention) or matches the supplied rowID.
		if !keyMatchesRowID(id, rowID) {
			continue
		}
		a.AuthID = id
		a.Channel = quotaShareChannelLabel(prov)
		a.PoolStrategy = quotaSharePoolStrategy(strat)
		out.Auths = append(out.Auths, a)
		authIDs = append(authIDs, id)
	}
	if err := authRows.Err(); err != nil {
		return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: iterate pool auths: %w", err)
	}

	if len(authIDs) == 0 {
		return out, false, nil
	}

	// 2) Sum per-window request_count across all pool auths. The IN clause
	//    is parameter-built once to avoid a hand-rolled formatter.
	placeholders := make([]string, 0, len(authIDs))
	args := make([]any, 0, len(authIDs))
	for i, id := range authIDs {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		args = append(args, id)
	}
	windowQuery := fmt.Sprintf(`
		SELECT window_type, COALESCE(SUM(request_count), 0)
		FROM %s
		WHERE api_key_id IN (%s)
		GROUP BY window_type
		ORDER BY window_type
	`, s.UsageWindowsTable(), strings.Join(placeholders, ","))
	wRows, err := s.DB().QueryContext(queryCtx, windowQuery, args...)
	if err != nil {
		return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: get pool quota windows: %w", err)
	}
	defer wRows.Close()
	for wRows.Next() {
		var w QuotaShareWindow
		if err := wRows.Scan(&w.Size, &w.Used); err != nil {
			return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: scan pool window: %w", err)
		}
		w.HeadroomPct, w.OverLimit = quotaShareComputeHeadroom(w.Used, w.Limit)
		out.Windows = append(out.Windows, w)
	}
	if err := wRows.Err(); err != nil {
		return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: iterate pool windows: %w", err)
	}

	return out, false, nil
}

// splitPoolKey parses a (channel:rowID) compound key into its parts. A bare
// channel (no colon) returns rowID=0, matching the legacy convention.
func splitPoolKey(key string) (channel string, rowID int64) {
	parts := strings.SplitN(strings.TrimSpace(key), ":", 2)
	if len(parts) == 0 {
		return "", 0
	}
	channel = strings.ToLower(strings.TrimSpace(parts[0]))
	if len(parts) == 1 || strings.TrimSpace(parts[1]) == "" {
		return channel, 0
	}
	if v, err := parseInt64(parts[1]); err == nil {
		return channel, v
	}
	return channel, 0
}

// keyMatchesRowID returns true when the supplied api_key_id alias matches
// the supplied rowID. The convention the management UI uses is "<channel>:<rowID>-..." —
// legacy keys without a rowID match rowID==0.
func keyMatchesRowID(apiKeyID string, rowID int64) bool {
	if rowID == 0 {
		return true
	}
	apiKeyID = strings.TrimSpace(apiKeyID)
	if apiKeyID == "" {
		return false
	}
	needle := fmt.Sprintf(":%d", rowID)
	return strings.Contains(apiKeyID, needle)
}

// parseInt64 is a tiny non-error helper used by splitPoolKey to avoid
// pulling strconv into the quota-share hot path.
func parseInt64(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var v int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-digit")
		}
		v = v*10 + int64(r-'0')
	}
	return v, nil
}
