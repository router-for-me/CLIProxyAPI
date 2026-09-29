package model

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	EmailBindingTTL         = 10 * time.Minute
	EmailBindingResendDelay = time.Minute
	EmailBindingMaxAttempts = 5
)

var (
	ErrEmailBindingCodeInvalid = errors.New("Email verification code is incorrect.")
	ErrEmailBindingLocked      = errors.New("Too many incorrect codes. Start email verification again.")
	ErrEmailBindingResendWait  = errors.New("Please wait before requesting another verification code.")
)

// EmailBindingState is server-owned payload. It deliberately contains only
// salted code hashes and an already-consumed authorization, never usable codes
// or proof tokens. Resending must retain the deadline and failure counter.
type EmailBindingState struct {
	Authorization  *AuthFlowAuthorization `json:"authorization"`
	CurrentEmail   string                 `json:"current_email"`
	Email          string                 `json:"email"`
	NewCodeHash    string                 `json:"new_code_hash"`
	OldCodeHash    string                 `json:"old_code_hash,omitempty"`
	FailedAttempts int                    `json:"failed_attempts"`
	ResendAt       int64                  `json:"resend_at"`
}

func CreateEmailBinding(identity AuthSessionIdentity, state EmailBindingState) (string, *AuthFlow, error) {
	var token string
	var flow *AuthFlow
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := validateEmailBindingAccountWithTx(tx, identity, &state); err != nil {
			return err
		}
		if err := ensureEmailAvailableWithTx(tx, state.Email, identity.UserID); err != nil {
			return err
		}
		payload, err := common.Marshal(state)
		if err != nil {
			return err
		}
		token, flow, err = createAuthFlowWithTx(tx, AuthFlowCreate{
			Purpose: AuthFlowPurposeEmailBinding, UserId: identity.UserID, SessionId: identity.SessionID,
			Payload: string(payload), ExpiresAt: time.Now().Add(EmailBindingTTL),
		})
		return err
	})
	return token, flow, err
}

func GetEmailBinding(identity AuthSessionIdentity, token string) (*AuthFlow, *EmailBindingState, error) {
	flow, err := GetAuthFlow(token, AuthFlowMatch{Purpose: AuthFlowPurposeEmailBinding, UserId: identity.UserID, SessionId: identity.SessionID})
	if err != nil {
		return nil, nil, err
	}
	var state EmailBindingState
	if err := common.UnmarshalJsonStr(flow.Payload, &state); err != nil {
		return nil, nil, ErrAuthFlowInvalid
	}
	return flow, &state, nil
}

func ResendEmailBinding(identity AuthSessionIdentity, token, newCodeHash, oldCodeHash string) (*AuthFlow, *EmailBindingState, error) {
	var flow *AuthFlow
	var state *EmailBindingState
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		flow, state, err = lockEmailBindingWithTx(tx, identity, token)
		if err != nil {
			return err
		}
		now := time.Now()
		if state.ResendAt > now.Unix() {
			return ErrEmailBindingResendWait
		}
		if newCodeHash == "" || (state.OldCodeHash != "") != (oldCodeHash != "") {
			return ErrAuthFlowInvalid
		}
		state.NewCodeHash, state.OldCodeHash = newCodeHash, oldCodeHash
		state.ResendAt = now.Add(EmailBindingResendDelay).Unix()
		payload, err := common.Marshal(state)
		if err != nil {
			return err
		}
		return tx.Model(flow).Update("payload", string(payload)).Error
	})
	return flow, state, err
}

// CompleteEmailBinding commits failed attempts even though verification fails.
// Successful confirmation consumes the flow in the same transaction as the
// email update, preserving the existing cross-database email ownership lock.
func CompleteEmailBinding(identity AuthSessionIdentity, token, email, newCode, oldCode string) (*EmailBindingState, error) {
	var state *EmailBindingState
	var verificationError error
	err := DB.Transaction(func(tx *gorm.DB) error {
		// Acquire ownership protection before the first consistent read. Under
		// MySQL REPEATABLE READ, a snapshot created before this lock could hide
		// an address claimed by a concurrent transaction while we were waiting.
		if email == "" || email != NormalizeEmail(email) {
			return ErrAuthFlowInvalid
		}
		if err := lockNormalizedEmail(tx, email); err != nil {
			return err
		}
		flow, lockedState, err := lockEmailBindingWithTx(tx, identity, token)
		if err != nil {
			return err
		}
		state = lockedState
		if state.Email != email {
			return ErrAuthFlowInvalid
		}
		newCode, newCodeError := common.ValidateNumericCode(newCode)
		oldCode, oldCodeError := common.ValidateNumericCode(oldCode)
		newValid := newCodeError == nil && common.ValidatePasswordAndHash(newCode, state.NewCodeHash)
		oldValid := state.OldCodeHash == "" || oldCodeError == nil && common.ValidatePasswordAndHash(oldCode, state.OldCodeHash)
		if !newValid || !oldValid {
			state.FailedAttempts++
			verificationError = ErrEmailBindingCodeInvalid
			if state.FailedAttempts >= EmailBindingMaxAttempts {
				verificationError = ErrEmailBindingLocked
			}
			payload, err := common.Marshal(state)
			if err != nil {
				return err
			}
			return tx.Model(flow).Update("payload", string(payload)).Error
		}
		if err := ensureEmailAvailableWithTx(tx, state.Email, identity.UserID); err != nil {
			return err
		}
		if err := tx.Model(&User{}).Where("id = ?", identity.UserID).Update("email", state.Email).Error; err != nil {
			return err
		}
		return tx.Model(flow).Update("consumed_at", time.Now()).Error
	})
	if err != nil {
		return nil, err
	}
	return state, verificationError
}

func lockEmailBindingWithTx(tx *gorm.DB, identity AuthSessionIdentity, token string) (*AuthFlow, *EmailBindingState, error) {
	match := AuthFlowMatch{Purpose: AuthFlowPurposeEmailBinding, UserId: identity.UserID, SessionId: identity.SessionID}
	// Make the first statement a write so SQLite does not have to upgrade a
	// deferred read transaction. A no-op update also locks the row on MySQL and
	// PostgreSQL; do not interpret dialect-dependent RowsAffected as success.
	if err := applyAuthFlowMatch(tx.Model(&AuthFlow{}), token, match).
		Where("consumed_at IS NULL AND expires_at > ?", time.Now()).
		UpdateColumn("payload", gorm.Expr("payload")).Error; err != nil {
		return nil, nil, err
	}
	var flow AuthFlow
	if err := applyAuthFlowMatch(tx, token, match).First(&flow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrAuthFlowInvalid
		}
		return nil, nil, err
	}
	if flow.ConsumedAt != nil {
		return nil, nil, ErrAuthFlowConsumed
	}
	if !flow.ExpiresAt.After(time.Now()) {
		return nil, nil, ErrAuthFlowExpired
	}
	var state EmailBindingState
	if err := common.UnmarshalJsonStr(flow.Payload, &state); err != nil {
		return nil, nil, ErrAuthFlowInvalid
	}
	if err := validateEmailBindingAccountWithTx(tx, identity, &state); err != nil {
		return nil, nil, err
	}
	if state.FailedAttempts >= EmailBindingMaxAttempts {
		return nil, nil, ErrEmailBindingLocked
	}
	return &flow, &state, nil
}

func validateEmailBindingAccountWithTx(tx *gorm.DB, identity AuthSessionIdentity, state *EmailBindingState) error {
	if state.Authorization == nil || state.Authorization.ProofID <= 0 || state.Authorization.AuthSessionIdentity != identity ||
		state.Authorization.Scope != "account.binding.bind" || state.Authorization.ContextHash == "" ||
		state.Email == "" || state.Email != NormalizeEmail(state.Email) || state.NewCodeHash == "" {
		return ErrAuthFlowInvalid
	}
	if err := ValidateAuthSessionWithTx(tx, identity); err != nil {
		return err
	}
	var user User
	if err := tx.Select("email").First(&user, identity.UserID).Error; err != nil {
		return err
	}
	if NormalizeEmail(user.Email) != state.CurrentEmail || state.CurrentEmail == state.Email {
		return ErrAccountBindingChanged
	}
	return nil
}
