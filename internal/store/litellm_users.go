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

// LiteLLM user roles mirror the runtime internal-user roles (internal_user is
// the default; proxy_admin / proxy_admin_viewer bypass per-user budget/RPM
// enforcement in the runtime path).
const (
	LiteLLMUserRole        = "internal_user"
	LiteLLMUserAdmin       = "proxy_admin"
	LiteLLMUserAdminViewer = "proxy_admin_viewer"
)

// ErrLiteLLMUserNotFound is returned when no Manage-LiteLLM user matches the
// supplied identifier.
var ErrLiteLLMUserNotFound = errors.New("postgres store: litellm user not found")

// ErrLiteLLMUserEmailExists is returned when a create/update would violate the
// unique user_email constraint on litellm_internal_users.
var ErrLiteLLMUserEmailExists = errors.New("postgres store: litellm user email already exists")

// LiteLLMUser mirrors a row in the litellm_internal_users table. It is the
// Manage-LiteLLM key-owner entity: API keys reference the user via
// litellm_api_keys.user_id and each key carries its own complete LiteLLM-style
// policy (spend, budget, tpm_limit, tags, aliases). This table is intentionally
// separate from internal_users — the runtime proxy never reads it.
type LiteLLMUser struct {
	ID                  string         `json:"id"`
	UserAlias           string         `json:"user_alias,omitempty"`
	UserEmail           string         `json:"user_email,omitempty"`
	UserRole            string         `json:"user_role"`
	Models              []string       `json:"models,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	MaxBudget           *float64       `json:"max_budget,omitempty"`
	BudgetDuration      string         `json:"budget_duration,omitempty"`
	BudgetResetAt       *time.Time     `json:"budget_reset_at,omitempty"`
	RPMLimit            *int64         `json:"rpm_limit,omitempty"`
	TPMLimit            *int64         `json:"tpm_limit,omitempty"`
	MaxParallelRequests *int           `json:"max_parallel_requests,omitempty"`
	Spend               float64        `json:"spend"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	// KeyCount is populated only by the list path (joined count of attached
	// litellm_api_keys). Single-row reads (Get) leave it at zero.
	KeyCount int64 `json:"key_count,omitempty"`
}

// LiteLLMUserStore provides CRUD operations for Manage-LiteLLM internal users.
// Backed by the same *sql.DB connection as PostgresStore.
type LiteLLMUserStore struct {
	db           *sql.DB
	usersTable   string
	apiKeysTable string
}

// NewLiteLLMUserStore builds a LiteLLMUserStore that reuses the PostgresStore
// connection and table names. Returns nil when the parent store is nil so
// feature-detection is a single nil check.
func NewLiteLLMUserStore(parent *PostgresStore) *LiteLLMUserStore {
	if parent == nil {
		return nil
	}
	return &LiteLLMUserStore{
		db:           parent.DB(),
		usersTable:   parent.LiteLLMUsersTable(),
		apiKeysTable: parent.LiteLLMKeysTable(),
	}
}

// LiteLLMListFilter narrows a LiteLLMUserStore.List query.
type LiteLLMListFilter struct {
	Role      string
	Search    string // matches user_alias OR user_email (case-insensitive)
	Page      int
	PageSize  int
	SortBy    string // "spend" | "created_at" | "user_alias" (default spend)
	SortOrder string // "asc" | "desc" (default desc)
}

// Create inserts a new Manage-LiteLLM internal user. ID is generated when
// empty; user_role defaults to internal_user. A duplicate user_email surfaces
// as ErrLiteLLMUserEmailExists.
func (s *LiteLLMUserStore) Create(ctx context.Context, user LiteLLMUser) (LiteLLMUser, error) {
	if s == nil || s.db == nil {
		return LiteLLMUser{}, fmt.Errorf("postgres store: litellm user store not initialized")
	}
	if user.ID == "" {
		user.ID = uuid.NewString()
	}
	if user.UserRole == "" {
		user.UserRole = LiteLLMUserRole
	}
	if user.Models == nil {
		user.Models = []string{}
	}
	if user.Metadata == nil {
		user.Metadata = map[string]any{}
	}
	user.BudgetResetAt = computeBudgetResetAt(user.BudgetDuration, user.BudgetResetAt)

	modelsJSON, err := json.Marshal(user.Models)
	if err != nil {
		return LiteLLMUser{}, fmt.Errorf("postgres store: marshal litellm user models: %w", err)
	}
	metaJSON, err := json.Marshal(user.Metadata)
	if err != nil {
		return LiteLLMUser{}, fmt.Errorf("postgres store: marshal litellm user metadata: %w", err)
	}

	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, user_alias, user_email, user_role, models, metadata,
			max_budget, budget_duration, budget_reset_at, rpm_limit, tpm_limit,
			max_parallel_requests, spend)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9, $10, $11, $12, 0)
	`, s.usersTable),
		user.ID, nullableString(user.UserAlias), nullableString(user.UserEmail),
		user.UserRole, string(modelsJSON), string(metaJSON),
		user.MaxBudget, nullableString(user.BudgetDuration), user.BudgetResetAt,
		user.RPMLimit, user.TPMLimit, user.MaxParallelRequests,
	); err != nil {
		if isUniqueViolation(err) {
			return LiteLLMUser{}, ErrLiteLLMUserEmailExists
		}
		return LiteLLMUser{}, fmt.Errorf("postgres store: insert litellm user: %w", err)
	}
	return s.Get(ctx, user.ID)
}

// Upsert inserts a new Manage-LiteLLM user or updates an existing one by ID.
// It is used by the external-liteLLM sync: rows from the remote instance are
// created or refreshed in place, and the user's spend total is carried over.
// A duplicate user_email (against a different id) surfaces as
// ErrLiteLLMUserEmailExists.
func (s *LiteLLMUserStore) Upsert(ctx context.Context, user LiteLLMUser) (LiteLLMUser, error) {
	if s == nil || s.db == nil {
		return LiteLLMUser{}, fmt.Errorf("postgres store: litellm user store not initialized")
	}
	if user.ID == "" {
		user.ID = uuid.NewString()
	}
	if user.UserRole == "" {
		user.UserRole = LiteLLMUserRole
	}
	if user.Models == nil {
		user.Models = []string{}
	}
	if user.Metadata == nil {
		user.Metadata = map[string]any{}
	}
	user.BudgetResetAt = computeBudgetResetAt(user.BudgetDuration, user.BudgetResetAt)

	modelsJSON, err := json.Marshal(user.Models)
	if err != nil {
		return LiteLLMUser{}, fmt.Errorf("postgres store: marshal litellm user models: %w", err)
	}
	metaJSON, err := json.Marshal(user.Metadata)
	if err != nil {
		return LiteLLMUser{}, fmt.Errorf("postgres store: marshal litellm user metadata: %w", err)
	}

	// Persist the remote row's updated_at when the sync parser carried one, so
	// the "last update from LiteLLM" detection reflects the remote change time
	// rather than the local write time. Fall back to now for rows without one.
	updatedAt := user.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, user_alias, user_email, user_role, models, metadata,
			max_budget, budget_duration, budget_reset_at, rpm_limit, tpm_limit,
			max_parallel_requests, spend, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (id) DO UPDATE SET
			user_alias           = EXCLUDED.user_alias,
			user_email           = EXCLUDED.user_email,
			user_role            = EXCLUDED.user_role,
			models               = EXCLUDED.models,
			metadata             = EXCLUDED.metadata,
			max_budget           = EXCLUDED.max_budget,
			budget_duration      = EXCLUDED.budget_duration,
			budget_reset_at      = EXCLUDED.budget_reset_at,
			rpm_limit            = EXCLUDED.rpm_limit,
			tpm_limit            = EXCLUDED.tpm_limit,
			max_parallel_requests = EXCLUDED.max_parallel_requests,
			spend                = EXCLUDED.spend,
			updated_at           = EXCLUDED.updated_at
	`, s.usersTable),
		user.ID, nullableString(user.UserAlias), nullableString(user.UserEmail),
		user.UserRole, string(modelsJSON), string(metaJSON),
		user.MaxBudget, nullableString(user.BudgetDuration), user.BudgetResetAt,
		user.RPMLimit, user.TPMLimit, user.MaxParallelRequests, user.Spend, updatedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return LiteLLMUser{}, ErrLiteLLMUserEmailExists
		}
		return LiteLLMUser{}, fmt.Errorf("postgres store: upsert litellm user: %w", err)
	}
	return s.Get(ctx, user.ID)
}

// Get returns a single Manage-LiteLLM user by id.
func (s *LiteLLMUserStore) Get(ctx context.Context, id string) (LiteLLMUser, error) {
	if s == nil || s.db == nil {
		return LiteLLMUser{}, fmt.Errorf("postgres store: litellm user store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, COALESCE(user_alias, ''), COALESCE(user_email, ''), user_role,
		       models, metadata, max_budget, COALESCE(budget_duration, ''), budget_reset_at,
		       rpm_limit, tpm_limit, max_parallel_requests, spend, created_at, updated_at
		FROM %s WHERE id = $1
	`, s.usersTable), id)
	u, err := scanLiteLLMUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LiteLLMUser{}, ErrLiteLLMUserNotFound
		}
		return LiteLLMUser{}, err
	}
	return u, nil
}

// GetByEmail returns the Manage-LiteLLM user matching the supplied email.
func (s *LiteLLMUserStore) GetByEmail(ctx context.Context, email string) (LiteLLMUser, error) {
	if s == nil || s.db == nil {
		return LiteLLMUser{}, fmt.Errorf("postgres store: litellm user store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, COALESCE(user_alias, ''), COALESCE(user_email, ''), user_role,
		       models, metadata, max_budget, COALESCE(budget_duration, ''), budget_reset_at,
		       rpm_limit, tpm_limit, max_parallel_requests, spend, created_at, updated_at
		FROM %s WHERE user_email = $1
	`, s.usersTable), email)
	u, err := scanLiteLLMUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LiteLLMUser{}, ErrLiteLLMUserNotFound
		}
		return LiteLLMUser{}, err
	}
	return u, nil
}

// List returns a page of Manage-LiteLLM users with their spend total and
// attached key count. Spenders are surfaced first by default so the dashboard
// leaderboard aligns with LiteLLM's spend leaderboard.
func (s *LiteLLMUserStore) List(ctx context.Context, f LiteLLMListFilter) ([]LiteLLMUser, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: litellm user store not initialized")
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
	sortCol, err := userSortColumn(f.SortBy)
	if err != nil {
		return nil, 0, err
	}
	sortOrder := "DESC"
	if strings.EqualFold(f.SortOrder, "asc") {
		sortOrder = "ASC"
	}

	where := " WHERE 1=1"
	args := []any{}
	if f.Role != "" {
		args = append(args, f.Role)
		where += fmt.Sprintf(" AND u.user_role = $%d", len(args))
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%", "%"+f.Search+"%")
		where += fmt.Sprintf(" AND (u.user_alias ILIKE $%d OR u.user_email ILIKE $%d)", len(args)-1, len(args))
	}

	// Count first.
	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s u%s`, s.usersTable, where)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count litellm users: %w", err)
	}

	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	query := fmt.Sprintf(`
		SELECT u.id, COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''), u.user_role,
		       u.models, u.metadata, u.max_budget, COALESCE(u.budget_duration, ''), u.budget_reset_at,
		       u.rpm_limit, u.tpm_limit, u.max_parallel_requests, u.spend, u.created_at, u.updated_at,
		       (SELECT COUNT(*) FROM %s k WHERE k.user_id = u.id) AS key_count
		FROM %s u%s
		ORDER BY %s %s
		LIMIT $%d OFFSET $%d
	`, s.apiKeysTable, s.usersTable, where, sortCol, sortOrder, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list litellm users: %w", err)
	}
	defer rows.Close()
	out := make([]LiteLLMUser, 0, f.PageSize)
	for rows.Next() {
		u, errScan := scanLiteLLMUserFull(rows)
		if errScan != nil {
			return nil, 0, fmt.Errorf("postgres store: scan litellm user row: %w", errScan)
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// ListAll returns every Manage-LiteLLM internal user (no pagination). It backs
// the "sync to NixLLM" push, which needs the full source set so the runtime
// internal_users table can mirror the external LiteLLM users. KeyCount is left
// at zero; single-row reads (Get) leave it at zero too.
func (s *LiteLLMUserStore) ListAll(ctx context.Context) ([]LiteLLMUser, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: litellm user store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, COALESCE(user_alias, ''), COALESCE(user_email, ''), user_role,
		       models, metadata, max_budget, COALESCE(budget_duration, ''), budget_reset_at,
		       rpm_limit, tpm_limit, max_parallel_requests, spend, created_at, updated_at, 0
		FROM %s`, s.usersTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list all litellm users: %w", err)
	}
	defer rows.Close()
	out := make([]LiteLLMUser, 0)
	for rows.Next() {
		u, errScan := scanLiteLLMUserFull(rows)
		if errScan != nil {
			return nil, fmt.Errorf("postgres store: scan litellm user row: %w", errScan)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Pointer-typed fields are only updated when non-nil; scalar strings are
// cleared on empty.
type LiteLLMUserUpdate struct {
	UserAlias           *string
	UserEmail           *string
	UserRole            *string
	Models              *[]string
	Metadata            *map[string]any
	MaxBudget           *float64
	BudgetDuration      *string
	RPMLimit            *int64
	TPMLimit            *int64
	MaxParallelRequests *int
}

// Update applies the supplied partial update. A duplicate user_email (against
// a different user) surfaces as ErrLiteLLMUserEmailExists.
func (s *LiteLLMUserStore) Update(ctx context.Context, id string, upd LiteLLMUserUpdate) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm user store not initialized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin litellm user update tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	run := func(query string, args ...any) error {
		if _, errExec := tx.ExecContext(ctx, fmt.Sprintf(query, s.usersTable), args...); errExec != nil {
			return errExec
		}
		return nil
	}
	if upd.UserAlias != nil {
		if err = run(`UPDATE %s SET user_alias = $1, updated_at = NOW() WHERE id = $2`, nullableString(*upd.UserAlias), id); err != nil {
			return fmt.Errorf("postgres store: update litellm user alias: %w", err)
		}
	}
	if upd.UserEmail != nil {
		if err = run(`UPDATE %s SET user_email = $1, updated_at = NOW() WHERE id = $2`, nullableString(*upd.UserEmail), id); err != nil {
			if isUniqueViolation(err) {
				return ErrLiteLLMUserEmailExists
			}
			return fmt.Errorf("postgres store: update litellm user email: %w", err)
		}
	}
	if upd.UserRole != nil {
		if err = run(`UPDATE %s SET user_role = $1, updated_at = NOW() WHERE id = $2`, *upd.UserRole, id); err != nil {
			return fmt.Errorf("postgres store: update litellm user role: %w", err)
		}
	}
	if upd.Models != nil {
		modelsJSON, _ := json.Marshal(normalizeStringSlice(*upd.Models))
		if err = run(`UPDATE %s SET models = $1::jsonb, updated_at = NOW() WHERE id = $2`, string(modelsJSON), id); err != nil {
			return fmt.Errorf("postgres store: update litellm user models: %w", err)
		}
	}
	if upd.Metadata != nil {
		metaJSON, _ := json.Marshal(*upd.Metadata)
		if err = run(`UPDATE %s SET metadata = $1::jsonb, updated_at = NOW() WHERE id = $2`, string(metaJSON), id); err != nil {
			return fmt.Errorf("postgres store: update litellm user metadata: %w", err)
		}
	}
	if upd.MaxBudget != nil {
		if err = run(`UPDATE %s SET max_budget = $1, updated_at = NOW() WHERE id = $2`, *upd.MaxBudget, id); err != nil {
			return fmt.Errorf("postgres store: update litellm user max_budget: %w", err)
		}
	}
	if upd.BudgetDuration != nil {
		resetAt := computeBudgetResetAt(*upd.BudgetDuration, nil)
		if err = run(`UPDATE %s SET budget_duration = $1, budget_reset_at = $2, updated_at = NOW() WHERE id = $3`,
			nullableString(*upd.BudgetDuration), resetAt, id); err != nil {
			return fmt.Errorf("postgres store: update litellm user budget_duration: %w", err)
		}
	}
	if upd.RPMLimit != nil {
		if err = run(`UPDATE %s SET rpm_limit = $1, updated_at = NOW() WHERE id = $2`, *upd.RPMLimit, id); err != nil {
			return fmt.Errorf("postgres store: update litellm user rpm_limit: %w", err)
		}
	}
	if upd.TPMLimit != nil {
		if err = run(`UPDATE %s SET tpm_limit = $1, updated_at = NOW() WHERE id = $2`, *upd.TPMLimit, id); err != nil {
			return fmt.Errorf("postgres store: update litellm user tpm_limit: %w", err)
		}
	}
	if upd.MaxParallelRequests != nil {
		if err = run(`UPDATE %s SET max_parallel_requests = $1, updated_at = NOW() WHERE id = $2`, *upd.MaxParallelRequests, id); err != nil {
			return fmt.Errorf("postgres store: update litellm user max_parallel_requests: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: commit litellm user update: %w", err)
	}
	return nil
}

// Delete permanently removes a Manage-LiteLLM user. Its keys are detached
// (litellm_api_keys.user_id cleared) rather than deleted so an operator can
// reassign them; the dashboard surfaces "unassigned" for such keys.
func (s *LiteLLMUserStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm user store not initialized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin litellm user delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET user_id = NULL, updated_at = NOW() WHERE user_id = $1`, s.apiKeysTable,
	), id); err != nil {
		return fmt.Errorf("postgres store: detach litellm user keys: %w", err)
	}
	res, err := tx.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE id = $1`, s.usersTable,
	), id)
	if err != nil {
		return fmt.Errorf("postgres store: delete litellm user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: litellm user delete rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s", ErrLiteLLMUserNotFound, id)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: commit litellm user delete: %w", err)
	}
	return nil
}

// ResetSpend zeros the running spend counter of a Manage-LiteLLM user and
// re-arms budget_reset_at for the next window.
func (s *LiteLLMUserStore) ResetSpend(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm user store not initialized")
	}
	u, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	resetAt := computeBudgetResetAt(u.BudgetDuration, nil)
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET spend = 0, budget_reset_at = $1, updated_at = NOW() WHERE id = $2`,
		s.usersTable,
	), resetAt, id)
	if err != nil {
		return fmt.Errorf("postgres store: reset litellm user spend: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: litellm user reset rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s", ErrLiteLLMUserNotFound, id)
	}
	return nil
}

// scanLiteLLMUser scans a 15-column row (no key_count). Used by Get/GetByEmail.
func scanLiteLLMUser(row *sql.Row) (LiteLLMUser, error) {
	var (
		u                LiteLLMUser
		modelsJSON       []byte
		metadataJSON     []byte
		maxBudget        sql.NullFloat64
		budgetResetAt    sql.NullTime
		rpmLimit         sql.NullInt64
		tpmLimit         sql.NullInt64
		maxParallelLimit sql.NullInt64
		budgetDuration   string
	)
	if err := row.Scan(&u.ID, &u.UserAlias, &u.UserEmail, &u.UserRole,
		&modelsJSON, &metadataJSON, &maxBudget, &budgetDuration, &budgetResetAt,
		&rpmLimit, &tpmLimit, &maxParallelLimit, &u.Spend, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return LiteLLMUser{}, err
	}
	u.Models = decodeStringArray(modelsJSON)
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &u.Metadata)
	}
	if u.Metadata == nil {
		u.Metadata = map[string]any{}
	}
	u.BudgetDuration = budgetDuration
	if maxBudget.Valid {
		v := maxBudget.Float64
		u.MaxBudget = &v
	}
	if budgetResetAt.Valid {
		t := budgetResetAt.Time
		u.BudgetResetAt = &t
	}
	if rpmLimit.Valid {
		v := rpmLimit.Int64
		u.RPMLimit = &v
	}
	if tpmLimit.Valid {
		v := tpmLimit.Int64
		u.TPMLimit = &v
	}
	if maxParallelLimit.Valid {
		v := int(maxParallelLimit.Int64)
		u.MaxParallelRequests = &v
	}
	return u, nil
}

// scanLiteLLMUserFull scans a 16-column row (with trailing key_count) for List.
func scanLiteLLMUserFull(row *sql.Rows) (LiteLLMUser, error) {
	var (
		u                LiteLLMUser
		modelsJSON       []byte
		metadataJSON     []byte
		maxBudget        sql.NullFloat64
		budgetResetAt    sql.NullTime
		rpmLimit         sql.NullInt64
		tpmLimit         sql.NullInt64
		maxParallelLimit sql.NullInt64
		budgetDuration   string
	)
	if err := row.Scan(&u.ID, &u.UserAlias, &u.UserEmail, &u.UserRole,
		&modelsJSON, &metadataJSON, &maxBudget, &budgetDuration, &budgetResetAt,
		&rpmLimit, &tpmLimit, &maxParallelLimit, &u.Spend, &u.CreatedAt, &u.UpdatedAt, &u.KeyCount); err != nil {
		return LiteLLMUser{}, err
	}
	u.Models = decodeStringArray(modelsJSON)
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &u.Metadata)
	}
	if u.Metadata == nil {
		u.Metadata = map[string]any{}
	}
	u.BudgetDuration = budgetDuration
	if maxBudget.Valid {
		v := maxBudget.Float64
		u.MaxBudget = &v
	}
	if budgetResetAt.Valid {
		t := budgetResetAt.Time
		u.BudgetResetAt = &t
	}
	if rpmLimit.Valid {
		v := rpmLimit.Int64
		u.RPMLimit = &v
	}
	if tpmLimit.Valid {
		v := tpmLimit.Int64
		u.TPMLimit = &v
	}
	if maxParallelLimit.Valid {
		v := int(maxParallelLimit.Int64)
		u.MaxParallelRequests = &v
	}
	return u, nil
}
