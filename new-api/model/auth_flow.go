package model

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	AuthFlowPurposeOAuth             = "oauth"
	AuthFlowPurposeTwoFALogin        = "2fa_login"
	AuthFlowPurposeLoginVerification = "login_verification"
	AuthFlowPurposeLoginPasskey      = "login_passkey"
	AuthFlowPurposePasskeyLogin      = "passkey_login"
	AuthFlowPurposePasskeyRegister   = "passkey_register"
	AuthFlowPurposePasskeyStepUp     = "passkey_step_up"
	AuthFlowPurposeTelegramBind      = "telegram_bind"
	AuthFlowPurposeTelegramAssertion = "telegram_assertion"
	AuthFlowIntentLogin              = "login"
	AuthFlowIntentBind               = "bind"
	AuthFlowIntentVerify             = "verify"
	AuthFlowPurposeTwoFASetup        = "2fa_setup"
	AuthFlowPurposeSecurityProof     = "security_proof"
	AuthFlowPurposeEmailBinding      = "email_binding"
	AuthFlowTokenBytes               = 32
	AuthFlowDefaultCleanupRetention  = 24 * time.Hour
)

var (
	ErrAuthFlowInvalid  = errors.New("auth flow is invalid")
	ErrAuthFlowExpired  = errors.New("auth flow has expired")
	ErrAuthFlowConsumed = errors.New("auth flow has already been consumed")
)

// AuthFlow stores one-time, short-lived state for authentication ceremonies.
// TokenHash is an HMAC of the opaque token; the token itself is never persisted.
type AuthFlow struct {
	Id         int64      `json:"id" gorm:"primaryKey"`
	TokenHash  string     `json:"-" gorm:"type:char(64);not null;uniqueIndex"`
	Purpose    string     `json:"purpose" gorm:"type:varchar(32);not null;index:idx_auth_flow_purpose_expiry"`
	Provider   string     `json:"provider,omitempty" gorm:"type:varchar(64)"`
	Intent     string     `json:"intent,omitempty" gorm:"type:varchar(16)"`
	UserId     int        `json:"user_id,omitempty" gorm:"index"`
	SessionId  string     `json:"session_id,omitempty" gorm:"type:varchar(64);index"`
	Payload    string     `json:"-" gorm:"type:text"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at" gorm:"not null;index:idx_auth_flow_purpose_expiry"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty" gorm:"index"`
}

func (AuthFlow) TableName() string {
	return "auth_flows"
}

type AuthFlowCreate struct {
	Purpose   string
	Provider  string
	Intent    string
	UserId    int
	SessionId string
	Payload   string
	ExpiresAt time.Time
}

type AuthFlowMatch struct {
	Purpose   string
	Provider  string
	Intent    string
	UserId    int
	SessionId string
}

// AuthSessionIdentity binds an authentication flow to a specific session version.
type AuthSessionIdentity struct {
	UserID          int    `json:"user_id"`
	SessionID       string `json:"session_id"`
	UserAuthVersion int64  `json:"auth_version"`
	SessionVersion  int64  `json:"session_version"`
}

// AuthFlowAuthorization is server-owned state carried into a configuration flow
// after a proof has been consumed. ProofID is a database ID, never the proof token.
type AuthFlowAuthorization struct {
	AuthSessionIdentity
	ProofID     int64  `json:"proof_id"`
	Scope       string `json:"scope"`
	ContextHash string `json:"context_hash"`
	Method      string `json:"method"`
}

// ValidateAuthSessionWithTx rechecks the authoritative identity while holding the
// user/session locks until the caller's credential change or flow consumption commits.
func ValidateAuthSessionWithTx(tx *gorm.DB, identity AuthSessionIdentity) error {
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 || identity.SessionVersion <= 0 {
		return ErrUserSessionInactive
	}
	var user User
	if err := lockForUpdate(tx).First(&user, identity.UserID).Error; err != nil {
		return err
	}
	if user.Status != common.UserStatusEnabled || user.AuthVersion != identity.UserAuthVersion {
		return ErrUserSessionInactive
	}
	var session UserSession
	if err := lockForUpdate(tx).Where("sid = ? AND user_id = ?", identity.SessionID, identity.UserID).First(&session).Error; err != nil {
		return err
	}
	if session.Status != UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() || session.UserAuthVersion != identity.UserAuthVersion || session.Version != identity.SessionVersion {
		return ErrUserSessionInactive
	}
	return nil
}

func applyAuthFlowMatch(query *gorm.DB, token string, match AuthFlowMatch) *gorm.DB {
	query = query.Where("token_hash = ? AND purpose = ?", authFlowTokenHash(token), match.Purpose)
	if match.Provider != "" {
		query = query.Where("provider = ?", match.Provider)
	}
	if match.Intent != "" {
		query = query.Where("intent = ?", match.Intent)
	}
	if match.UserId != 0 {
		query = query.Where("user_id = ?", match.UserId)
	}
	if match.SessionId != "" {
		query = query.Where("session_id = ?", match.SessionId)
	}
	return query
}

func authFlowTokenHash(token string) string {
	return common.GenerateHMACWithKey([]byte("auth-flow-v1:"+common.SessionSecret), token)
}

func CreateAuthFlow(input AuthFlowCreate) (string, *AuthFlow, error) {
	return createAuthFlowWithTx(DB, input)
}

func createAuthFlowWithTx(tx *gorm.DB, input AuthFlowCreate) (string, *AuthFlow, error) {
	if strings.TrimSpace(input.Purpose) == "" || input.ExpiresAt.IsZero() || !input.ExpiresAt.After(time.Now()) {
		return "", nil, ErrAuthFlowInvalid
	}
	random := make([]byte, AuthFlowTokenBytes)
	if _, err := rand.Read(random); err != nil {
		return "", nil, fmt.Errorf("generate auth flow token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	flow := &AuthFlow{
		TokenHash: authFlowTokenHash(token),
		Purpose:   input.Purpose,
		Provider:  input.Provider,
		Intent:    input.Intent,
		UserId:    input.UserId,
		SessionId: input.SessionId,
		Payload:   input.Payload,
		ExpiresAt: input.ExpiresAt,
	}
	if err := tx.Create(flow).Error; err != nil {
		return "", nil, err
	}
	return token, flow, nil
}

// ClaimExternalAuthAssertion records a signed provider assertion as consumed.
// The assertion is HMACed before storage and the unique token_hash index makes
// replay rejection atomic on SQLite, MySQL and PostgreSQL.
func ClaimExternalAuthAssertion(purpose, assertion string, expiresAt time.Time) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return ClaimExternalAuthAssertionWithTx(tx, purpose, assertion, expiresAt)
	})
}

// ClaimExternalAuthAssertionWithTx records a provider assertion in the
// caller's transaction so replay protection can commit atomically with the
// authentication flow and its resulting state change.
func ClaimExternalAuthAssertionWithTx(tx *gorm.DB, purpose, assertion string, expiresAt time.Time) error {
	purpose = strings.TrimSpace(purpose)
	assertion = strings.TrimSpace(assertion)
	now := time.Now()
	if tx == nil || purpose == "" || assertion == "" || !expiresAt.After(now) {
		return ErrAuthFlowInvalid
	}
	flow := AuthFlow{
		TokenHash:  authFlowTokenHash("external:" + purpose + ":" + assertion),
		Purpose:    purpose,
		ExpiresAt:  expiresAt,
		ConsumedAt: &now,
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "token_hash"}},
		DoNothing: true,
	}).Create(&flow)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAuthFlowConsumed
	}
	return nil
}

// GetAuthFlow validates a flow without consuming it. Callers must still use
// ConsumeAuthFlow with all identity-bound fields before performing the action.
func GetAuthFlow(token string, match AuthFlowMatch) (*AuthFlow, error) {
	if token == "" || match.Purpose == "" {
		return nil, ErrAuthFlowInvalid
	}
	var flow AuthFlow
	if err := applyAuthFlowMatch(DB, token, match).First(&flow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAuthFlowInvalid
		}
		return nil, err
	}
	if flow.ConsumedAt != nil {
		return nil, ErrAuthFlowConsumed
	}
	if !flow.ExpiresAt.After(time.Now()) {
		return nil, ErrAuthFlowExpired
	}
	return &flow, nil
}

// ConsumeAuthFlow atomically validates and consumes a flow. Optional match
// fields are enforced when non-zero so tokens cannot cross purposes or users.
func ConsumeAuthFlow(token string, match AuthFlowMatch) (*AuthFlow, error) {
	return ConsumeAuthFlowWithAction(token, match, nil)
}

// ConsumeAuthFlowWithAction consumes a flow and runs action in the same
// database transaction. An action failure rolls the consumption back.
func ConsumeAuthFlowWithAction(token string, match AuthFlowMatch, action func(tx *gorm.DB, flow *AuthFlow) error) (*AuthFlow, error) {
	if token == "" || match.Purpose == "" {
		return nil, ErrAuthFlowInvalid
	}
	var consumed AuthFlow
	err := DB.Transaction(func(tx *gorm.DB) error {
		// Claim with the first write, rather than upgrading a prior read lock.
		// SQLite cannot reliably upgrade two concurrent deferred read transactions.
		now := time.Now()
		result := applyAuthFlowMatch(tx.Model(&AuthFlow{}), token, match).
			Where("consumed_at IS NULL AND expires_at > ?", now).
			Update("consumed_at", now)
		if result.Error != nil {
			return result.Error
		}
		query := applyAuthFlowMatch(tx, token, match)
		if err := query.First(&consumed).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAuthFlowInvalid
			}
			return err
		}
		if result.RowsAffected != 1 && consumed.ConsumedAt != nil {
			return ErrAuthFlowConsumed
		}
		if !consumed.ExpiresAt.After(time.Now()) {
			return ErrAuthFlowExpired
		}
		if result.RowsAffected != 1 {
			return ErrAuthFlowInvalid
		}
		if action != nil {
			if err := action(tx, &consumed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &consumed, nil
}

func DeleteExpiredAuthFlows(now time.Time) error {
	cutoff := now.Add(-AuthFlowDefaultCleanupRetention)
	return DB.Where("expires_at < ? OR (consumed_at IS NOT NULL AND consumed_at < ?)", cutoff, cutoff).
		Delete(&AuthFlow{}).Error
}
