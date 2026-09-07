package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
)

// ErrAutoRouterProfileNotFound is returned only when a profile lookup is
// explicitly requested from a store that has no router row. Runtime lookups
// use the built-in profile when the profile row itself is absent.
var ErrAutoRouterProfileNotFound = errors.New("postgres store: auto router profile not found")

// AutoRouterProfileStore persists one active scoring profile per Auto Router.
type AutoRouterProfileStore struct {
	db    *sql.DB
	table string

	cacheMu sync.RWMutex
	cache   map[string]*autorouter.Profile
	// compiled caches the compiled form of each cached profile, keyed like
	// cache. CompileProfile work happens once per profile version instead of
	// once per request; invalidate() clears both maps together.
	compiled map[string]*autorouter.CompiledProfile
}

// NewAutoRouterProfileStore builds a profile store from a PostgresStore.
func NewAutoRouterProfileStore(parent *PostgresStore) *AutoRouterProfileStore {
	if parent == nil {
		return nil
	}
	return &AutoRouterProfileStore{
		db:       parent.DB(),
		table:    parent.AutoRouterProfilesTable(),
		cache:    map[string]*autorouter.Profile{},
		compiled: map[string]*autorouter.CompiledProfile{},
	}
}

// Compiled returns the compiled form of the active profile for routerID. A
// missing profile row compiles the built-in defaults. Compile results are
// cached until invalidated, so the per-request path skips all normalization.
func (s *AutoRouterProfileStore) Compiled(ctx context.Context, routerID string) (*autorouter.CompiledProfile, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: auto router profile store not initialized")
	}
	routerID = strings.ToLower(strings.TrimSpace(routerID))
	if routerID == "" {
		return nil, ErrAutoRouterProfileNotFound
	}
	if compiled := s.cachedCompiled(routerID); compiled != nil {
		return compiled, nil
	}
	// Reuse the raw cache/Get path so compilation rides on the same
	// invalidation guarantees.
	if _, err := s.Get(ctx, routerID); err != nil {
		return nil, err
	}
	if compiled := s.cachedCompiled(routerID); compiled != nil {
		return compiled, nil
	}
	// Get cached the raw profile; compile and store it now.
	profile, err := s.cached(routerID), error(nil)
	if profile == nil {
		return nil, ErrAutoRouterProfileNotFound
	}
	compiled, err := autorouter.CompileProfile(profile.ProfileConfig)
	if err != nil {
		return nil, fmt.Errorf("postgres store: compile auto router profile: %w", err)
	}
	compiled.Version = profile.ProfileVersion
	s.cacheCompiled(routerID, &compiled)
	return &compiled, nil
}

// Get returns the active profile for routerID. A missing profile row uses the
// built-in defaults so existing routers remain requestable during migration.
func (s *AutoRouterProfileStore) Get(ctx context.Context, routerID string) (autorouter.Profile, error) {
	if s == nil || s.db == nil {
		return autorouter.Profile{}, fmt.Errorf("postgres store: auto router profile store not initialized")
	}
	routerID = strings.TrimSpace(routerID)
	if routerID == "" {
		return autorouter.Profile{}, ErrAutoRouterProfileNotFound
	}
	if cached := s.cached(routerID); cached != nil {
		return cloneProfile(*cached), nil
	}
	var (
		version       int64
		hash          string
		thresholdsRaw []byte
		weightsRaw    []byte
		keywordsRaw   []byte
	)
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT profile_version, profile_hash, thresholds, weights, keyword_tier_rules
		FROM %s WHERE router_id = $1
	`, s.table), routerID).Scan(&version, &hash, &thresholdsRaw, &weightsRaw, &keywordsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		profile := autorouter.DefaultProfile()
		s.cacheProfile(routerID, &profile)
		return profile, nil
	}
	if err != nil {
		return autorouter.Profile{}, fmt.Errorf("postgres store: get auto router profile: %w", err)
	}
	config, err := decodeProfileConfig(thresholdsRaw, weightsRaw, keywordsRaw)
	if err != nil {
		return autorouter.Profile{}, err
	}
	profile := autorouter.Profile{ProfileConfig: config, ProfileVersion: version, ProfileHash: strings.TrimSpace(hash)}
	if profile.ProfileHash == "" {
		profile.ProfileHash, err = autorouter.ProfileHash(config)
		if err != nil {
			return autorouter.Profile{}, err
		}
	}
	s.cacheProfile(routerID, &profile)
	return profile, nil
}

// Upsert replaces the active profile and increments its version. The returned
// profile contains the canonical configuration, new version, and full hash.
func (s *AutoRouterProfileStore) Upsert(ctx context.Context, routerID string, config autorouter.ProfileConfig) (autorouter.Profile, error) {
	if s == nil || s.db == nil {
		return autorouter.Profile{}, fmt.Errorf("postgres store: auto router profile store not initialized")
	}
	routerID = strings.TrimSpace(routerID)
	if routerID == "" {
		return autorouter.Profile{}, ErrAutoRouterProfileNotFound
	}
	normalized, err := autorouter.NormalizeProfile(config)
	if err != nil {
		return autorouter.Profile{}, err
	}
	hash, err := autorouter.ProfileHash(normalized)
	if err != nil {
		return autorouter.Profile{}, err
	}
	thresholdsJSON, _ := json.Marshal(normalized.Thresholds)
	weightsJSON, _ := json.Marshal(normalized.Weights)
	keywordsJSON, _ := json.Marshal(normalized.KeywordTierRules)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return autorouter.Profile{}, fmt.Errorf("postgres store: begin auto router profile update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int64
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COALESCE(profile_version, 0) FROM %s WHERE router_id = $1 FOR UPDATE
	`, s.table), routerID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		version = 0
	} else if err != nil {
		return autorouter.Profile{}, fmt.Errorf("postgres store: lock auto router profile: %w", err)
	}
	version++
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (router_id, profile_version, profile_hash, thresholds, weights, keyword_tier_rules, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, NOW())
		ON CONFLICT (router_id) DO UPDATE SET
			profile_version = EXCLUDED.profile_version,
			profile_hash = EXCLUDED.profile_hash,
			thresholds = EXCLUDED.thresholds,
			weights = EXCLUDED.weights,
			keyword_tier_rules = EXCLUDED.keyword_tier_rules,
			updated_at = NOW()
	`, s.table), routerID, version, hash, string(thresholdsJSON), string(weightsJSON), string(keywordsJSON))
	if err != nil {
		return autorouter.Profile{}, fmt.Errorf("postgres store: upsert auto router profile: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return autorouter.Profile{}, fmt.Errorf("postgres store: commit auto router profile: %w", err)
	}
	profile := autorouter.Profile{ProfileConfig: normalized, ProfileVersion: version, ProfileHash: hash}
	s.cacheProfile(routerID, &profile)
	return profile, nil
}

// EnsureDefault inserts the built-in profile when the router has no profile.
// It is idempotent and does not overwrite operator configuration.
func (s *AutoRouterProfileStore) EnsureDefault(ctx context.Context, routerID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: auto router profile store not initialized")
	}
	profile := autorouter.DefaultProfile()
	thresholdsJSON, _ := json.Marshal(profile.Thresholds)
	weightsJSON, _ := json.Marshal(profile.Weights)
	keywordsJSON, _ := json.Marshal(profile.KeywordTierRules)
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (router_id, profile_version, profile_hash, thresholds, weights, keyword_tier_rules)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb)
		ON CONFLICT (router_id) DO NOTHING
	`, s.table), strings.TrimSpace(routerID), profile.ProfileVersion, profile.ProfileHash,
		string(thresholdsJSON), string(weightsJSON), string(keywordsJSON))
	if err != nil {
		return fmt.Errorf("postgres store: ensure auto router profile: %w", err)
	}
	s.invalidate(routerID)
	return nil
}

// Invalidate clears the cached profile for a router after an external update.
func (s *AutoRouterProfileStore) Invalidate(routerID string) {
	s.invalidate(routerID)
}

func decodeProfileConfig(thresholdsRaw, weightsRaw, keywordsRaw []byte) (autorouter.ProfileConfig, error) {
	var config autorouter.ProfileConfig
	if err := json.Unmarshal(thresholdsRaw, &config.Thresholds); err != nil {
		return autorouter.ProfileConfig{}, fmt.Errorf("postgres store: decode profile thresholds: %w", err)
	}
	if err := json.Unmarshal(weightsRaw, &config.Weights); err != nil {
		return autorouter.ProfileConfig{}, fmt.Errorf("postgres store: decode profile weights: %w", err)
	}
	if len(keywordsRaw) == 0 {
		config.KeywordTierRules = []autorouter.KeywordTierRule{}
	} else if err := json.Unmarshal(keywordsRaw, &config.KeywordTierRules); err != nil {
		return autorouter.ProfileConfig{}, fmt.Errorf("postgres store: decode profile keyword rules: %w", err)
	}
	normalized, err := autorouter.NormalizeProfile(config)
	if err != nil {
		return autorouter.ProfileConfig{}, fmt.Errorf("postgres store: validate stored auto router profile: %w", err)
	}
	return normalized, nil
}

func (s *AutoRouterProfileStore) cached(routerID string) *autorouter.Profile {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	if profile := s.cache[strings.ToLower(routerID)]; profile != nil {
		copy := cloneProfile(*profile)
		return &copy
	}
	return nil
}

func (s *AutoRouterProfileStore) cacheProfile(routerID string, profile *autorouter.Profile) {
	if s == nil || profile == nil {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cache[strings.ToLower(strings.TrimSpace(routerID))] = cloneProfilePtr(profile)
}

func (s *AutoRouterProfileStore) invalidate(routerID string) {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if strings.TrimSpace(routerID) == "" {
		s.cache = map[string]*autorouter.Profile{}
		s.compiled = map[string]*autorouter.CompiledProfile{}
		return
	}
	key := strings.ToLower(strings.TrimSpace(routerID))
	delete(s.cache, key)
	delete(s.compiled, key)
}

func (s *AutoRouterProfileStore) cachedCompiled(routerID string) *autorouter.CompiledProfile {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	return s.compiled[routerID]
}

func (s *AutoRouterProfileStore) cacheCompiled(routerID string, compiled *autorouter.CompiledProfile) {
	if s == nil || compiled == nil {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.compiled[routerID] = compiled
}

func cloneProfile(profile autorouter.Profile) autorouter.Profile {
	profile.ProfileConfig = cloneProfileConfig(profile.ProfileConfig)
	return profile
}

func cloneProfilePtr(profile *autorouter.Profile) *autorouter.Profile {
	if profile == nil {
		return nil
	}
	copy := cloneProfile(*profile)
	return &copy
}

func cloneProfileConfig(config autorouter.ProfileConfig) autorouter.ProfileConfig {
	weights := make(map[autorouter.ScoreField]float64, len(config.Weights))
	for field, weight := range config.Weights {
		weights[field] = weight
	}
	rules := make([]autorouter.KeywordTierRule, len(config.KeywordTierRules))
	for i, rule := range config.KeywordTierRules {
		rules[i] = rule
		rules[i].Keywords = append([]string(nil), rule.Keywords...)
	}
	return autorouter.ProfileConfig{Thresholds: config.Thresholds, Weights: weights, KeywordTierRules: rules}
}
