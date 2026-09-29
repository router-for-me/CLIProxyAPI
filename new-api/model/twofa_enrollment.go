package model

import (
	"crypto/hmac"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrTwoFASetupInvalid = errors.New("The two-factor setup has expired or changed. Start setup again.")
var ErrTwoFACodeInvalid = errors.New("The authenticator code is incorrect.")

type twoFAEnrollmentPayload struct {
	TwoFAID       int                    `json:"twofa_id"`
	SecretHash    string                 `json:"secret_hash"`
	Authorization *AuthFlowAuthorization `json:"authorization"`
}

// CreateTwoFAEnrollment stores the pending credential, recovery codes and
// session-bound flow atomically. Reinitialization invalidates the previous setup.
func CreateTwoFAEnrollment(identity AuthSessionIdentity, authorization *AuthFlowAuthorization, secret string, backupCodes []string, expiresAt time.Time) (string, error) {
	if authorization == nil || authorization.ProofID <= 0 || authorization.AuthSessionIdentity != identity {
		return "", ErrTwoFASetupInvalid
	}
	var token string
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, identity); err != nil {
			return err
		}
		var existing TwoFA
		err := lockForUpdate(tx).Where("user_id = ?", identity.UserID).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			if existing.IsEnabled {
				return ErrTwoFAAlreadyEnabled
			}
			if err := tx.Unscoped().Delete(&existing).Error; err != nil {
				return err
			}
		}
		pending := &TwoFA{UserId: identity.UserID, Secret: secret}
		if err := tx.Create(pending).Error; err != nil {
			return err
		}
		if err := replaceBackupCodesWithTx(tx, identity.UserID, backupCodes); err != nil {
			return err
		}
		payload, err := common.Marshal(twoFAEnrollmentPayload{
			TwoFAID: pending.Id, SecretHash: common.GenerateHMACWithKey([]byte("twofa-enrollment:"+common.SessionSecret), secret),
			Authorization: authorization,
		})
		if err != nil {
			return err
		}
		token, _, err = createAuthFlowWithTx(tx, AuthFlowCreate{
			Purpose: AuthFlowPurposeTwoFASetup, UserId: identity.UserID, SessionId: identity.SessionID,
			Payload: string(payload), ExpiresAt: expiresAt,
		})
		return err
	})
	return token, err
}

// EnableTwoFAEnrollment validates and consumes one setup in the same transaction
// as factor activation and auth_version advancement. A bad code can be retried.
func EnableTwoFAEnrollment(identity AuthSessionIdentity, token, code string) error {
	cleanCode, err := common.ValidateNumericCode(code)
	if err != nil {
		return ErrTwoFACodeInvalid
	}
	_, err = ConsumeAuthFlowWithAction(token, AuthFlowMatch{
		Purpose: AuthFlowPurposeTwoFASetup, UserId: identity.UserID, SessionId: identity.SessionID,
	}, func(tx *gorm.DB, flow *AuthFlow) error {
		var payload twoFAEnrollmentPayload
		if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
			return err
		}
		if payload.Authorization == nil || payload.Authorization.ProofID <= 0 || payload.Authorization.AuthSessionIdentity != identity {
			return ErrTwoFASetupInvalid
		}
		if err := ValidateAuthSessionWithTx(tx, identity); err != nil {
			return err
		}
		var pending TwoFA
		if err := lockForUpdate(tx).Where("id = ? AND user_id = ? AND is_enabled = ?", payload.TwoFAID, identity.UserID, false).First(&pending).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTwoFASetupInvalid
			}
			return err
		}
		secretHash := common.GenerateHMACWithKey([]byte("twofa-enrollment:"+common.SessionSecret), pending.Secret)
		if !hmac.Equal([]byte(payload.SecretHash), []byte(secretHash)) {
			return ErrTwoFASetupInvalid
		}
		if !common.ValidateTOTPCode(pending.Secret, cleanCode) {
			return ErrTwoFACodeInvalid
		}
		if _, err := IncrementUserAuthVersionWithTx(tx, identity.UserID); err != nil {
			return err
		}
		return tx.Model(&pending).Updates(map[string]any{"is_enabled": true, "failed_attempts": 0, "locked_until": nil}).Error
	})
	if errors.Is(err, ErrAuthFlowInvalid) || errors.Is(err, ErrAuthFlowExpired) || errors.Is(err, ErrAuthFlowConsumed) {
		return ErrTwoFASetupInvalid
	}
	if err != nil {
		return err
	}
	return PublishUserAuthCache(identity.UserID)
}
