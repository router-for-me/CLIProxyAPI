package usercontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const inviteReservationLifetime = 30 * time.Minute

// PostgresRepository keeps account data beside the existing PostgreSQL auth store.
// Table names are intentionally fixed; the configured schema still isolates deployments.
type PostgresRepository struct {
	db     *sql.DB
	schema string
}

func NewPostgresRepository(db *sql.DB, schema string) *PostgresRepository {
	return &PostgresRepository{db: db, schema: strings.TrimSpace(schema)}
}

func (r *PostgresRepository) EnsureSchema(ctx context.Context) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("managed users: postgres is not initialized")
	}
	queries := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			requests_per_minute INTEGER NOT NULL DEFAULT 0,
			concurrent_requests INTEGER NOT NULL DEFAULT 0,
			monthly_tokens BIGINT NOT NULL DEFAULT 0,
			allowed_models JSONB NOT NULL DEFAULT '[]'::jsonb,
			expires_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`, r.table("managed_users")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			prefix TEXT NOT NULL,
			secret_hash BYTEA NOT NULL,
			status TEXT NOT NULL,
			expires_at TIMESTAMPTZ,
			last_used_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL
		)`, r.table("managed_api_keys"), r.table("managed_users")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			user_id TEXT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			key_id TEXT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			period_start DATE NOT NULL,
			requests BIGINT NOT NULL DEFAULT 0,
			failed BIGINT NOT NULL DEFAULT 0,
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (user_id, key_id, period_start)
		)`, r.table("managed_usage_monthly"), r.table("managed_users"), r.table("managed_api_keys")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			token_hash BYTEA NOT NULL UNIQUE,
			label TEXT NOT NULL,
			providers JSONB NOT NULL,
			max_uses INTEGER NOT NULL,
			used_uses INTEGER NOT NULL DEFAULT 0,
			reserved_uses INTEGER NOT NULL DEFAULT 0,
			active BOOLEAN NOT NULL DEFAULT TRUE,
			expires_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL
		)`, r.table("oauth_invitations")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			state TEXT PRIMARY KEY,
			invite_id TEXT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			provider TEXT NOT NULL,
			status TEXT NOT NULL,
			auth_id TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL,
			completed_at TIMESTAMPTZ
		)`, r.table("oauth_invite_sessions"), r.table("oauth_invitations")),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (user_id)`, quoteIdentifier("managed_api_keys_user_id_idx"), r.table("managed_api_keys")),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (invite_id, status)`, quoteIdentifier("oauth_invite_sessions_invite_idx"), r.table("oauth_invite_sessions")),
	}
	for _, query := range queries {
		if _, errExec := r.db.ExecContext(ctx, query); errExec != nil {
			return fmt.Errorf("managed users: create schema: %w", errExec)
		}
	}
	return nil
}

func (r *PostgresRepository) CreateUser(ctx context.Context, user User) (User, error) {
	models, err := json.Marshal(user.Limits.AllowedModels)
	if err != nil {
		return User{}, fmt.Errorf("managed users: encode models: %w", err)
	}
	query := fmt.Sprintf(`INSERT INTO %s
		(id, name, email, status, requests_per_minute, concurrent_requests, monthly_tokens, allowed_models, expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, r.table("managed_users"))
	_, err = r.db.ExecContext(ctx, query, user.ID, user.Name, user.Email, user.Status, user.Limits.RequestsPerMinute, user.Limits.ConcurrentRequests, user.Limits.MonthlyTokens, models, user.ExpiresAt, user.CreatedAt, user.UpdatedAt)
	if err != nil {
		return User{}, fmt.Errorf("managed users: create user: %w", err)
	}
	return user, nil
}

func (r *PostgresRepository) ListUsers(ctx context.Context) ([]User, error) {
	query := fmt.Sprintf(`SELECT id, name, email, status, requests_per_minute, concurrent_requests, monthly_tokens, allowed_models, expires_at, created_at, updated_at FROM %s ORDER BY created_at DESC`, r.table("managed_users"))
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("managed users: list users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	users := make([]User, 0)
	for rows.Next() {
		user, errScan := scanUser(rows)
		if errScan != nil {
			return nil, errScan
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (r *PostgresRepository) GetUser(ctx context.Context, id string) (User, error) {
	query := fmt.Sprintf(`SELECT id, name, email, status, requests_per_minute, concurrent_requests, monthly_tokens, allowed_models, expires_at, created_at, updated_at FROM %s WHERE id=$1`, r.table("managed_users"))
	user, err := scanUser(r.db.QueryRowContext(ctx, query, id))
	return user, normalizeNotFound(err)
}

func (r *PostgresRepository) UpdateUser(ctx context.Context, user User) (User, error) {
	models, err := json.Marshal(user.Limits.AllowedModels)
	if err != nil {
		return User{}, fmt.Errorf("managed users: encode models: %w", err)
	}
	query := fmt.Sprintf(`UPDATE %s SET name=$2,email=$3,status=$4,requests_per_minute=$5,concurrent_requests=$6,monthly_tokens=$7,allowed_models=$8,expires_at=$9,updated_at=$10 WHERE id=$1`, r.table("managed_users"))
	result, err := r.db.ExecContext(ctx, query, user.ID, user.Name, user.Email, user.Status, user.Limits.RequestsPerMinute, user.Limits.ConcurrentRequests, user.Limits.MonthlyTokens, models, user.ExpiresAt, user.UpdatedAt)
	if err != nil {
		return User{}, fmt.Errorf("managed users: update user: %w", err)
	}
	if err = requireChanged(result); err != nil {
		return User{}, err
	}
	return user, nil
}

func (r *PostgresRepository) DeleteUser(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id=$1`, r.table("managed_users")), id)
	if err != nil {
		return fmt.Errorf("managed users: delete user: %w", err)
	}
	return requireChanged(result)
}

func (r *PostgresRepository) CreateAPIKey(ctx context.Context, key APIKey, secretHash []byte) (APIKey, error) {
	query := fmt.Sprintf(`INSERT INTO %s (id,user_id,name,prefix,secret_hash,status,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, r.table("managed_api_keys"))
	_, err := r.db.ExecContext(ctx, query, key.ID, key.UserID, key.Name, key.Prefix, secretHash, key.Status, key.ExpiresAt, key.CreatedAt)
	if err != nil {
		return APIKey{}, fmt.Errorf("managed users: create API key: %w", err)
	}
	return key, nil
}

func (r *PostgresRepository) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	query := fmt.Sprintf(`SELECT id,user_id,name,prefix,status,expires_at,last_used_at,created_at FROM %s WHERE user_id=$1 ORDER BY created_at DESC`, r.table("managed_api_keys"))
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("managed users: list API keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	keys := make([]APIKey, 0)
	for rows.Next() {
		key, errScan := scanAPIKey(rows)
		if errScan != nil {
			return nil, errScan
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (r *PostgresRepository) GetStoredAPIKey(ctx context.Context, id string) (StoredAPIKey, error) {
	query := fmt.Sprintf(`SELECT
		k.id,k.user_id,k.name,k.prefix,k.status,k.expires_at,k.last_used_at,k.created_at,k.secret_hash,
		u.id,u.name,u.email,u.status,u.requests_per_minute,u.concurrent_requests,u.monthly_tokens,u.allowed_models,u.expires_at,u.created_at,u.updated_at,
		COALESCE(SUM(m.total_tokens),0)
		FROM %s k JOIN %s u ON u.id=k.user_id
		LEFT JOIN %s m ON m.user_id=u.id AND m.period_start=DATE_TRUNC('month', NOW() AT TIME ZONE 'UTC')::date
		WHERE k.id=$1 GROUP BY k.id,u.id`, r.table("managed_api_keys"), r.table("managed_users"), r.table("managed_usage_monthly"))
	var stored StoredAPIKey
	var models []byte
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&stored.ID, &stored.UserID, &stored.Name, &stored.Prefix, &stored.Status, &stored.ExpiresAt, &stored.LastUsedAt, &stored.CreatedAt, &stored.SecretHash,
		&stored.User.ID, &stored.User.Name, &stored.User.Email, &stored.User.Status, &stored.User.Limits.RequestsPerMinute, &stored.User.Limits.ConcurrentRequests, &stored.User.Limits.MonthlyTokens, &models, &stored.User.ExpiresAt, &stored.User.CreatedAt, &stored.User.UpdatedAt,
		&stored.UsedTokens,
	)
	if err != nil {
		return StoredAPIKey{}, normalizeNotFound(err)
	}
	if err = json.Unmarshal(models, &stored.User.Limits.AllowedModels); err != nil {
		return StoredAPIKey{}, fmt.Errorf("managed users: decode models: %w", err)
	}
	return stored, nil
}

func (r *PostgresRepository) RevokeAPIKey(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET status=$2 WHERE id=$1`, r.table("managed_api_keys")), id, KeyStatusRevoked)
	if err != nil {
		return fmt.Errorf("managed users: revoke API key: %w", err)
	}
	return requireChanged(result)
}

func (r *PostgresRepository) RecordUsage(ctx context.Context, delta UsageDelta) error {
	query := fmt.Sprintf(`INSERT INTO %s AS usage (user_id,key_id,period_start,requests,failed,input_tokens,output_tokens,total_tokens,updated_at)
		SELECT user_id,id,DATE_TRUNC('month', NOW() AT TIME ZONE 'UTC')::date,1,$2,$3,$4,$5,NOW() FROM %s WHERE id=$1
		ON CONFLICT (user_id,key_id,period_start) DO UPDATE SET requests=usage.requests+1,failed=usage.failed+EXCLUDED.failed,input_tokens=usage.input_tokens+EXCLUDED.input_tokens,output_tokens=usage.output_tokens+EXCLUDED.output_tokens,total_tokens=usage.total_tokens+EXCLUDED.total_tokens,updated_at=NOW()`,
		r.table("managed_usage_monthly"), r.table("managed_api_keys"))
	failed := 0
	if delta.Failed {
		failed = 1
	}
	result, err := r.db.ExecContext(ctx, query, delta.KeyID, failed, delta.InputTokens, delta.OutputTokens, delta.TotalTokens)
	if err != nil {
		return fmt.Errorf("managed users: record usage: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	_, err = r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET last_used_at=NOW() WHERE id=$1`, r.table("managed_api_keys")), delta.KeyID)
	return err
}

func (r *PostgresRepository) GetUsage(ctx context.Context, userID string, periodStart time.Time) (UsageSummary, error) {
	query := fmt.Sprintf(`SELECT COALESCE(SUM(requests),0),COALESCE(SUM(failed),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(total_tokens),0) FROM %s WHERE user_id=$1 AND period_start=$2`, r.table("managed_usage_monthly"))
	summary := UsageSummary{UserID: userID, PeriodStart: periodStart}
	err := r.db.QueryRowContext(ctx, query, userID, periodStart).Scan(&summary.Requests, &summary.Failed, &summary.InputTokens, &summary.OutputTokens, &summary.TotalTokens)
	if err != nil {
		return UsageSummary{}, fmt.Errorf("managed users: get usage: %w", err)
	}
	return summary, nil
}

func (r *PostgresRepository) CreateOAuthInvite(ctx context.Context, invite OAuthInvite, tokenHash []byte) (OAuthInvite, error) {
	providers, err := json.Marshal(invite.Providers)
	if err != nil {
		return OAuthInvite{}, fmt.Errorf("OAuth invitations: encode providers: %w", err)
	}
	query := fmt.Sprintf(`INSERT INTO %s (id,token_hash,label,providers,max_uses,used_uses,reserved_uses,active,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, r.table("oauth_invitations"))
	_, err = r.db.ExecContext(ctx, query, invite.ID, tokenHash, invite.Label, providers, invite.MaxUses, invite.UsedUses, invite.ReservedUses, invite.Active, invite.ExpiresAt, invite.CreatedAt)
	if err != nil {
		return OAuthInvite{}, fmt.Errorf("OAuth invitations: create: %w", err)
	}
	return invite, nil
}

func (r *PostgresRepository) ListOAuthInvites(ctx context.Context) ([]OAuthInvite, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT id,label,providers,max_uses,used_uses,reserved_uses,active,expires_at,created_at FROM %s ORDER BY created_at DESC`, r.table("oauth_invitations")))
	if err != nil {
		return nil, fmt.Errorf("OAuth invitations: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	invites := make([]OAuthInvite, 0)
	for rows.Next() {
		invite, errScan := scanInvite(rows)
		if errScan != nil {
			return nil, errScan
		}
		invites = append(invites, invite)
	}
	return invites, rows.Err()
}

func (r *PostgresRepository) RevokeOAuthInvite(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET active=FALSE WHERE id=$1`, r.table("oauth_invitations")), id)
	if err != nil {
		return fmt.Errorf("OAuth invitations: revoke: %w", err)
	}
	return requireChanged(result)
}

func (r *PostgresRepository) GetOAuthInviteByTokenHash(ctx context.Context, tokenHash []byte) (OAuthInvite, error) {
	query := fmt.Sprintf(`SELECT id,label,providers,max_uses,used_uses,reserved_uses,active,expires_at,created_at FROM %s WHERE token_hash=$1`, r.table("oauth_invitations"))
	invite, err := scanInvite(r.db.QueryRowContext(ctx, query, tokenHash))
	return invite, normalizeNotFound(err)
}

func (r *PostgresRepository) ReserveOAuthInvite(ctx context.Context, tokenHash []byte, state, provider string) (OAuthInvite, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return OAuthInvite{}, fmt.Errorf("OAuth invitations: start transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = r.releaseStaleReservations(ctx, tx); err != nil {
		return OAuthInvite{}, err
	}
	query := fmt.Sprintf(`SELECT id,label,providers,max_uses,used_uses,reserved_uses,active,expires_at,created_at FROM %s WHERE token_hash=$1 FOR UPDATE`, r.table("oauth_invitations"))
	invite, err := scanInvite(tx.QueryRowContext(ctx, query, tokenHash))
	if err != nil || !inviteAvailable(invite, time.Now()) || !containsProvider(invite.Providers, provider) {
		return OAuthInvite{}, ErrInviteUnavailable
	}
	insert := fmt.Sprintf(`INSERT INTO %s (state,invite_id,provider,status,created_at) VALUES ($1,$2,$3,'pending',NOW())`, r.table("oauth_invite_sessions"))
	if _, err = tx.ExecContext(ctx, insert, state, invite.ID, provider); err != nil {
		return OAuthInvite{}, ErrInviteUnavailable
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET reserved_uses=reserved_uses+1 WHERE id=$1`, r.table("oauth_invitations")), invite.ID); err != nil {
		return OAuthInvite{}, fmt.Errorf("OAuth invitations: reserve use: %w", err)
	}
	invite.ReservedUses++
	if err = tx.Commit(); err != nil {
		return OAuthInvite{}, fmt.Errorf("OAuth invitations: commit reservation: %w", err)
	}
	return invite, nil
}

func (r *PostgresRepository) OAuthInviteOwnsSession(ctx context.Context, tokenHash []byte, state string) (bool, error) {
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s s JOIN %s i ON i.id=s.invite_id WHERE s.state=$1 AND i.token_hash=$2)`, r.table("oauth_invite_sessions"), r.table("oauth_invitations"))
	var owns bool
	if err := r.db.QueryRowContext(ctx, query, state, tokenHash).Scan(&owns); err != nil {
		return false, fmt.Errorf("OAuth invitations: check session: %w", err)
	}
	return owns, nil
}

func (r *PostgresRepository) CompleteOAuthInvite(ctx context.Context, state, authID, email string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("OAuth invitations: start completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var inviteID, status string
	query := fmt.Sprintf(`SELECT invite_id,status FROM %s WHERE state=$1 FOR UPDATE`, r.table("oauth_invite_sessions"))
	if err = tx.QueryRowContext(ctx, query, state).Scan(&inviteID, &status); err != nil {
		return normalizeNotFound(err)
	}
	if status == "completed" {
		return tx.Commit()
	}
	updateSession := fmt.Sprintf(`UPDATE %s SET status='completed',auth_id=$2,email=$3,completed_at=NOW() WHERE state=$1`, r.table("oauth_invite_sessions"))
	if _, err = tx.ExecContext(ctx, updateSession, state, authID, email); err != nil {
		return fmt.Errorf("OAuth invitations: complete session: %w", err)
	}
	updateInvite := fmt.Sprintf(`UPDATE %s SET reserved_uses=GREATEST(0,reserved_uses-1),used_uses=used_uses+1 WHERE id=$1`, r.table("oauth_invitations"))
	if _, err = tx.ExecContext(ctx, updateInvite, inviteID); err != nil {
		return fmt.Errorf("OAuth invitations: consume invitation: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("OAuth invitations: commit completion: %w", err)
	}
	return nil
}

func (r *PostgresRepository) releaseStaleReservations(ctx context.Context, tx *sql.Tx) error {
	cutoff := time.Now().Add(-inviteReservationLifetime)
	query := fmt.Sprintf(`UPDATE %s SET status='expired',completed_at=NOW() WHERE status='pending' AND created_at<$1 RETURNING invite_id`, r.table("oauth_invite_sessions"))
	rows, err := tx.QueryContext(ctx, query, cutoff)
	if err != nil {
		return fmt.Errorf("OAuth invitations: release stale sessions: %w", err)
	}
	counts := make(map[string]int)
	for rows.Next() {
		var inviteID string
		if errScan := rows.Scan(&inviteID); errScan != nil {
			_ = rows.Close()
			return fmt.Errorf("OAuth invitations: scan stale session: %w", errScan)
		}
		counts[inviteID]++
	}
	if err = rows.Close(); err != nil {
		return fmt.Errorf("OAuth invitations: close stale sessions: %w", err)
	}
	for inviteID, count := range counts {
		update := fmt.Sprintf(`UPDATE %s SET reserved_uses=GREATEST(0,reserved_uses-$2) WHERE id=$1`, r.table("oauth_invitations"))
		if _, err = tx.ExecContext(ctx, update, inviteID, count); err != nil {
			return fmt.Errorf("OAuth invitations: release stale reservation: %w", err)
		}
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanUser(scanner rowScanner) (User, error) {
	var user User
	var models []byte
	err := scanner.Scan(&user.ID, &user.Name, &user.Email, &user.Status, &user.Limits.RequestsPerMinute, &user.Limits.ConcurrentRequests, &user.Limits.MonthlyTokens, &models, &user.ExpiresAt, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return User{}, normalizeNotFound(err)
	}
	if err = json.Unmarshal(models, &user.Limits.AllowedModels); err != nil {
		return User{}, fmt.Errorf("managed users: decode models: %w", err)
	}
	return user, nil
}

func scanAPIKey(scanner rowScanner) (APIKey, error) {
	var key APIKey
	err := scanner.Scan(&key.ID, &key.UserID, &key.Name, &key.Prefix, &key.Status, &key.ExpiresAt, &key.LastUsedAt, &key.CreatedAt)
	if err != nil {
		return APIKey{}, normalizeNotFound(err)
	}
	return key, nil
}

func scanInvite(scanner rowScanner) (OAuthInvite, error) {
	var invite OAuthInvite
	var providers []byte
	err := scanner.Scan(&invite.ID, &invite.Label, &providers, &invite.MaxUses, &invite.UsedUses, &invite.ReservedUses, &invite.Active, &invite.ExpiresAt, &invite.CreatedAt)
	if err != nil {
		return OAuthInvite{}, normalizeNotFound(err)
	}
	if err = json.Unmarshal(providers, &invite.Providers); err != nil {
		return OAuthInvite{}, fmt.Errorf("OAuth invitations: decode providers: %w", err)
	}
	return invite, nil
}

func (r *PostgresRepository) table(name string) string {
	if r.schema == "" {
		return quoteIdentifier(name)
	}
	return quoteIdentifier(r.schema) + "." + quoteIdentifier(name)
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func normalizeNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func requireChanged(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func containsProvider(providers []string, provider string) bool {
	for _, candidate := range providers {
		if strings.EqualFold(candidate, provider) {
			return true
		}
	}
	return false
}
