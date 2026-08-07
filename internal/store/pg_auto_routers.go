package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrAutoRouterNotFound is returned when no Auto Router matches the supplied
// identifier or model id.
var ErrAutoRouterNotFound = errors.New("postgres store: auto router not found")

// ErrAutoRouterNameTaken is returned on Create/Update when the requested name
// is already in use by another router. Names are unique per auto_routers.name.
var ErrAutoRouterNameTaken = errors.New("postgres store: auto router name already taken")

// ErrAutoRouterModelIDTaken is returned on Create/Update when the requested
// model id is already in use by another router. Model ids are unique per
// auto_routers.model_id (a client-facing id, e.g. "router:smart-router").
var ErrAutoRouterModelIDTaken = errors.New("postgres store: auto router model id already taken")

// TierMapping maps a single complexity tier to a concrete upstream target plus
// its per-model routing, mirroring a Model Route (providers + strategy +
// priorities) exactly like Models Group. When Model is empty the tier is
// unresolved and resolution falls back to a lower tier.
type TierMapping struct {
	Tier  string `json:"tier" yaml:"tier"`
	Model string `json:"model,omitempty" yaml:"model,omitempty"`
	// Providers pins the tier's target model to a subset of upstream provider
	// keys. Empty inherits the model's default providers.
	Providers []string `json:"providers,omitempty" yaml:"providers,omitempty"`
	// Strategy optionally overrides the routing strategy ("priority"/"failover").
	// Empty inherits the global routing strategy.
	Strategy string `json:"strategy,omitempty" yaml:"strategy,omitempty"`
	// Priorities optionally assigns a priority weight per pinned provider
	// (higher = primary). Only providers also in Providers are honored.
	Priorities []ProviderPriority `json:"priorities,omitempty" yaml:"priorities,omitempty"`
}

// AutoRouterPricing carries optional model-style pricing metadata for the
// router entity (display only; runtime cost attribution uses the resolved
// target model's pricing). Units are USD per 1M tokens. The cached fields
// mirror the general model pricing convention: CachedInputPer1M is the
// cache-creation (write) price, and CachedReadPer1M is the cache-read ("prompt
// caching") discount price billed for tokens served from an exact prompt-cache
// match.
type AutoRouterPricing struct {
	InputPer1M       float64 `json:"input_per_1m_usd,omitempty"`
	OutputPer1M      float64 `json:"output_per_1m_usd,omitempty"`
	CachedInputPer1M float64 `json:"cached_input_per_1m_usd,omitempty"`
	CachedReadPer1M  float64 `json:"cached_read_per_1m_usd,omitempty"`
	ReasoningPer1M   float64 `json:"reasoning_per_1m_usd,omitempty"`
}

// AutoRouter is the persisted definition of a single Auto Router. It acts as a
// "global model": it exposes a requestable model_id plus name/display/pricing
// metadata, and maps each complexity tier to a concrete upstream target model.
type AutoRouter struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	ModelID     string             `json:"model_id"`
	Description string             `json:"description,omitempty"`
	DisplayName string             `json:"display_name,omitempty"`
	Mappings    []TierMapping      `json:"mappings,omitempty"`
	Pricing     *AutoRouterPricing `json:"pricing,omitempty"`
	// VisionBridgeModel is an optional per-router model used to analyze images
	// when a tier's resolved target model does not support vision. Empty = the
	// vision bridge is disabled for this router.
	VisionBridgeModel string         `json:"vision_bridge_model,omitempty"`
	Enabled           bool           `json:"enabled"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

// AutoRouterStore provides CRUD and runtime lookups for Auto Router
// definitions. Backed by the same *sql.DB connection as PostgresStore.
type AutoRouterStore struct {
	db         *sql.DB
	table      string
	cacheMu    sync.RWMutex
	modelCache map[string]*AutoRouter // model_id -> router
}

// NewAutoRouterStore builds an AutoRouterStore that reuses the PostgresStore
// connection and table name. Returns nil when the parent store is nil so
// feature-detection is a single nil check.
func NewAutoRouterStore(parent *PostgresStore) *AutoRouterStore {
	if parent == nil {
		return nil
	}
	return &AutoRouterStore{
		db:         parent.DB(),
		table:      parent.AutoRoutersTable(),
		modelCache: map[string]*AutoRouter{},
	}
}

const autoRouterColumns = "id, name, model_id, COALESCE(description, ''), COALESCE(display_name, ''), tier_mappings, pricing, COALESCE(vision_bridge_model, ''), enabled, metadata, created_at, updated_at"

// Create inserts a new Auto Router. ID is generated when empty. Names and model
// ids must be unique; a conflict surfaces ErrAutoRouterNameTaken /
// ErrAutoRouterModelIDTaken.
func (s *AutoRouterStore) Create(ctx context.Context, r AutoRouter) (AutoRouter, error) {
	if s == nil || s.db == nil {
		return AutoRouter{}, fmt.Errorf("postgres store: auto router store not initialized")
	}
	r.Name = strings.TrimSpace(r.Name)
	r.ModelID = strings.TrimSpace(r.ModelID)
	if r.Name == "" {
		return AutoRouter{}, fmt.Errorf("postgres store: auto router name is required")
	}
	if r.ModelID == "" {
		return AutoRouter{}, fmt.Errorf("postgres store: auto router model id is required")
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	r.Description = strings.TrimSpace(r.Description)
	r.DisplayName = strings.TrimSpace(r.DisplayName)
	if r.Mappings == nil {
		r.Mappings = []TierMapping{}
	}
	if r.Metadata == nil {
		r.Metadata = map[string]any{}
	}

	mappingsJSON, _ := json.Marshal(r.Mappings)
	pricingJSON := "null"
	if r.Pricing != nil {
		pj, _ := json.Marshal(r.Pricing)
		pricingJSON = string(pj)
	}
	metaJSON, _ := json.Marshal(r.Metadata)

	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, name, model_id, description, display_name, tier_mappings, pricing, vision_bridge_model, enabled, metadata)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10::jsonb)
	`, s.table),
		r.ID, r.Name, r.ModelID, nullableString(r.Description), nullableString(r.DisplayName),
		string(mappingsJSON), pricingJSON, nullableString(strings.TrimSpace(r.VisionBridgeModel)), r.Enabled, string(metaJSON),
	); err != nil {
		return AutoRouter{}, mapAutoRouterWriteError(err, r.Name, r.ModelID)
	}
	return s.Get(ctx, r.ID)
}

// Get returns a single Auto Router by id.
func (s *AutoRouterStore) Get(ctx context.Context, id string) (AutoRouter, error) {
	if s == nil || s.db == nil {
		return AutoRouter{}, fmt.Errorf("postgres store: auto router store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT %s FROM %s WHERE id = $1
	`, autoRouterColumns, s.table), id)
	r, err := scanAutoRouter(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AutoRouter{}, ErrAutoRouterNotFound
		}
		return AutoRouter{}, err
	}
	return r, nil
}

// GetByModelID returns the Auto Router that exposes the given client-facing
// model id. Used by the request-time resolver to locate a router from the
// requested model. Returns ErrAutoRouterNotFound when no router matches.
func (s *AutoRouterStore) GetByModelID(ctx context.Context, modelID string) (AutoRouter, error) {
	if s == nil || s.db == nil {
		return AutoRouter{}, fmt.Errorf("postgres store: auto router store not initialized")
	}
	if cached := s.cachedByModel(modelID); cached != nil {
		return *cached, nil
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT %s FROM %s WHERE LOWER(model_id) = LOWER($1)
	`, autoRouterColumns, s.table), modelID)
	r, err := scanAutoRouter(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AutoRouter{}, ErrAutoRouterNotFound
		}
		return AutoRouter{}, err
	}
	s.cacheByModel(modelID, &r)
	return r, nil
}

// AutoRouterListFilter narrows a List query.
type AutoRouterListFilter struct {
	Search    string // case-insensitive substring match on name OR model_id
	Page      int
	PageSize  int
	SortBy    string // "name" (default) | "model_id" | "created_at" | "updated_at"
	SortOrder string // "asc" (default) | "desc"
}

// ListPaged returns a page of Auto Routers plus the total row count.
func (s *AutoRouterStore) ListPaged(ctx context.Context, f AutoRouterListFilter) ([]AutoRouter, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: auto router store not initialized")
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize <= 0 {
		f.PageSize = 25
	}
	if f.PageSize > 200 {
		f.PageSize = 200
	}
	sortCol, err := autoRouterSortColumn(f.SortBy)
	if err != nil {
		return nil, 0, err
	}
	sortOrder := "ASC"
	if strings.EqualFold(f.SortOrder, "desc") {
		sortOrder = "DESC"
	}

	where := " WHERE 1=1"
	args := []any{}
	if search := strings.TrimSpace(f.Search); search != "" {
		args = append(args, "%"+search+"%", "%"+search+"%")
		where += fmt.Sprintf(" AND (name ILIKE $%d OR model_id ILIKE $%d)", len(args)-1, len(args))
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s r%s`, s.table, where), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count auto routers: %w", err)
	}

	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	query := fmt.Sprintf(`
		SELECT %s FROM %s r%s
		ORDER BY %s %s
		LIMIT $%d OFFSET $%d
	`, autoRouterColumns, s.table, where, sortCol, sortOrder, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list auto routers: %w", err)
	}
	defer rows.Close()
	out := make([]AutoRouter, 0, f.PageSize)
	for rows.Next() {
		r, errScan := scanAutoRouter(rows)
		if errScan != nil {
			return nil, 0, fmt.Errorf("postgres store: scan auto router row: %w", errScan)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ListAll returns every Auto Router (unpaginated). Used at boot to restore the
// in-memory/catalog registrations for all routers after a restart.
func (s *AutoRouterStore) ListAll(ctx context.Context) ([]AutoRouter, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: auto router store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT %s FROM %s ORDER BY created_at`, autoRouterColumns, s.table,
	))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list all auto routers: %w", err)
	}
	defer rows.Close()
	out := []AutoRouter{}
	for rows.Next() {
		r, errScan := scanAutoRouter(rows)
		if errScan != nil {
			return nil, fmt.Errorf("postgres store: scan auto router row: %w", errScan)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AutoRouterUpdate is the partial-update shape. All fields are pointer-typed so
// callers can distinguish "leave unchanged" (nil) from "set to empty" (non-nil
// zero value).
type AutoRouterUpdate struct {
	Name        *string
	ModelID     *string
	Description *string
	DisplayName *string
	Mappings    *[]TierMapping
	Pricing     *AutoRouterPricing
	// VisionBridgeModel updates the optional per-router vision bridge model.
	// Non-nil (even empty) applies the change.
	VisionBridgeModel *string
	Enabled           *bool
	Metadata          *map[string]any
}

// Update applies a partial update to an Auto Router. Name/model id uniqueness
// is enforced; a conflict surfaces the corresponding Err.
func (s *AutoRouterStore) Update(ctx context.Context, id string, upd AutoRouterUpdate) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: auto router store not initialized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin auto router update tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if upd.Name != nil {
		name := strings.TrimSpace(*upd.Name)
		if name == "" {
			return fmt.Errorf("postgres store: auto router name cannot be empty")
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET name = $1, updated_at = NOW() WHERE id = $2`, s.table,
		), name, id); err != nil {
			if isUniqueViolation(err) {
				return ErrAutoRouterNameTaken
			}
			return fmt.Errorf("postgres store: update auto router name: %w", err)
		}
	}
	if upd.ModelID != nil {
		modelID := strings.TrimSpace(*upd.ModelID)
		if modelID == "" {
			return fmt.Errorf("postgres store: auto router model id cannot be empty")
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET model_id = $1, updated_at = NOW() WHERE id = $2`, s.table,
		), modelID, id); err != nil {
			if isUniqueViolation(err) {
				return ErrAutoRouterModelIDTaken
			}
			return fmt.Errorf("postgres store: update auto router model id: %w", err)
		}
	}
	if upd.Description != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET description = $1, updated_at = NOW() WHERE id = $2`, s.table,
		), nullableString(strings.TrimSpace(*upd.Description)), id); err != nil {
			return fmt.Errorf("postgres store: update auto router description: %w", err)
		}
	}
	if upd.DisplayName != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET display_name = $1, updated_at = NOW() WHERE id = $2`, s.table,
		), nullableString(strings.TrimSpace(*upd.DisplayName)), id); err != nil {
			return fmt.Errorf("postgres store: update auto router display_name: %w", err)
		}
	}
	if upd.VisionBridgeModel != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET vision_bridge_model = $1, updated_at = NOW() WHERE id = $2`, s.table,
		), nullableString(strings.TrimSpace(*upd.VisionBridgeModel)), id); err != nil {
			return fmt.Errorf("postgres store: update auto router vision_bridge_model: %w", err)
		}
	}
	if upd.Mappings != nil {
		mappings, _ := json.Marshal(*upd.Mappings)
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET tier_mappings = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.table,
		), string(mappings), id); err != nil {
			return fmt.Errorf("postgres store: update auto router tier_mappings: %w", err)
		}
	}
	if upd.Pricing != nil {
		pricingJSON := "null"
		if upd.Pricing != nil {
			pj, _ := json.Marshal(*upd.Pricing)
			pricingJSON = string(pj)
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET pricing = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.table,
		), pricingJSON, id); err != nil {
			return fmt.Errorf("postgres store: update auto router pricing: %w", err)
		}
	}
	if upd.Enabled != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET enabled = $1, updated_at = NOW() WHERE id = $2`, s.table,
		), *upd.Enabled, id); err != nil {
			return fmt.Errorf("postgres store: update auto router enabled: %w", err)
		}
	}
	if upd.Metadata != nil {
		meta, _ := json.Marshal(*upd.Metadata)
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET metadata = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.table,
		), string(meta), id); err != nil {
			return fmt.Errorf("postgres store: update auto router metadata: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: commit auto router update: %w", err)
	}
	// Invalidate any cached model_id entry after a mutation.
	s.invalidateModelCache()
	return nil
}

// Delete permanently removes an Auto Router.
func (s *AutoRouterStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: auto router store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, s.table), id)
	if err != nil {
		return fmt.Errorf("postgres store: delete auto router: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: auto router delete rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s", ErrAutoRouterNotFound, id)
	}
	s.invalidateModelCache()
	return nil
}

// scanAutoRouter scans the 11-column auto_routers row shape shared by Get /
// GetByModelID / ListPaged. Accepts both *sql.Row and *sql.Rows.
func scanAutoRouter(row rowScanner) (AutoRouter, error) {
	var (
		r            AutoRouter
		mappingsJSON []byte
		pricingJSON  []byte
		metadataJSON []byte
		description  sql.NullString
		displayName  sql.NullString
		visionBridge sql.NullString
	)
	if err := row.Scan(&r.ID, &r.Name, &r.ModelID, &description, &displayName,
		&mappingsJSON, &pricingJSON, &visionBridge, &r.Enabled, &metadataJSON, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return AutoRouter{}, err
	}
	r.Description = description.String
	r.DisplayName = displayName.String
	r.VisionBridgeModel = visionBridge.String
	r.Mappings = decodeTierMappings(mappingsJSON)
	if len(pricingJSON) > 0 && string(pricingJSON) != "null" {
		var p AutoRouterPricing
		if err := json.Unmarshal(pricingJSON, &p); err == nil {
			r.Pricing = &p
		}
	}
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &r.Metadata)
	}
	if r.Metadata == nil {
		r.Metadata = map[string]any{}
	}
	return r, nil
}

func decodeTierMappings(raw []byte) []TierMapping {
	out := []TierMapping{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func mapAutoRouterWriteError(err error, name, modelID string) error {
	if isUniqueViolation(err) {
		// Genuine ambiguity when both changed; heuristically report which one.
		if strings.Contains(err.Error(), "name") {
			return ErrAutoRouterNameTaken
		}
		return ErrAutoRouterModelIDTaken
	}
	return fmt.Errorf("postgres store: insert auto router (%s/%s): %w", name, modelID, err)
}

// cachedByModel returns the cached router for a lowercase model id, or nil.
func (s *AutoRouterStore) cachedByModel(modelID string) *AutoRouter {
	if s == nil {
		return nil
	}
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	return s.modelCache[strings.ToLower(strings.TrimSpace(modelID))]
}

// cacheByModel stores (or clears) the in-memory router for a model id.
func (s *AutoRouterStore) cacheByModel(modelID string, r *AutoRouter) {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if r == nil {
		delete(s.modelCache, strings.ToLower(strings.TrimSpace(modelID)))
		return
	}
	s.modelCache[strings.ToLower(strings.TrimSpace(modelID))] = r
}

// invalidateModelCache clears the whole model cache after any mutation so
// subsequent lookups re-read from the database.
func (s *AutoRouterStore) invalidateModelCache() {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.modelCache = map[string]*AutoRouter{}
}

func autoRouterSortColumn(sortBy string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "", "name":
		return "r.name", nil
	case "model_id":
		return "r.model_id", nil
	case "created_at":
		return "r.created_at", nil
	case "updated_at":
		return "r.updated_at", nil
	default:
		return "", fmt.Errorf("postgres store: unsupported auto router sort_by: %q", sortBy)
	}
}
