package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrModelGroupNotFound is returned when no model group matches the supplied
// identifier.
var ErrModelGroupNotFound = errors.New("postgres store: model group not found")

// ErrModelGroupNameTaken is returned on Create/Update when the requested
// group name is already in use by another group. Names are unique per the
// model_groups.name column.
var ErrModelGroupNameTaken = errors.New("postgres store: model group name already taken")

// ErrModelGroupInUse is returned by Delete when at least one api_key_policy or
// internal_user is still attached to the group. Callers must detach every
// entity first (or delete them) before the group can be removed.
var ErrModelGroupInUse = errors.New("postgres store: model group is still attached to one or more entities")

// ModelGroup is a reusable template of allowed/blocked model grant lists plus
// optional per-model upstream routing. A group can be attached to an API-key
// policy or to an internal user via the model_group_id column (single-valued:
// one group per entity). Once attached the group acts as the source of truth
// for AllowedModels/BlockedModels (and ModelRoutes, when attached to an
// API-key policy), overriding the entity's own fields at enforcement time.
//
// AllowedModels / BlockedModels mirror the per-key Policy semantics: empty
// AllowedModels = all allowed; BlockedModels takes precedence and supports
// trailing '*' wildcards. ModelRoutes is validated so every routed model is
// in AllowedModels (see validateModelRoutesFor).
type ModelGroup struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Description   string       `json:"description,omitempty"`
	AllowedModels []string     `json:"allowed_models,omitempty"`
	BlockedModels []string     `json:"blocked_models,omitempty"`
	ModelRoutes   []ModelRoute `json:"model_routes,omitempty"`
	// ModelRPMLimits caps requests-per-minute per concrete model for keys
	// attached to this group. Keys are model IDs (lowercased at enforcement
	// time); a missing key = unlimited. Persisted in the model_rpm_limits
	// JSONB column; kept consistent with ModelRoutes[].RPMLimit by the
	// management handler (the routes carry the authoritative per-model row).
	ModelRPMLimits map[string]int `json:"model_rpm_limits,omitempty"`
	// ModelBudgetLimits caps the total lifetime USD spend per concrete model
	// for keys attached to this group. Enforced against SUM(cost_usd) on the
	// key's usage_events rows for the model. Persisted in the
	// model_budget_limits JSONB column.
	ModelBudgetLimits map[string]float64 `json:"model_budget_limits,omitempty"`
	// DiscountPct is the group-level default discount percentage (0-100)
	// applied to the computed cost_usd of every request made by keys attached
	// to this group. A value of 20 means each event's cost is billed at 80% of
	// the model pricing (cost *= (1 - 20/100)). nil/0 = no discount. Persisted
	// as the discount_pct NUMERIC(5,2) column. Per-model discounts (see
	// ModelDiscountPcts) take precedence over this default when both are set.
	DiscountPct *float64 `json:"discount_pct,omitempty"`
	// ModelDiscountPcts carries per-model discount percentages that override
	// the group-level DiscountPct for the listed models. Keys are model ids
	// (lowercased at enforcement); a missing key falls back to DiscountPct.
	// Persisted in the model_discount_pcts JSONB column; kept consistent with
	// ModelRoutes[].DiscountPct by the management handler (the routes carry
	// the authoritative per-model row), mirroring the cap-maps pattern.
	ModelDiscountPcts map[string]float64 `json:"model_discount_pcts,omitempty"`
	Metadata          map[string]any     `json:"metadata,omitempty"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

// ModelGroupStore provides CRUD and attachment operations for model groups.
// Backed by the same *sql.DB connection as PostgresStore. The store reads the
// api_key_policies.model_group_id column so it can resolve attachments (used
// by the delete guard and the list-attachments handler). Internal Users are
// intentionally not attachable — model groups apply only to API-key policies.
type ModelGroupStore struct {
	db            *sql.DB
	groupsTable   string
	policiesTable string
	apiKeysTable  string
	usersTable    string
}

// NewModelGroupStore builds a ModelGroupStore that reuses the PostgresStore
// connection and table names. Returns nil when the parent store is nil so
// feature-detection is a single nil check.
func NewModelGroupStore(parent *PostgresStore) *ModelGroupStore {
	if parent == nil {
		return nil
	}
	return &ModelGroupStore{
		db:            parent.DB(),
		groupsTable:   parent.ModelGroupsTable(),
		policiesTable: parent.PoliciesTable(),
		apiKeysTable:  parent.APIKeysTable(),
		usersTable:    parent.InternalUsersTable(),
	}
}

// Create inserts a new model group. ID is generated when empty. AllowedModels
// / BlockedModels / ModelRoutes are normalized (trims whitespace, drops empty
// entries) before persisting so callers can pass raw request payloads.
func (s *ModelGroupStore) Create(ctx context.Context, group ModelGroup) (ModelGroup, error) {
	if s == nil || s.db == nil {
		return ModelGroup{}, fmt.Errorf("postgres store: model group store not initialized")
	}
	name := strings.TrimSpace(group.Name)
	if name == "" {
		return ModelGroup{}, fmt.Errorf("postgres store: model group name is required")
	}
	if group.ID == "" {
		group.ID = uuid.NewString()
	}
	group.Name = name
	group.Description = strings.TrimSpace(group.Description)
	if group.AllowedModels == nil {
		group.AllowedModels = []string{}
	}
	if group.BlockedModels == nil {
		group.BlockedModels = []string{}
	}
	if group.ModelRoutes == nil {
		group.ModelRoutes = []ModelRoute{}
	}
	if group.ModelRPMLimits == nil {
		group.ModelRPMLimits = map[string]int{}
	}
	if group.ModelBudgetLimits == nil {
		group.ModelBudgetLimits = map[string]float64{}
	}
	if group.ModelDiscountPcts == nil {
		group.ModelDiscountPcts = map[string]float64{}
	}
	if group.Metadata == nil {
		group.Metadata = map[string]any{}
	}
	// Normalize the group-level discount: clamp to [0,100]; drop (set to nil)
	// when absent/zero so it is omitted from the JSON response and treated as
	// "no discount" at enforcement time.
	group.DiscountPct = normalizeDiscountPct(group.DiscountPct)

	allowedJSON, _ := json.Marshal(normalizeStringSlice(group.AllowedModels))
	blockedJSON, _ := json.Marshal(normalizeStringSlice(group.BlockedModels))
	routesJSON, _ := json.Marshal(normalizeModelRoutes(group.ModelRoutes))
	rpmJSON, _ := json.Marshal(normalizeModelRPMMap(group.ModelRPMLimits))
	budgetJSON, _ := json.Marshal(normalizeModelBudgetMap(group.ModelBudgetLimits))
	discountJSON, _ := json.Marshal(normalizeModelDiscountMap(group.ModelDiscountPcts))
	metaJSON, _ := json.Marshal(group.Metadata)

	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, name, description, allowed_models, blocked_models, model_routes, model_rpm_limits, model_budget_limits, discount_pct, model_discount_pcts, metadata)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, $9, $10::jsonb, $11::jsonb)
	`, s.groupsTable),
		group.ID, group.Name, nullableString(group.Description),
		string(allowedJSON), string(blockedJSON), string(routesJSON),
		string(rpmJSON), string(budgetJSON),
		nullableFloat64Ptr(group.DiscountPct), string(discountJSON), string(metaJSON),
	); err != nil {
		if isUniqueViolation(err) {
			return ModelGroup{}, ErrModelGroupNameTaken
		}
		return ModelGroup{}, fmt.Errorf("postgres store: insert model group: %w", err)
	}
	return s.Get(ctx, group.ID)
}

// Get returns a single model group by id.
func (s *ModelGroupStore) Get(ctx context.Context, id string) (ModelGroup, error) {
	if s == nil || s.db == nil {
		return ModelGroup{}, fmt.Errorf("postgres store: model group store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, name, COALESCE(description, ''),
		       allowed_models, blocked_models, model_routes,
		       model_rpm_limits, model_budget_limits,
		       discount_pct, model_discount_pcts,
		       metadata,
		       created_at, updated_at
		FROM %s WHERE id = $1
	`, s.groupsTable), id)
	g, err := scanModelGroup(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ModelGroup{}, ErrModelGroupNotFound
		}
		return ModelGroup{}, err
	}
	return g, nil
}

// GetByName returns the model group matching the unique name. Used by the
// enforcement path when an operator wants to drive group selection by name
// (currently unused at runtime but exposed for parity with the keyed-lookup
// pattern used by internal_users).
func (s *ModelGroupStore) GetByName(ctx context.Context, name string) (ModelGroup, error) {
	if s == nil || s.db == nil {
		return ModelGroup{}, fmt.Errorf("postgres store: model group store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, name, COALESCE(description, ''),
		       allowed_models, blocked_models, model_routes,
		       model_rpm_limits, model_budget_limits,
		       discount_pct, model_discount_pcts,
		       metadata,
		       created_at, updated_at
		FROM %s WHERE name = $1
	`, s.groupsTable), name)
	g, err := scanModelGroup(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ModelGroup{}, ErrModelGroupNotFound
		}
		return ModelGroup{}, err
	}
	return g, nil
}

// ModelGroupListFilter narrows a List query.
type ModelGroupListFilter struct {
	Search    string // case-insensitive substring match on name OR description
	Page      int
	PageSize  int
	SortBy    string // "name" (default) | "created_at" | "updated_at"
	SortOrder string // "asc" | "desc" (default asc — groups read better A→Z)
}

// ListPaged returns a page of model groups plus the total row count.
func (s *ModelGroupStore) ListPaged(ctx context.Context, f ModelGroupListFilter) ([]ModelGroup, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: model group store not initialized")
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
	sortCol, err := groupSortColumn(f.SortBy)
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
		where += fmt.Sprintf(" AND (name ILIKE $%d OR description ILIKE $%d)", len(args)-1, len(args))
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s g%s`, s.groupsTable, where), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count model groups: %w", err)
	}

	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	query := fmt.Sprintf(`
		SELECT id, name, COALESCE(description, ''),
		       allowed_models, blocked_models, model_routes,
		       model_rpm_limits, model_budget_limits,
		       discount_pct, model_discount_pcts,
		       metadata,
		       created_at, updated_at
		FROM %s g%s
		ORDER BY %s %s
		LIMIT $%d OFFSET $%d
	`, s.groupsTable, where, sortCol, sortOrder, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list model groups: %w", err)
	}
	defer rows.Close()
	out := make([]ModelGroup, 0, f.PageSize)
	for rows.Next() {
		g, errScan := scanModelGroup(rows)
		if errScan != nil {
			return nil, 0, fmt.Errorf("postgres store: scan model group row: %w", errScan)
		}
		out = append(out, g)
	}
	return out, total, rows.Err()
}

// ModelGroupUpdate is the partial-update shape. All fields are pointer-typed
// so callers can distinguish "leave unchanged" (nil) from "set to empty" (non-nil
// zero value). ModelGroupID here is the value applied to the api_key_policies
// and internal_users columns; this struct itself does not carry it.
type ModelGroupUpdate struct {
	Name              *string
	Description       *string
	AllowedModels     *[]string
	BlockedModels     *[]string
	ModelRoutes       *[]ModelRoute
	ModelRPMLimits    *map[string]int
	ModelBudgetLimits *map[string]float64
	DiscountPct       *float64
	ModelDiscountPcts *map[string]float64
	Metadata          *map[string]any
}

// Update applies a partial update to a model group. Name uniqueness is
// enforced; a conflict surfaces ErrModelGroupNameTaken.
func (s *ModelGroupStore) Update(ctx context.Context, id string, upd ModelGroupUpdate) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: model group store not initialized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin model group update tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if upd.Name != nil {
		name := strings.TrimSpace(*upd.Name)
		if name == "" {
			return fmt.Errorf("postgres store: model group name cannot be empty")
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET name = $1, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), name, id); err != nil {
			if isUniqueViolation(err) {
				return ErrModelGroupNameTaken
			}
			return fmt.Errorf("postgres store: update model group name: %w", err)
		}
	}
	if upd.Description != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET description = $1, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), nullableString(strings.TrimSpace(*upd.Description)), id); err != nil {
			return fmt.Errorf("postgres store: update model group description: %w", err)
		}
	}
	if upd.AllowedModels != nil {
		allowed, _ := json.Marshal(normalizeStringSlice(*upd.AllowedModels))
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET allowed_models = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), string(allowed), id); err != nil {
			return fmt.Errorf("postgres store: update model group allowed_models: %w", err)
		}
	}
	if upd.BlockedModels != nil {
		blocked, _ := json.Marshal(normalizeStringSlice(*upd.BlockedModels))
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET blocked_models = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), string(blocked), id); err != nil {
			return fmt.Errorf("postgres store: update model group blocked_models: %w", err)
		}
	}
	if upd.ModelRoutes != nil {
		routes, _ := json.Marshal(normalizeModelRoutes(*upd.ModelRoutes))
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET model_routes = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), string(routes), id); err != nil {
			return fmt.Errorf("postgres store: update model group model_routes: %w", err)
		}
	}
	if upd.ModelRPMLimits != nil {
		rpm, _ := json.Marshal(normalizeModelRPMMap(*upd.ModelRPMLimits))
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET model_rpm_limits = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), string(rpm), id); err != nil {
			return fmt.Errorf("postgres store: update model group model_rpm_limits: %w", err)
		}
	}
	if upd.ModelBudgetLimits != nil {
		budget, _ := json.Marshal(normalizeModelBudgetMap(*upd.ModelBudgetLimits))
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET model_budget_limits = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), string(budget), id); err != nil {
			return fmt.Errorf("postgres store: update model group model_budget_limits: %w", err)
		}
	}
	if upd.DiscountPct != nil {
		nv := normalizeDiscountPct(upd.DiscountPct)
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET discount_pct = $1, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), nullableFloat64Ptr(nv), id); err != nil {
			return fmt.Errorf("postgres store: update model group discount_pct: %w", err)
		}
	}
	if upd.ModelDiscountPcts != nil {
		discount, _ := json.Marshal(normalizeModelDiscountMap(*upd.ModelDiscountPcts))
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET model_discount_pcts = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), string(discount), id); err != nil {
			return fmt.Errorf("postgres store: update model group model_discount_pcts: %w", err)
		}
	}
	if upd.Metadata != nil {
		meta, _ := json.Marshal(*upd.Metadata)
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET metadata = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.groupsTable,
		), string(meta), id); err != nil {
			return fmt.Errorf("postgres store: update model group metadata: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: commit model group update: %w", err)
	}
	return nil
}

// Delete permanently removes a model group. It is rejected with
// ErrModelGroupInUse when any api_key_policy is still attached (checked via a
// COUNT against the model_group_id index). Callers must detach (or delete)
// every referencing API-key policy first. Internal Users are intentionally not
// attachable (model groups apply only to API-key policies), so the user-side
// count is not consulted.
func (s *ModelGroupStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: model group store not initialized")
	}
	var policyRefs int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE model_group_id = $1`, s.policiesTable,
	), id).Scan(&policyRefs); err != nil {
		return fmt.Errorf("postgres store: count model group policy refs: %w", err)
	}
	if policyRefs > 0 {
		return fmt.Errorf("%w: attached to %d api_key_policies", ErrModelGroupInUse, policyRefs)
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, s.groupsTable), id)
	if err != nil {
		return fmt.Errorf("postgres store: delete model group: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: model group delete rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s", ErrModelGroupNotFound, id)
	}
	return nil
}

// SetAttachment attaches (when groupID is non-empty) or detaches (when empty)
// a model group to/from an api_key_policies row. Existence of the group is
// validated here so callers cannot set a dangling model_group_id. Returns
// ErrModelGroupNotFound when the group id does not resolve, or
// ErrAPIKeyNotFound when targetID does not match any api_keys row.
func (s *ModelGroupStore) SetAttachment(ctx context.Context, targetID, groupID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: model group store not initialized")
	}
	if targetID == "" {
		return fmt.Errorf("postgres store: SetAttachment requires target_id (api_key id)")
	}
	if groupID != "" {
		// Validate group existence so we never persist a dangling FK-like value.
		exists := false
		if err := s.db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT EXISTS(SELECT 1 FROM %s WHERE id = $1)`, s.groupsTable,
		), groupID).Scan(&exists); err != nil {
			return fmt.Errorf("postgres store: validate model group existence: %w", err)
		}
		if !exists {
			return ErrModelGroupNotFound
		}
	}
	// api_key_policies is keyed by api_key_id (the API key's id). Attaching is
	// an upsert-ish UPDATE — if the policy row does not exist yet, the caller
	// should PUT /api-keys-pg/:id/policy first to materialize it.
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET model_group_id = $1, updated_at = NOW() WHERE api_key_id = $2`,
		s.policiesTable,
	), nullableString(groupID), targetID)
	if err != nil {
		return fmt.Errorf("postgres store: set model group attachment: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: set model group attachment rows affected: %w", err)
	}
	if n == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// AttachmentRef is one API-key policy currently attached to a model group.
type AttachmentRef struct {
	// EntityID is the api_key_id (the API key's id) of the attached policy.
	EntityID string `json:"entity_id"`
	// EntityLabel is a best-effort human label: the API key's name. Empty
	// when not resolvable.
	EntityLabel string `json:"entity_label,omitempty"`
	// EntityUserID is the api_keys.user_id of the owning internal user. Empty
	// when the key is unassigned (dangling user_id).
	EntityUserID string `json:"entity_user_id,omitempty"`
	// EntityUserAlias is the owning internal user's alias (empty when the key
	// is unassigned or the user row was deleted).
	EntityUserAlias string `json:"entity_user_alias,omitempty"`
	// EntityUserEmail is the owning internal user's email (empty when the key
	// is unassigned or the user row was deleted).
	EntityUserEmail string `json:"entity_user_email,omitempty"`
}

// ListAttachments enumerates every api_key_policies row whose model_group_id
// equals the supplied group id. Used by the dashboard's ModelGroupDetailPage
// and by the delete guard's friendly error message. Each row resolves the
// attached API key's name and owning internal user (alias/email) via LEFT
// JOINs so unassigned keys and deleted users surface as empty labels rather
// than dropping the row.
func (s *ModelGroupStore) ListAttachments(ctx context.Context, groupID string) ([]AttachmentRef, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: model group store not initialized")
	}
	out := make([]AttachmentRef, 0, 8)
	// Triple join: api_key_policies → api_keys (key name) → internal_users
	// (owner alias/email). LEFT JOINs everywhere so unassigned keys and
	// deleted users surface as empty labels rather than dropping the row.
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT p.api_key_id,
		       COALESCE(k.name, ''),
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''),
		       COALESCE(u.user_email, '')
		FROM %s p
		LEFT JOIN %s k ON k.id = p.api_key_id
		LEFT JOIN %s u ON u.id = k.user_id
		WHERE p.model_group_id = $1
		ORDER BY p.api_key_id
	`, s.policiesTable, s.apiKeysTable, s.usersTable), groupID)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list model group policy attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ref AttachmentRef
		if err = rows.Scan(&ref.EntityID, &ref.EntityLabel, &ref.EntityUserID, &ref.EntityUserAlias, &ref.EntityUserEmail); err != nil {
			return nil, fmt.Errorf("postgres store: scan model group policy attachment: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// scanModelGroup scans the 13-column model_groups row shape shared by Get /
// GetByName / ListPaged. Accepts both *sql.Row and *sql.Rows via the rowScanner
// interface so a single helper covers single-row and multi-row reads.
func scanModelGroup(row rowScanner) (ModelGroup, error) {
	var (
		g            ModelGroup
		allowedJSON  []byte
		blockedJSON  []byte
		routesJSON   []byte
		rpmJSON      []byte
		budgetJSON   []byte
		discountJSON []byte
		metadataJSON []byte
		description  sql.NullString
		discountPct  sql.NullFloat64
	)
	if err := row.Scan(&g.ID, &g.Name, &description,
		&allowedJSON, &blockedJSON, &routesJSON,
		&rpmJSON, &budgetJSON,
		&discountPct, &discountJSON, &metadataJSON,
		&g.CreatedAt, &g.UpdatedAt); err != nil {
		return ModelGroup{}, err
	}
	g.Description = description.String
	g.AllowedModels = decodeStringArray(allowedJSON)
	g.BlockedModels = decodeStringArray(blockedJSON)
	g.ModelRoutes = decodeModelRoutes(routesJSON)
	g.ModelRPMLimits = decodeStringIntMap(rpmJSON)
	g.ModelBudgetLimits = decodeStringFloatMap(budgetJSON)
	if discountPct.Valid && discountPct.Float64 > 0 {
		v := discountPct.Float64
		g.DiscountPct = &v
	}
	g.ModelDiscountPcts = decodeStringFloatMap(discountJSON)
	// Project the persisted cap maps back onto the route entries so API
	// responses carry rpm_limit / max_budget_usd on each model_routes row
	// (the operator-facing per-model row shape the dashboard edits).
	g.ModelRoutes = attachCapsToRoutes(g.ModelRoutes, g.ModelRPMLimits, g.ModelBudgetLimits, g.ModelDiscountPcts)
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &g.Metadata)
	}
	if g.Metadata == nil {
		g.Metadata = map[string]any{}
	}
	return g, nil
}

// attachCapsToRoutes returns routes with rpm_limit / max_budget_usd /
// discount_pct filled from the persisted per-model cap/discount maps. Route
// fields already set win (the store normalizes caps into the maps on write,
// so this only matters for rows written before the cap columns existed). It
// also appends cap-only / discount-only models (caps without a provider pin)
// as route rows with an empty provider list so the dashboard still shows and
// can edit them; those rows are filtered back out by the handler validation
// which only enforces provider rules on rows with providers.
func attachCapsToRoutes(routes []ModelRoute, rpm map[string]int, budget, discount map[string]float64) []ModelRoute {
	if len(rpm) == 0 && len(budget) == 0 && len(discount) == 0 {
		return routes
	}
	out := make([]ModelRoute, 0, len(routes)+2)
	seen := make(map[string]struct{}, len(routes))
	for _, r := range routes {
		seen[r.Model] = struct{}{}
		if r.RPMLimit == nil {
			if v, ok := rpm[r.Model]; ok && v > 0 {
				vv := v
				r.RPMLimit = &vv
			}
		}
		if r.MaxBudgetUSD == nil {
			if v, ok := budget[r.Model]; ok && v > 0 {
				vv := v
				r.MaxBudgetUSD = &vv
			}
		}
		if r.DiscountPct == nil {
			if v, ok := discount[r.Model]; ok && v > 0 {
				vv := v
				r.DiscountPct = &vv
			}
		}
		out = append(out, r)
	}
	// Cap/discount-only models (no provider pin): surface them as route rows
	// so the dashboard edits caps in one place. Providers stay empty.
	for model, v := range rpm {
		if v <= 0 {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		row := ModelRoute{Model: model, Providers: []string{}}
		vv := v
		row.RPMLimit = &vv
		if b, ok := budget[model]; ok && b > 0 {
			bb := b
			row.MaxBudgetUSD = &bb
		}
		if d, ok := discount[model]; ok && d > 0 {
			dd := d
			row.DiscountPct = &dd
		}
		out = append(out, row)
	}
	for model, v := range budget {
		if v <= 0 {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		row := ModelRoute{Model: model, Providers: []string{}}
		vv := v
		row.MaxBudgetUSD = &vv
		if d, ok := discount[model]; ok && d > 0 {
			dd := d
			row.DiscountPct = &dd
		}
		out = append(out, row)
	}
	for model, v := range discount {
		if v <= 0 {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		row := ModelRoute{Model: model, Providers: []string{}}
		vv := v
		row.DiscountPct = &vv
		out = append(out, row)
	}
	return out
}

// rowScanner is the subset of *sql.Row / *sql.Rows used by scanModelGroup.
type rowScanner interface {
	Scan(dest ...any) error
}

// decodeStringIntMap unmarshals a { model: rpm } jsonb column. Returns an
// empty (non-nil) map when the column is empty/null so callers can rely on
// map reads without nil checks.
func decodeStringIntMap(raw []byte) map[string]int {
	out := map[string]int{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// decodeStringFloatMap unmarshals a { model: usd } jsonb column. Returns an
// empty (non-nil) map when the column is empty/null.
func decodeStringFloatMap(raw []byte) map[string]float64 {
	out := map[string]float64{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// normalizeModelRPMMap trims model ids and drops entries with non-positive
// limits (non-positive = unlimited, so persisting them is noise).
func normalizeModelRPMMap(in map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" || v <= 0 {
			continue
		}
		out[k] = v
	}
	return out
}

// normalizeModelBudgetMap trims model ids and drops entries with
// non-positive limits (non-positive = unlimited).
func normalizeModelBudgetMap(in map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" || v <= 0 {
			continue
		}
		out[k] = v
	}
	return out
}

// normalizeModelDiscountMap trims model ids and clamps/drops discount
// percentages outside the valid [0,100] range. A discount of 0 means "no
// discount" and is dropped (mirrors the cap maps' non-positive = unlimited /
// no-op convention) so persisted maps only carry meaningful overrides.
func normalizeModelDiscountMap(in map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if v <= 0 {
			continue
		}
		if v > 100 {
			v = 100
		}
		out[k] = v
	}
	return out
}

// normalizeDiscountPct clamps a group-level discount percentage to the valid
// range and returns nil when the value is absent/zero (nil propagates as
// "no discount" through JSON and the enforcement path). Callers pass a
// pointer so a 0 can be distinguished from "unchanged" in the update flow.
func normalizeDiscountPct(v *float64) *float64 {
	if v == nil {
		return nil
	}
	val := *v
	if val <= 0 {
		return nil
	}
	if val > 100 {
		val = 100
	}
	return &val
}

// nullableFloat64Ptr converts a *float64 into the value sql driver nullable
// representation used for NUMERIC columns: nil pointer → NULL, non-nil → the
// dereferenced float. Mirrors nullableString for the scalar discount column.
func nullableFloat64Ptr(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func groupSortColumn(sortBy string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "", "name":
		return "g.name", nil
	case "created_at":
		return "g.created_at", nil
	case "updated_at":
		return "g.updated_at", nil
	default:
		return "", fmt.Errorf("postgres store: unsupported model group sort_by: %q", sortBy)
	}
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505). Kept local to avoid pulling in a pgconn import
// here; the error string check is robust enough for the unique-name path.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "23505") || strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
