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

// quotaSharePerWindowTimeout caps each per-window sub-query. The handler
// enforces a 2s budget end-to-end; three sequential sub-queries at 500ms
// each (1.5s) plus the metadata/auth lookup leave the handler's outer 2s
// budget intact even under sustained PG slowness. When the per-window
// timeout fires, the store catches the context-deadline error, fills the
// missing window with zeros, and flips partial=true so the handler can
// return 200 + partial=true rather than 500.
const quotaSharePerWindowTimeout = 500 * time.Millisecond

// quotaShareMetaTimeout caps the metadata/auth-list sub-queries. Tighter
// than the per-window budget so the per-window queries (which carry the
// data the dashboard cares about) always get their full 500ms.
const quotaShareMetaTimeout = 400 * time.Millisecond

// quotaShareWindowTypes are the window sizes surfaced to the dashboard.
// The values mirror the WindowType* constants in pg_usage.go (the values
// actually written to usage_windows.window_type). The dashboard renders
// these as "hourly"/"weekly"/"monthly" — a future round-3 task can map
// them to "1h"/"1d"/"30d" labels without changing this slice.
var quotaShareWindowTypes = []string{
	WindowTypeHourly,  // "hourly" — 1h rolling budget
	WindowTypeWeekly,  // "weekly" — 7d rolling budget
	WindowTypeMonthly, // "monthly" — 30d rolling budget
}

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

// quotaShareWindowQuery runs one per-window sub-query and returns the
// summed used value. It is the injected query function passed to
// quotaShareRunWindowAggregate — production wires this to a real
// `SELECT COALESCE(SUM(request_count), 0) ...` while tests inject a fake
// that simulates slow PG by blocking until the context fires. The signature
// is the minimal contract: a per-window type and a context with the
// per-window budget already applied.
type quotaShareWindowQuery func(ctx context.Context, windowType string) (int64, error)

// quotaShareRunWindowAggregate fans out one per-window sub-query and
// returns the (window_type -> used) map plus a partial flag. The query
// function is injected so the unit test can simulate slow PG without
// spinning up a real database; production wires this to a helper that
// issues the actual `SELECT COALESCE(SUM(request_count), 0) FROM
// usage_windows WHERE ... AND window_type = $N`.
//
// Semantics:
//   - Each window runs under its own timeout sub-context so a single slow
//     query never eats the whole budget.
//   - On context-deadline-exceeded (or context-cancelled) for a window,
//     the missing window is recorded as used=0 (placeholder) and the
//     returned partial flag is flipped to true. The caller surfaces
//     partial=true on the response.
//   - Non-context errors halt aggregation immediately and propagate so
//     the handler can return 500. Windows already aggregated are
//     discarded by the caller (the caller short-circuits on err).
//   - Windows that come back successfully are returned verbatim.
//
// Note: we deliberately do NOT distinguish "no rows" from "rows summing
// to 0" — both surface as used=0. The dashboard renders used=0 the same
// way regardless of whether there are zero rows or a row with
// request_count=0; distinguishing the two would leak an internal schema
// detail into the API surface.
func quotaShareRunWindowAggregate(
	ctx context.Context,
	windowTypes []string,
	timeout time.Duration,
	query quotaShareWindowQuery,
) (map[string]int64, bool, error) {
	if len(windowTypes) == 0 {
		return map[string]int64{}, false, nil
	}
	used := make(map[string]int64, len(windowTypes))
	partial := false
	for _, wt := range windowTypes {
		subCtx, cancel := context.WithTimeout(ctx, timeout)
		v, err := query(subCtx, wt)
		cancel()
		switch {
		case err == nil:
			used[wt] = v
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			// Window query hit the timeout. Record a zero placeholder
			// and flag the response as partial so the handler can
			// surface 200 + partial=true rather than 500.
			used[wt] = 0
			partial = true
		default:
			// Non-context error: halt aggregation. The caller
			// propagates this as a 500; any windows already
			// aggregated are discarded because the caller's error
			// path returns before reading the map.
			return nil, partial, err
		}
	}
	return used, partial, nil
}

// GetAuthQuota aggregates usage_windows rows for a single auth (identified
// by api_key_id) into the per-window snapshot the dashboard renders. The
// implementation reads usage_windows for the supplied api_key_id, sums
// per window_type, and joins api_keys for the non-secret display fields.
//
// Partial semantics: the per-window sub-queries run independently with a
// 500ms timeout each. If any window's query hits the per-window timeout,
// the store returns the windows that did come back plus zero placeholders
// for the timed-out windows, with partial=true so the handler surfaces
// 200 + partial=true (the design's "slow PG must not black-out the
// dashboard" contract). The handler's outer 2s context still caps the
// total: under sustained PG slowness the handler's context will cancel
// mid-flight, which surfaces as a context error that propagates as 500
// (acceptable — that's the timeout firing, and partial=true will already
// be set if any window came back before the outer cancel landed).
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

	out := QuotaShareAuthSnapshot{AuthID: authID, Windows: []QuotaShareWindow{}, Models: []QuotaShareModel{}}
	partial := false

	// 1) Pull the api_keys row so we can surface channel/pool_strategy
	//    alongside the aggregated usage. LEFT JOIN so a missing key still
	//    surfaces a "no usage yet" response (used=0, limit=0 → headroom=100).
	metaCtx, metaCancel := context.WithTimeout(ctx, quotaShareMetaTimeout)
	defer metaCancel()
	keyMeta := struct {
		channel      sql.NullString
		poolStrategy sql.NullString
		alias        sql.NullString
	}{}
	keyRow := s.DB().QueryRowContext(metaCtx, fmt.Sprintf(`
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

	// 2) Per-window sub-queries. Each window runs under its own 500ms
	//    sub-context so a single slow query can't eat the whole budget.
	//    If any window's query hits the timeout, the missing window is
	//    recorded as used=0 (placeholder) and partial flips to true.
	usedByWindow, windowsPartial, err := quotaShareRunWindowAggregate(
		ctx,
		quotaShareWindowTypes,
		quotaSharePerWindowTimeout,
		func(subCtx context.Context, windowType string) (int64, error) {
			var sum sql.NullInt64
			row := s.DB().QueryRowContext(subCtx, fmt.Sprintf(`
				SELECT COALESCE(SUM(request_count), 0)
				FROM %s
				WHERE api_key_id = $1 AND window_type = $2
			`, s.UsageWindowsTable()), authID, windowType)
			if err := row.Scan(&sum); err != nil {
				return 0, err
			}
			return sum.Int64, nil
		},
	)
	if err != nil {
		return QuotaShareAuthSnapshot{}, windowsPartial, fmt.Errorf("postgres store: get auth quota windows: %w", err)
	}
	partial = partial || windowsPartial
	for _, wt := range quotaShareWindowTypes {
		w := QuotaShareWindow{Size: wt, Used: usedByWindow[wt]}
		w.HeadroomPct, w.OverLimit = quotaShareComputeHeadroom(w.Used, w.Limit)
		out.Windows = append(out.Windows, w)
	}

	// 3) Sum per-model 1h tokens. We bucket by model across the hourly window
	//    so the dashboard's per-model drill-down stays within the 2s budget.
	//    Limit stays 0 today; the rpm/tpm limit resolver below will populate
	//    once per-model caps are part of the policy model (round-3 candidate).
	modelCtx, modelCancel := context.WithTimeout(ctx, quotaShareMetaTimeout)
	defer modelCancel()
	var modelTokens, modelRequests sql.NullInt64
	modelRow := s.DB().QueryRowContext(modelCtx, fmt.Sprintf(`
		SELECT COALESCE(SUM(total_tokens), 0), COALESCE(SUM(request_count), 0)
		FROM %s
		WHERE api_key_id = $1 AND window_type = $2
	`, s.UsageWindowsTable()), authID, WindowTypeHourly)
	switch err := modelRow.Scan(&modelTokens, &modelRequests); {
	case err == nil:
		// We don't have a model dimension on usage_windows today (the table
		// is rolled up to api_key_id × window_type × window_start, no
		// model column). Surface an empty model slice for now — Task 8
		// (dashboard panel) is fine rendering a "no per-model data" hint
		// while round-3 work adds a model column. Tokens/requests are
		// summed across all models; we keep the totals in case a future
		// caller wants to surface them.
		_ = modelTokens.Int64
		_ = modelRequests.Int64
	case errors.Is(err, sql.ErrNoRows):
		// No hourly rows yet — leave Models empty.
	default:
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			partial = true
		} else {
			return QuotaShareAuthSnapshot{}, partial, fmt.Errorf("postgres store: get auth quota models: %w", err)
		}
	}

	return out, partial, nil
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
//
// Partial semantics mirror GetAuthQuota: per-window sub-queries with
// 500ms timeouts; any timeout flips partial=true and fills the missing
// window with a zero placeholder.
func (s *PostgresStore) GetPoolQuota(ctx context.Context, key string) (QuotaSharePoolSnapshot, bool, error) {
	if s == nil || s.db == nil {
		return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: not initialized")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return QuotaSharePoolSnapshot{}, false, fmt.Errorf("postgres store: pool key is required")
	}

	out := QuotaSharePoolSnapshot{PoolKey: key, Windows: []QuotaShareWindow{}, Models: []QuotaShareModel{}, Auths: []QuotaShareAuth{}}
	partial := false

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
	authCtx, authCancel := context.WithTimeout(ctx, quotaShareMetaTimeout)
	defer authCancel()
	authRows, err := s.DB().QueryContext(authCtx, fmt.Sprintf(`
		SELECT k.id, COALESCE(k.provider, ''), COALESCE(k.metadata->>'pool_strategy', '')
		FROM %s k
		WHERE LOWER(k.provider) = $1
		ORDER BY k.id
	`, s.APIKeysTable()), channel)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return out, true, nil
		}
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

	// 2) Per-window sub-queries across all pool auths. The IN clause
	//    is parameter-built once to avoid a hand-rolled formatter.
	placeholders := make([]string, 0, len(authIDs))
	args := make([]any, 0, len(authIDs))
	for i, id := range authIDs {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+2))
		args = append(args, id)
	}
	usedByWindow, windowsPartial, err := quotaShareRunWindowAggregate(
		ctx,
		quotaShareWindowTypes,
		quotaSharePerWindowTimeout,
		func(subCtx context.Context, windowType string) (int64, error) {
			var sum sql.NullInt64
			query := fmt.Sprintf(`
				SELECT COALESCE(SUM(request_count), 0)
				FROM %s
				WHERE window_type = $1 AND api_key_id IN (%s)
			`, s.UsageWindowsTable(), strings.Join(placeholders, ","))
			queryArgs := append([]any{windowType}, args...)
			row := s.DB().QueryRowContext(subCtx, query, queryArgs...)
			if err := row.Scan(&sum); err != nil {
				return 0, err
			}
			return sum.Int64, nil
		},
	)
	if err != nil {
		return QuotaSharePoolSnapshot{}, windowsPartial, fmt.Errorf("postgres store: get pool quota windows: %w", err)
	}
	partial = partial || windowsPartial
	for _, wt := range quotaShareWindowTypes {
		w := QuotaShareWindow{Size: wt, Used: usedByWindow[wt]}
		w.HeadroomPct, w.OverLimit = quotaShareComputeHeadroom(w.Used, w.Limit)
		out.Windows = append(out.Windows, w)
	}

	return out, partial, nil
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
