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
	log "github.com/sirupsen/logrus"
)

// Internal user roles. internal_user is the default subject to per-user
// budget/RPM enforcement; proxy_admin and proxy_admin_viewer bypass the
// per-user budget/RPM path (admins still honor per-key budgets).
const (
	InternalUserRole        = "internal_user"
	InternalUserAdmin       = "proxy_admin"
	InternalUserAdminViewer = "proxy_admin_viewer"
)

// Budget duration tokens accepted on the InternalUser.BudgetDuration field.
// LiteLLM-compatible: the leading integer is the window length in days.
const (
	BudgetDuration1d  = "1d"
	BudgetDuration7d  = "7d"
	BudgetDuration30d = "30d"
)

// ErrInternalUserNotFound is returned when no internal user matches the
// supplied identifier.
var ErrInternalUserNotFound = errors.New("postgres store: internal user not found")

// InternalUser mirrors a row in the internal_users table. It is the key-owner
// entity modeled after LiteLLM's InternalUser: API keys reference the user via
// api_keys.user_id, usage_events stamp user_id at flush time, and per-user
// budget/RPM enforcement reads from this row.
type InternalUser struct {
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
	// KeyCount is populated only by ListWithSpend (joined count of attached
	// api_keys). Single-row reads (Get) leave it at zero.
	KeyCount int64 `json:"key_count,omitempty"`
}

// UserStore provides CRUD and budget-window operations for internal users.
// Backed by the same *sql.DB connection as PostgresStore.
type UserStore struct {
	db           *sql.DB
	usersTable   string
	windowsTable string
	apiKeysTable string
	eventsTable  string
}

// NewUserStore builds a UserStore that reuses the PostgresStore connection and
// table names. Returns nil when the parent store is nil so feature-detection is
// a single nil check.
func NewUserStore(parent *PostgresStore) *UserStore {
	if parent == nil {
		return nil
	}
	return &UserStore{
		db:           parent.DB(),
		usersTable:   parent.InternalUsersTable(),
		windowsTable: parent.UserWindowsTable(),
		apiKeysTable: parent.APIKeysTable(),
		eventsTable:  parent.UsageEventsTable(),
	}
}

// Create inserts a new internal user. ID is generated when empty.
func (s *UserStore) Create(ctx context.Context, user InternalUser) (InternalUser, error) {
	if s == nil || s.db == nil {
		return InternalUser{}, fmt.Errorf("postgres store: user store not initialized")
	}
	if user.ID == "" {
		user.ID = uuid.NewString()
	}
	if user.UserRole == "" {
		user.UserRole = InternalUserRole
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
		return InternalUser{}, fmt.Errorf("postgres store: marshal internal user models: %w", err)
	}
	metaJSON, err := json.Marshal(user.Metadata)
	if err != nil {
		return InternalUser{}, fmt.Errorf("postgres store: marshal internal user metadata: %w", err)
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
		return InternalUser{}, fmt.Errorf("postgres store: insert internal user: %w", err)
	}
	return s.Get(ctx, user.ID)
}

// Get returns a single internal user by id.
func (s *UserStore) Get(ctx context.Context, id string) (InternalUser, error) {
	if s == nil || s.db == nil {
		return InternalUser{}, fmt.Errorf("postgres store: user store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, COALESCE(user_alias, ''), COALESCE(user_email, ''), user_role,
		       models, metadata, max_budget, COALESCE(budget_duration, ''), budget_reset_at,
		       rpm_limit, tpm_limit, max_parallel_requests, spend, created_at, updated_at
		FROM %s WHERE id = $1
	`, s.usersTable), id)
	u, err := scanInternalUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return InternalUser{}, ErrInternalUserNotFound
		}
		return InternalUser{}, err
	}
	return u, nil
}

// GetByEmail returns the internal user matching the supplied email.
func (s *UserStore) GetByEmail(ctx context.Context, email string) (InternalUser, error) {
	if s == nil || s.db == nil {
		return InternalUser{}, fmt.Errorf("postgres store: user store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, COALESCE(user_alias, ''), COALESCE(user_email, ''), user_role,
		       models, metadata, max_budget, COALESCE(budget_duration, ''), budget_reset_at,
		       rpm_limit, tpm_limit, max_parallel_requests, spend, created_at, updated_at
		FROM %s WHERE user_email = $1
	`, s.usersTable), email)
	u, err := scanInternalUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return InternalUser{}, ErrInternalUserNotFound
		}
		return InternalUser{}, err
	}
	return u, nil
}

// ListFilter narrows a ListWithSpend query.
type ListFilter struct {
	Role      string
	Search    string // matches user_alias OR user_email (case-insensitive)
	Page      int
	PageSize  int
	SortBy    string // "spend" | "created_at" | "user_alias" (default spend)
	SortOrder string // "asc" | "desc" (default desc)
}

// ListWithSpend returns a page of internal users with their spend total and
// attached api-key count. Spenders are surfaced first by default so the
// dashboard leaderboard aligns with LiteLLM's spend leaderboard.
func (s *UserStore) ListWithSpend(ctx context.Context, f ListFilter) ([]InternalUser, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: user store not initialized")
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
	if f.SortOrder == "asc" {
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
		return nil, 0, fmt.Errorf("postgres store: count internal users: %w", err)
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
		return nil, 0, fmt.Errorf("postgres store: list internal users: %w", err)
	}
	defer rows.Close()
	out := make([]InternalUser, 0, f.PageSize)
	for rows.Next() {
		u, errScan := scanInternalUserFull(rows)
		if errScan != nil {
			return nil, 0, fmt.Errorf("postgres store: scan internal user row: %w", errScan)
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// ListBudgetExceeded returns every internal user who has an active budget cap
// (max_budget > 0) and whose running spend has reached or exceeded it. Only
// budget-bearing non-admin users are returned so the alert detector can flag
// exactly the users an operator cares about (admins bypass per-user caps at
// enforcement time). Mirrors the enforcement predicate in policy.Check where a
// user's request is denied once spend >= max_budget.
func (s *UserStore) ListBudgetExceeded(ctx context.Context) ([]InternalUser, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: user store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT u.id, COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''), u.user_role,
		       u.models, u.metadata, u.max_budget, COALESCE(u.budget_duration, ''), u.budget_reset_at,
		       u.rpm_limit, u.tpm_limit, u.max_parallel_requests, u.spend, u.created_at, u.updated_at, 0
		FROM %s u
		WHERE u.max_budget > 0
		  AND u.spend >= u.max_budget
		  AND u.user_role <> 'admin'
		ORDER BY u.spend DESC`, s.usersTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list budget-exceeded internal users: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close budget-exceeded users rows failed")
		}
	}()
	out := make([]InternalUser, 0)
	for rows.Next() {
		u, errScan := scanInternalUserFull(rows)
		if errScan != nil {
			return nil, fmt.Errorf("postgres store: scan budget-exceeded user: %w", errScan)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Update mutates the mutable fields of an internal user. Pointer-typed fields
// are only updated when non-nil; scalar strings are cleared on empty.
type InternalUserUpdate struct {
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

func (s *UserStore) Update(ctx context.Context, id string, upd InternalUserUpdate) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: user store not initialized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin internal user update tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if upd.UserAlias != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET user_alias = $1, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), nullableString(*upd.UserAlias), id); err != nil {
			return fmt.Errorf("postgres store: update internal user alias: %w", err)
		}
	}
	if upd.UserEmail != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET user_email = $1, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), nullableString(*upd.UserEmail), id); err != nil {
			return fmt.Errorf("postgres store: update internal user email: %w", err)
		}
	}
	if upd.UserRole != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET user_role = $1, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), *upd.UserRole, id); err != nil {
			return fmt.Errorf("postgres store: update internal user role: %w", err)
		}
	}
	if upd.Models != nil {
		modelsJSON, _ := json.Marshal(normalizeStringSlice(*upd.Models))
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET models = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), string(modelsJSON), id); err != nil {
			return fmt.Errorf("postgres store: update internal user models: %w", err)
		}
	}
	if upd.Metadata != nil {
		metaJSON, _ := json.Marshal(*upd.Metadata)
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET metadata = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), string(metaJSON), id); err != nil {
			return fmt.Errorf("postgres store: update internal user metadata: %w", err)
		}
	}
	if upd.MaxBudget != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET max_budget = $1, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), *upd.MaxBudget, id); err != nil {
			return fmt.Errorf("postgres store: update internal user max_budget: %w", err)
		}
	}
	if upd.BudgetDuration != nil {
		resetAt := computeBudgetResetAt(*upd.BudgetDuration, nil)
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET budget_duration = $1, budget_reset_at = $2, updated_at = NOW() WHERE id = $3`,
			s.usersTable,
		), nullableString(*upd.BudgetDuration), resetAt, id); err != nil {
			return fmt.Errorf("postgres store: update internal user budget_duration: %w", err)
		}
	}
	if upd.RPMLimit != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET rpm_limit = $1, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), *upd.RPMLimit, id); err != nil {
			return fmt.Errorf("postgres store: update internal user rpm_limit: %w", err)
		}
	}
	if upd.TPMLimit != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET tpm_limit = $1, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), *upd.TPMLimit, id); err != nil {
			return fmt.Errorf("postgres store: update internal user tpm_limit: %w", err)
		}
	}
	if upd.MaxParallelRequests != nil {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET max_parallel_requests = $1, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), *upd.MaxParallelRequests, id); err != nil {
			return fmt.Errorf("postgres store: update internal user max_parallel_requests: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: commit internal user update: %w", err)
	}
	return nil
}

// Delete permanently removes an internal user. api_keys.user_id is left
// dangling (the column has no FK); the dashboard surfaces "unassigned" for
// such keys.
func (s *UserStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: user store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE id = $1`, s.usersTable,
	), id)
	if err != nil {
		return fmt.Errorf("postgres store: delete internal user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: internal user delete rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s", ErrInternalUserNotFound, id)
	}
	return nil
}

// IncrementSpend atomically increments the running spend counter by cost.
// Called from PolicyService.Consume alongside UpsertUserWindow. Failures are
// logged by the caller (best-effort, parallel to the per-key path).
func (s *UserStore) IncrementSpend(ctx context.Context, id string, cost float64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: user store not initialized")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET spend = spend + $1, updated_at = NOW() WHERE id = $2`, s.usersTable,
	), cost, id)
	if err != nil {
		return fmt.Errorf("postgres store: increment internal user spend: %w", err)
	}
	return nil
}

// ResetSpend zeros the running spend counter and re-arms budget_reset_at for
// the next window. Used by the "Reset spend" dashboard button and by the
// policy service when budget_reset_at has elapsed.
func (s *UserStore) ResetSpend(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: user store not initialized")
	}
	// Re-read the user to compute the next reset_at from budget_duration.
	u, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	resetAt := computeBudgetResetAt(u.BudgetDuration, nil)
	// Also zero user_windows counters so the new window starts clean.
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE user_id = $1`, s.windowsTable,
	), id); err != nil {
		return fmt.Errorf("postgres store: reset internal user windows: %w", err)
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET spend = 0, budget_reset_at = $1, updated_at = NOW() WHERE id = $2`,
		s.usersTable,
	), resetAt, id); err != nil {
		return fmt.Errorf("postgres store: reset internal user spend: %w", err)
	}
	return nil
}

// UpsertUserWindow increments a per-user budget window counter atomically,
// parallel to UsageStore.UpsertWindow but keyed by user_id.
func (s *UserStore) UpsertUserWindow(ctx context.Context, userID, windowType string, windowStart, windowEnd time.Time, deltaReq, deltaTokens int64, deltaCost float64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: user store not initialized")
	}
	if userID == "" || windowType == "" {
		return fmt.Errorf("postgres store: upsert user window requires user_id and window_type")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (user_id, window_type, window_start, window_end,
			request_count, total_tokens, cost_usd)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, window_type, window_start) DO UPDATE SET
			request_count = %s.request_count + EXCLUDED.request_count,
			total_tokens  = %s.total_tokens + EXCLUDED.total_tokens,
			cost_usd      = %s.cost_usd + EXCLUDED.cost_usd
	`, s.windowsTable, s.windowsTable, s.windowsTable, s.windowsTable),
		userID, windowType, windowStart, windowEnd, deltaReq, deltaTokens, deltaCost)
	if err != nil {
		return fmt.Errorf("postgres store: upsert user window: %w", err)
	}
	return nil
}

// GetUserWindow returns the per-user budget window for the supplied reference
// time. Missing rows return a zero-value window (no error).
func (s *UserStore) GetUserWindow(ctx context.Context, userID, windowType string, windowStart time.Time) (UsageWindow, error) {
	if s == nil || s.db == nil {
		return UsageWindow{}, fmt.Errorf("postgres store: user store not initialized")
	}
	var w UsageWindow
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT user_id, window_type, window_start, window_end,
			COALESCE(request_count, 0), COALESCE(total_tokens, 0),
			COALESCE(cost_usd, 0)
		FROM %s
		WHERE user_id = $1 AND window_type = $2 AND window_start = $3
	`, s.windowsTable), userID, windowType, windowStart)
	w.WindowType = windowType
	err := row.Scan(&w.APIKeyID, &w.WindowType, &w.WindowStart, &w.WindowEnd,
		&w.RequestCount, &w.TotalTokens, &w.CostUSD)
	// Reuse the APIKeyID field as the user_id carrier so UsageWindow is shared.
	if errors.Is(err, sql.ErrNoRows) {
		return UsageWindow{
			APIKeyID:    userID,
			WindowType:  windowType,
			WindowStart: windowStart,
		}, nil
	}
	if err != nil {
		return UsageWindow{}, fmt.Errorf("postgres store: get user window: %w", err)
	}
	return w, nil
}

// ListUserWindows returns all budget windows for an internal user, ordered by
// window_start desc (capped at limit; default 100).
func (s *UserStore) ListUserWindows(ctx context.Context, userID string, limit int) ([]UsageWindow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: user store not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT user_id, window_type, window_start, window_end,
			request_count, total_tokens, cost_usd
		FROM %s
		WHERE user_id = $1
		ORDER BY window_start DESC
		LIMIT $2
	`, s.windowsTable), userID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list user windows: %w", err)
	}
	defer rows.Close()
	out := make([]UsageWindow, 0, limit)
	for rows.Next() {
		var w UsageWindow
		if err = rows.Scan(&w.APIKeyID, &w.WindowType, &w.WindowStart, &w.WindowEnd,
			&w.RequestCount, &w.TotalTokens, &w.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan user window: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// scanInternalUser scans a 15-column row (no key_count). Used by Get/GetByEmail.
func scanInternalUser(row *sql.Row) (InternalUser, error) {
	var (
		u                InternalUser
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
		return InternalUser{}, err
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

// scanInternalUserFull scans a 16-column row (with trailing key_count) for
// ListWithSpend.
func scanInternalUserFull(row *sql.Rows) (InternalUser, error) {
	var (
		u                InternalUser
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
		return InternalUser{}, err
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

func userSortColumn(sortBy string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "", "spend":
		return "u.spend", nil
	case "created_at":
		return "u.created_at", nil
	case "user_alias", "alias":
		return "u.user_alias", nil
	case "updated_at":
		return "u.updated_at", nil
	default:
		return "", fmt.Errorf("postgres store: unsupported internal user sort_by: %q", sortBy)
	}
}

// computeBudgetResetAt derives the next reset timestamp for a budget duration
// token. Returns nil when duration is empty (one-shot / no periodic reset).
func computeBudgetResetAt(duration string, existing *time.Time) *time.Time {
	dur := strings.TrimSpace(duration)
	if dur == "" {
		return nil
	}
	now := time.Now().UTC()
	base := now
	if existing != nil && existing.After(now) {
		base = *existing
	}
	var next time.Time
	switch dur {
	case BudgetDuration1d:
		next = base.AddDate(0, 0, 1)
	case BudgetDuration7d:
		next = base.AddDate(0, 0, 7)
	case BudgetDuration30d:
		next = base.AddDate(0, 0, 30)
	default:
		return nil
	}
	return &next
}

// ModelSpendEntry is one row in the per-user model_spend on-the-fly SELECT.
// Mirrors LiteLLM_UserTable.model_spend JSON entries but is computed freshly
// from usage_events so it is always consistent with the persisted rows (no
// drift risk that a precomputed JSON column suffers).
type ModelSpendEntry struct {
	Model        string  `json:"model"`
	CostUSD      float64 `json:"cost_usd"`
	RequestCount int64   `json:"request_count"`
	TotalTokens  int64   `json:"total_tokens"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

// GetModelSpend aggregates per-model cost/tokens/request counts for the
// supplied user from the usage_events table. Optional from/to bounds scope
// the query. Ordered by cost_usd descending so the dashboard renders a
// spend leaderboard with no additional client-side work.
func (s *UserStore) GetModelSpend(ctx context.Context, userID string, from, to time.Time) ([]ModelSpendEntry, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: user store not initialized")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("postgres store: GetModelSpend requires user_id")
	}
	args := []any{userID}
	where := " WHERE user_id = $1"
	if !from.IsZero() {
		args = append(args, from)
		where += fmt.Sprintf(" AND requested_at >= $%d", len(args))
	}
	if !to.IsZero() {
		args = append(args, to)
		where += fmt.Sprintf(" AND requested_at < $%d", len(args))
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT model,
		       COALESCE(SUM(cost_usd), 0),
		       COALESCE(SUM(CASE WHEN NOT failed THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0)
		FROM %s e
		%s
		GROUP BY model
		ORDER BY SUM(cost_usd) DESC
	`, s.eventsTable, where), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select model spend: %w", err)
	}
	defer rows.Close()
	out := make([]ModelSpendEntry, 0, 16)
	for rows.Next() {
		var m ModelSpendEntry
		if err = rows.Scan(&m.Model, &m.CostUSD, &m.RequestCount, &m.TotalTokens, &m.InputTokens, &m.OutputTokens); err != nil {
			return nil, fmt.Errorf("postgres store: scan model spend: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReconcileResult is the outcome of a per-user spend reconciliation pass.
type ReconcileResult struct {
	UserID       string  `json:"user_id"`
	EventsSpend  float64 `json:"events_spend"`  // SUM(cost_usd) WHERE user_id=X
	StoredSpend  float64 `json:"stored_spend"`  // internal_users.spend snapshot pre-update
	UpdatedSpend float64 `json:"updated_spend"` // internal_users.spend after update
	Delta        float64 `json:"delta"`         // EventsSpend - StoredSpend (post-update = 0)
	Updated      bool    `json:"updated"`
}

// ReconcileSpend recomputes the user's running spend from usage_events and
// updates internal_users.spend, in a single transaction. Used to recover
// after `IncrementSpend` failures (best-effort Consume) or operator-triggered
// post-mortems. Optional from/to bounds scope the SUM (so a windowed
// reconciliation can re-arm spend for a specific interval).
func (s *UserStore) ReconcileSpend(ctx context.Context, userID string, from, to time.Time) (ReconcileResult, error) {
	if s == nil || s.db == nil {
		return ReconcileResult{}, fmt.Errorf("postgres store: user store not initialized")
	}
	if strings.TrimSpace(userID) == "" {
		return ReconcileResult{}, fmt.Errorf("postgres store: ReconcileSpend requires user_id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("postgres store: begin reconcile tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	args := []any{userID}
	where := " WHERE user_id = $1"
	if !from.IsZero() {
		args = append(args, from)
		where += fmt.Sprintf(" AND requested_at >= $%d", len(args))
	}
	if !to.IsZero() {
		args = append(args, to)
		where += fmt.Sprintf(" AND requested_at < $%d", len(args))
	}

	var (
		eventsSum sql.NullFloat64
		storedSum float64
	)
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT SUM(cost_usd) FROM %s e %s`, s.eventsTable, where), args...)
	if err = row.Scan(&eventsSum); err != nil {
		return ReconcileResult{}, fmt.Errorf("postgres store: reconcile SUM query: %w", err)
	}
	eventsSpend := 0.0
	if eventsSum.Valid {
		eventsSpend = eventsSum.Float64
	}

	if err = tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT spend FROM %s WHERE id = $1`, s.usersTable,
	), userID).Scan(&storedSum); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReconcileResult{}, ErrInternalUserNotFound
		}
		return ReconcileResult{}, fmt.Errorf("postgres store: reconcile SELECT spend: %w", err)
	}

	delta := eventsSpend - storedSum
	updated := false
	// Avoid a meaningless write when already in sync.
	if delta != 0 {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET spend = $1, updated_at = NOW() WHERE id = $2`, s.usersTable,
		), eventsSpend, userID); err != nil {
			return ReconcileResult{}, fmt.Errorf("postgres store: reconcile UPDATE spend: %w", err)
		}
		updated = true
	}
	if err = tx.Commit(); err != nil {
		return ReconcileResult{}, fmt.Errorf("postgres store: reconcile commit: %w", err)
	}
	return ReconcileResult{
		UserID:       userID,
		EventsSpend:  eventsSpend,
		StoredSpend:  storedSum,
		UpdatedSpend: eventsSpend,
		Delta:        delta,
		Updated:      updated,
	}, nil
}

// ReconcileAll runs ReconcileSpend for every internal user that has events
// newer than `since`. Useful as a one-shot bulk recovery. The since bound
// limits the recomputation window so the bulk call does not re-sum the
// entire history for every user. Returns one ReconcileResult per affected
// user (in arbitrary order). When since is the zero-value, the entire
// usage_events history is recomputed for every user (slow — operators
// should scope with `since`).
func (s *UserStore) ReconcileAll(ctx context.Context, since time.Time) ([]ReconcileResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: user store not initialized")
	}
	args := []any{}
	where := " WHERE user_id IS NOT NULL AND user_id <> ''"
	if !since.IsZero() {
		args = append(args, since)
		where += fmt.Sprintf(" AND requested_at >= $%d", len(args))
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT DISTINCT user_id FROM %s e %s`, s.eventsTable, where,
	), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: reconcile-all list: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres store: reconcile-all scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ReconcileResult, 0, len(ids))
	for _, id := range ids {
		res, errRec := s.ReconcileSpend(ctx, id, since, time.Time{})
		if errRec != nil {
			if errors.Is(errRec, ErrInternalUserNotFound) {
				// User row was deleted but events remain. Skip — orphan.
				continue
			}
			return out, errRec
		}
		out = append(out, res)
	}
	return out, nil
}
