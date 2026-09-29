package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var (
	ErrCurrentPasswordInvalid = errors.New("Current password is incorrect.")
	ErrAccountPasswordState   = errors.New("Password settings have changed. Reload the page and try again.")
	ErrLastLoginMethod        = errors.New("Add another login method before unlinking this account.")
	ErrAccountBindingChanged  = errors.New("Account bindings have changed. Start this operation again.")
	ErrCannotDeleteRootUser   = errors.New("The root account cannot be deleted.")
)

// ChangeUserPassword rechecks the authorized session, password state and current
// password under the same user lock as the credential update. Only self-profile
// fields are accepted; a stale snapshot cannot restore roles or account status.
func ChangeUserPassword(identity AuthSessionIdentity, update *User, firstPassword bool) error {
	hash, err := common.HashAccountPassword(update.Password)
	if err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, identity); err != nil {
			return err
		}
		var current User
		if err := tx.First(&current, identity.UserID).Error; err != nil {
			return err
		}
		if firstPassword != (current.Password == "") {
			return ErrAccountPasswordState
		}
		if !firstPassword && !common.ValidatePasswordAndHash(update.OriginalPassword, current.Password) {
			return ErrCurrentPasswordInvalid
		}
		if current.Password != "" && common.ValidatePasswordAndHash(update.Password, current.Password) {
			return common.ErrAccountPasswordSame
		}
		changes := map[string]any{"password": hash}
		if update.Username != "" {
			changes["username"] = update.Username
		}
		if update.DisplayName != "" {
			changes["display_name"] = update.DisplayName
		}
		if _, err := IncrementUserAuthVersionWithTx(tx, identity.UserID); err != nil {
			return err
		}
		if err := tx.Model(&User{}).Where("id = ?", identity.UserID).Updates(changes).Error; err != nil {
			return err
		}
		return tx.First(update, identity.UserID).Error
	})
}

// AccountLoginMethods is a snapshot of administrator-enabled login mechanisms.
// Enrollment is checked inside the transaction, not trusted from this snapshot.
type AccountLoginMethods struct {
	Password          bool
	Passkey           bool
	WeChat            bool
	OAuthColumns      []string
	CustomProviderIDs []int
}

func UnbindUserOAuthForSession(identity AuthSessionIdentity, providerID int, enabled AccountLoginMethods) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, identity); err != nil {
			return err
		}
		var current User
		if err := tx.First(&current, identity.UserID).Error; err != nil {
			return err
		}
		var binding UserOAuthBinding
		if err := tx.Where("user_id = ? AND provider_id = ?", identity.UserID, providerID).First(&binding).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountBindingChanged
			}
			return err
		}
		hasLogin := enabled.Password && current.Password != "" || enabled.WeChat && current.WeChatId != ""
		columns := map[string]string{
			"github_id": current.GitHubId, "discord_id": current.DiscordId,
			"oidc_id": current.OidcId, "linux_do_id": current.LinuxDOId, "telegram_id": current.TelegramId,
		}
		for _, column := range enabled.OAuthColumns {
			hasLogin = hasLogin || columns[column] != ""
		}
		if !hasLogin && enabled.Passkey {
			var count int64
			if err := tx.Model(&PasskeyCredential{}).Where("user_id = ?", identity.UserID).Count(&count).Error; err != nil {
				return err
			}
			hasLogin = count > 0
		}
		if !hasLogin && len(enabled.CustomProviderIDs) > 0 {
			var count int64
			if err := tx.Model(&UserOAuthBinding{}).Where("user_id = ? AND provider_id <> ? AND provider_id IN ?", identity.UserID, providerID, enabled.CustomProviderIDs).Count(&count).Error; err != nil {
				return err
			}
			hasLogin = count > 0
		}
		if !hasLogin {
			return ErrLastLoginMethod
		}
		return tx.Delete(&binding).Error
	})
}

func UpdateUserBindColumnForSessionWithTx(tx *gorm.DB, identity AuthSessionIdentity, column, value string) error {
	if !userBindColumns[column] || value == "" {
		return ErrAccountBindingChanged
	}
	if err := ValidateAuthSessionWithTx(tx, identity); err != nil {
		return err
	}
	// Preserve the existing ownership check within the transaction and update
	// only the chosen binding column, never a complete user snapshot.
	var count int64
	if err := tx.Model(&User{}).Where(column+" = ? AND id <> ?", value, identity.UserID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrExternalIdentityAlreadyClaimed
	}
	return tx.Model(&User{}).Where("id = ?", identity.UserID).Update(column, value).Error
}

func UpdateUserOAuthBindingForSessionWithTx(tx *gorm.DB, identity AuthSessionIdentity, providerID int, subject string) error {
	if providerID <= 0 || subject == "" || len(subject) > 256 {
		return ErrAccountBindingChanged
	}
	if err := ValidateAuthSessionWithTx(tx, identity); err != nil {
		return err
	}
	var binding UserOAuthBinding
	err := tx.Where("user_id = ? AND provider_id = ?", identity.UserID, providerID).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CreateUserOAuthBindingWithTx(tx, &UserOAuthBinding{UserId: identity.UserID, ProviderId: providerID, ProviderUserId: subject})
	}
	if err != nil {
		return err
	}
	// The existing unique provider/subject index rejects concurrent ownership.
	return tx.Model(&binding).Update("provider_user_id", subject).Error
}

// MigrateLegacyGitHubBindingWithTx rewrites a login-name GitHub binding to the
// numeric account ID once the login flow has confirmed the account. It reports
// whether the row was rewritten; a row whose binding changed since it was read
// is left untouched.
func MigrateLegacyGitHubBindingWithTx(tx *gorm.DB, userID int, legacyID, gitHubID string) (bool, error) {
	if userID <= 0 || legacyID == "" || gitHubID == "" {
		return false, ErrAccountBindingChanged
	}
	var count int64
	if err := tx.Model(&User{}).Where("github_id = ? AND id <> ?", gitHubID, userID).Count(&count).Error; err != nil {
		return false, err
	}
	if count != 0 {
		return false, ErrExternalIdentityAlreadyClaimed
	}
	result := tx.Model(&User{}).Where("id = ? AND github_id = ?", userID, legacyID).Update("github_id", gitHubID)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}
