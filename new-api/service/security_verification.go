package service

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
)

const (
	VerificationMethodTwoFA              = "2fa"
	VerificationMethodPasskey            = "passkey"
	VerificationMethodPassword           = "password"
	VerificationMethodOAuth              = "oauth"
	VerificationMethodSession            = "session"
	VerificationScopeChannelKeyRead      = "channel.key.read"
	VerificationScopePasskeyRegister     = "passkey.register"
	VerificationScopePasskeyDelete       = "passkey.delete"
	VerificationScopeTwoFASetup          = "2fa.setup"
	VerificationScopeTwoFADisable        = "2fa.disable"
	VerificationScopeTwoFABackupCodes    = "2fa.backup_codes.regenerate"
	VerificationScopeLogin               = "auth.login"
	VerificationScopeAccessTokenGenerate = "access_token.generate"
	VerificationScopeAccessTokenRevoke   = "access_token.revoke"
	VerificationScopeAccountBind         = "account.binding.bind"
	VerificationScopeAccountUnbind       = "account.binding.unbind"
	VerificationScopePasswordSet         = "account.password.set"
	VerificationScopePasswordChange      = "account.password.change"
	VerificationScopeAccountDelete       = "account.delete"
)

var (
	ErrVerificationFailed         = errors.New("Verification failed. Please try again.")
	ErrVerificationUnavailable    = errors.New("This verification method is currently unavailable.")
	ErrVerificationLocked         = errors.New("Two-factor authentication is temporarily locked.")
	ErrVerificationFlowRequired   = errors.New("This verification method requires its dedicated verification flow.")
	ErrVerificationContextInvalid = errors.New("The action details are invalid.")
	ErrVerificationForbidden      = errors.New("You do not have permission to perform this action.")
)

// VerificationOperation describes the action being authorized, independently of
// its authentication method. Each scope owns a strict, normalized context schema.
type VerificationOperation struct {
	Scope   string          `json:"scope"`
	Context json.RawMessage `json:"context,omitempty"`
}

type ChannelKeyReadContext struct {
	ChannelID int `json:"channel_id"`
}

type AccountBindingContext struct {
	Provider string `json:"provider"`
	Email    string `json:"email,omitempty"`
	Code     string `json:"code,omitempty"`
}

type AccountUnbindingContext struct {
	ProviderID int `json:"provider_id"`
}

// VerificationBinding contains no original operation parameters. It can safely
// travel through a signed proof or a server-owned interactive verification flow.
type VerificationBinding struct {
	Scope       string `json:"scope"`
	ContextHash string `json:"context_hash"`
}

func BindVerificationOperation(operation VerificationOperation) (VerificationBinding, error) {
	var fields map[string]json.RawMessage
	if len(operation.Context) > 0 {
		if common.GetJsonType(operation.Context) != "object" || common.Unmarshal(operation.Context, &fields) != nil {
			return VerificationBinding{}, ErrVerificationContextInvalid
		}
	}
	var normalized any
	switch operation.Scope {
	case VerificationScopeChannelKeyRead:
		var context ChannelKeyReadContext
		if len(fields) != 1 || common.Unmarshal(fields["channel_id"], &context.ChannelID) != nil || context.ChannelID <= 0 {
			return VerificationBinding{}, ErrVerificationContextInvalid
		}
		normalized = context
	case VerificationScopeAccountBind:
		var context AccountBindingContext
		if common.Unmarshal(operation.Context, &context) != nil {
			return VerificationBinding{}, ErrVerificationContextInvalid
		}
		context.Provider = strings.TrimSpace(context.Provider)
		switch context.Provider {
		case "email":
			context.Email = model.NormalizeEmail(context.Email)
			if len(fields) != 2 || common.Validate.Var(context.Email, "required,email") != nil || context.Code != "" {
				return VerificationBinding{}, ErrVerificationContextInvalid
			}
		case "wechat":
			context.Code = strings.TrimSpace(context.Code)
			if len(fields) != 2 || context.Code == "" || len(context.Code) > 128 || context.Email != "" {
				return VerificationBinding{}, ErrVerificationContextInvalid
			}
		default:
			if len(fields) != 1 || context.Provider == "" || len(context.Provider) > 64 || oauth.GetProvider(context.Provider) == nil {
				return VerificationBinding{}, ErrVerificationContextInvalid
			}
		}
		normalized = context
	case VerificationScopeAccountUnbind:
		var context AccountUnbindingContext
		if len(fields) != 1 || common.Unmarshal(fields["provider_id"], &context.ProviderID) != nil || context.ProviderID <= 0 {
			return VerificationBinding{}, ErrVerificationContextInvalid
		}
		normalized = context
	case VerificationScopePasskeyRegister, VerificationScopePasskeyDelete, VerificationScopeTwoFASetup,
		VerificationScopeTwoFADisable, VerificationScopeTwoFABackupCodes,
		VerificationScopeAccessTokenGenerate, VerificationScopeAccessTokenRevoke,
		VerificationScopePasswordSet, VerificationScopePasswordChange, VerificationScopeAccountDelete:
		if len(fields) != 0 {
			return VerificationBinding{}, ErrVerificationContextInvalid
		}
		normalized = struct{}{}
	default:
		return VerificationBinding{}, ErrProofScope
	}
	payload, err := common.Marshal(struct {
		Scope   string `json:"scope"`
		Context any    `json:"context"`
	}{Scope: operation.Scope, Context: normalized})
	if err != nil {
		return VerificationBinding{}, err
	}
	return VerificationBinding{
		Scope:       operation.Scope,
		ContextHash: common.GenerateHMACWithKey(authSigningKey("verification-context"), string(payload)),
	}, nil
}

type SecurityProof struct {
	ProofToken string `json:"proof_token"`
	ExpiresAt  int64  `json:"expires_at"`
	Method     string `json:"method"`
	Scope      string `json:"scope"`
}

type VerificationMethodOption struct {
	Method    string `json:"method"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type VerificationOAuthProvider struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	UserID string `json:"-"`
}

type VerificationRequirements struct {
	Scope                     string                      `json:"scope"`
	Methods                   []VerificationMethodOption  `json:"methods"`
	OAuthProviders            []VerificationOAuthProvider `json:"oauth_providers"`
	PasswordEncryptionEnabled bool                        `json:"password_encryption_enabled"`
}

// securityVerificationPolicy is the only operation-to-method policy. Device
// support and disabled providers never turn an enrolled factor into an absent one.
func securityVerificationPolicy(scope string, state model.UserVerificationState) ([]VerificationMethodOption, error) {
	var methods []string
	if state.HasTwoFA {
		methods = append(methods, VerificationMethodTwoFA)
	}
	if state.HasPasskey {
		methods = append(methods, VerificationMethodPasskey)
	}
	switch scope {
	case VerificationScopeChannelKeyRead, VerificationScopePasskeyDelete, VerificationScopeLogin:
	case VerificationScopeTwoFADisable, VerificationScopeTwoFABackupCodes:
		if !state.HasTwoFA {
			return nil, model.ErrTwoFANotEnabled
		}
	case VerificationScopePasskeyRegister, VerificationScopeTwoFASetup,
		VerificationScopeAccessTokenGenerate, VerificationScopeAccessTokenRevoke,
		VerificationScopeAccountBind, VerificationScopeAccountUnbind,
		VerificationScopePasswordSet, VerificationScopePasswordChange, VerificationScopeAccountDelete:
		if scope == VerificationScopeAccountDelete && state.Role == common.RoleRootUser {
			return nil, ErrVerificationForbidden
		}
		if scope == VerificationScopeTwoFASetup && state.HasTwoFA {
			return nil, model.ErrTwoFAAlreadyEnabled
		}
		if (scope == VerificationScopePasswordSet && state.HasPassword) || (scope == VerificationScopePasswordChange && !state.HasPassword) {
			return nil, ErrVerificationForbidden
		}
		if len(methods) == 0 {
			if state.HasPassword {
				methods = []string{VerificationMethodPassword}
			} else {
				methods = []string{VerificationMethodOAuth}
			}
		}
	default:
		return nil, ErrProofScope
	}
	options := make([]VerificationMethodOption, 0, len(methods))
	for _, method := range methods {
		option := VerificationMethodOption{Method: method, Available: true}
		if method == VerificationMethodTwoFA && state.TwoFALocked {
			option.Available, option.Reason = false, ErrVerificationLocked.Error()
		}
		if !system_setting.PasskeySettingsSnapshot().Enabled && (method == VerificationMethodPasskey || scope == VerificationScopePasskeyRegister) {
			option.Available, option.Reason = false, "Passkey authentication is disabled."
		}
		options = append(options, option)
	}
	return options, nil
}

func GetVerificationRequirements(identity AuthIdentity, scope string) (*VerificationRequirements, error) {
	if scope == VerificationScopeLogin {
		return nil, ErrProofScope
	}
	state, err := model.GetUserVerificationState(identity.UserID)
	if err != nil {
		return nil, err
	}
	if state.Status != common.UserStatusEnabled || state.AuthVersion != identity.UserAuthVersion {
		return nil, ErrAuthTokenInvalid
	}
	if scope == VerificationScopeChannelKeyRead && state.Role != common.RoleRootUser {
		return nil, ErrVerificationForbidden
	}
	methods, err := securityVerificationPolicy(scope, *state)
	if err != nil {
		return nil, err
	}
	requirements := &VerificationRequirements{Scope: scope, Methods: methods, OAuthProviders: []VerificationOAuthProvider{}, PasswordEncryptionEnabled: common.PasswordLoginEncryptionEnabled}
	for i := range methods {
		if methods[i].Method == VerificationMethodPassword && !common.PasswordLoginEnabled {
			switch scope {
			case VerificationScopeAccountBind, VerificationScopeAccountUnbind, VerificationScopePasswordSet, VerificationScopePasswordChange, VerificationScopeAccountDelete:
				methods[i].Available, methods[i].Reason = false, "Password authentication is disabled."
			}
		}
		if methods[i].Method != VerificationMethodOAuth {
			continue
		}
		user, err := model.GetUserById(identity.UserID, false)
		if err != nil {
			return nil, err
		}
		requirements.OAuthProviders, err = verificationOAuthProviders(user)
		if err != nil {
			return nil, err
		}
		if len(requirements.OAuthProviders) == 0 {
			methods[i].Available, methods[i].Reason = false, "No linked OAuth provider is available."
			if user.TelegramId != "" {
				if err := oauth.TelegramConfigurationError(); err != nil {
					methods[i].Reason = err.Error()
				}
			}
		}
	}
	return requirements, nil
}

func verificationOAuthProviders(user *model.User) ([]VerificationOAuthProvider, error) {
	bindings := map[int]string{}
	if len(oauth.GetEnabledCustomProviders()) > 0 {
		stored, err := model.GetUserOAuthBindingsByUserId(user.Id)
		if err != nil {
			return nil, err
		}
		for _, binding := range stored {
			bindings[binding.ProviderId] = binding.ProviderUserId
		}
	}
	providers := []VerificationOAuthProvider{}
	for slug, provider := range oauth.GetAllProviders() {
		if !provider.IsEnabled() {
			continue
		}
		var userID string
		if custom, ok := provider.(*oauth.GenericOAuthProvider); ok {
			userID = bindings[custom.GetProviderId()]
		} else {
			switch provider.ProviderUserIDColumn() {
			case "github_id":
				userID = user.GitHubId
			case "discord_id":
				userID = user.DiscordId
			case "oidc_id":
				userID = user.OidcId
			case "linux_do_id":
				userID = user.LinuxDOId
			case "telegram_id":
				userID = user.TelegramId
			}
		}
		if userID != "" {
			providers = append(providers, VerificationOAuthProvider{Slug: slug, Name: provider.GetName(), UserID: userID})
		}
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Slug < providers[j].Slug })
	return providers, nil
}

func RequireVerificationMethod(identity AuthIdentity, scope, method string) (*VerificationRequirements, error) {
	requirements, err := GetVerificationRequirements(identity, scope)
	if err != nil {
		return nil, err
	}
	for _, option := range requirements.Methods {
		if option.Method != method {
			continue
		}
		if !option.Available {
			return nil, ErrVerificationUnavailable
		}
		return requirements, nil
	}
	return nil, ErrProofMethod
}

// CompleteSecurityVerification runs after the concrete authentication ceremony.
// Recheck the session and policy after potentially slow external authentication.
func CompleteSecurityVerification(identity AuthIdentity, binding VerificationBinding, method string) (*SecurityProof, error) {
	if _, _, err := ValidateLoginSession(identity); err != nil {
		return nil, err
	}
	if _, err := RequireVerificationMethod(identity, binding.Scope, method); err != nil {
		return nil, err
	}
	token, expiresAt, err := IssueSecurityProof(identity, method, binding)
	if err != nil {
		return nil, err
	}
	return &SecurityProof{ProofToken: token, ExpiresAt: expiresAt, Method: method, Scope: binding.Scope}, nil
}

// ConsumeOperationProof accepts a proof at most once. The consumption commits
// before the caller performs its action; an action failure must not restore it.
func ConsumeOperationProof(raw string, identity AuthIdentity, operation VerificationOperation) (*model.AuthFlowAuthorization, error) {
	binding, err := BindVerificationOperation(operation)
	if err != nil {
		return nil, err
	}
	claims, err := verifySecurityProof(raw, identity, binding)
	if err != nil {
		return nil, err
	}
	if _, _, err := ValidateLoginSession(identity); err != nil {
		return nil, err
	}
	if _, err := RequireVerificationMethod(identity, binding.Scope, claims.Method); err != nil {
		return nil, err
	}
	flow, err := model.ConsumeAuthFlowWithAction(claims.ID, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeSecurityProof, UserId: identity.UserID, SessionId: identity.SessionID,
	}, func(tx *gorm.DB, _ *model.AuthFlow) error {
		return model.ValidateAuthSessionWithTx(tx, identity)
	})
	switch {
	case errors.Is(err, model.ErrAuthFlowConsumed):
		return nil, ErrProofConsumed
	case errors.Is(err, model.ErrAuthFlowExpired):
		return nil, ErrAuthTokenExpired
	case errors.Is(err, model.ErrAuthFlowInvalid):
		return nil, ErrAuthTokenInvalid
	case err != nil:
		return nil, err
	}
	return &model.AuthFlowAuthorization{
		AuthSessionIdentity: identity, ProofID: flow.Id, Scope: binding.Scope,
		ContextHash: binding.ContextHash, Method: claims.Method,
	}, nil
}

// ValidateFlowAuthorization permits a dedicated configuration flow to outlive
// its consumed proof, while retaining its operation, session and method policy.
func ValidateFlowAuthorization(identity AuthIdentity, operation VerificationOperation, authorization *model.AuthFlowAuthorization) error {
	if authorization == nil || authorization.ProofID <= 0 || authorization.AuthSessionIdentity != identity {
		return model.ErrAuthFlowInvalid
	}
	binding, err := BindVerificationOperation(operation)
	if err != nil {
		return err
	}
	if authorization.Scope != binding.Scope || !hmac.Equal([]byte(authorization.ContextHash), []byte(binding.ContextHash)) {
		return model.ErrAuthFlowInvalid
	}
	if _, _, err := ValidateLoginSession(identity); err != nil {
		return err
	}
	_, err = RequireVerificationMethod(identity, binding.Scope, authorization.Method)
	return err
}

type VerificationInput struct {
	Method            string          `json:"method"`
	Scope             string          `json:"scope"`
	Context           json.RawMessage `json:"context,omitempty"`
	Code              string          `json:"code,omitempty"`
	Password          string          `json:"password,omitempty"`
	PasswordEncrypted string          `json:"password_encrypted,omitempty"`
	EncryptionKeyID   string          `json:"encryption_key_id,omitempty"`
}

func VerifySecurityInput(identity AuthIdentity, input VerificationInput) (*SecurityProof, error) {
	binding, err := BindVerificationOperation(VerificationOperation{Scope: input.Scope, Context: input.Context})
	if err != nil {
		return nil, err
	}
	if _, err := RequireVerificationMethod(identity, input.Scope, input.Method); err != nil {
		return nil, err
	}
	switch input.Method {
	case VerificationMethodPassword:
		password := input.Password
		if common.PasswordLoginEncryptionEnabled {
			var err error
			password, err = common.DecryptPassword(input.PasswordEncrypted, input.EncryptionKeyID)
			if err != nil {
				return nil, ErrVerificationFailed
			}
		}
		user, err := model.GetUserById(identity.UserID, true)
		if err != nil {
			return nil, err
		}
		if password == "" || user.Password == "" || !common.ValidatePasswordAndHash(password, user.Password) {
			return nil, ErrVerificationFailed
		}
	case VerificationMethodTwoFA:
		if input.Scope == VerificationScopeTwoFABackupCodes {
			if _, err := common.ValidateNumericCode(input.Code); err != nil {
				return nil, ErrVerificationFailed
			}
		}
		twoFA, err := model.GetTwoFAByUserId(identity.UserID)
		if err != nil {
			return nil, err
		}
		if err := VerifyTwoFactorCode(twoFA, input.Code); err != nil {
			return nil, err
		}
	case VerificationMethodPasskey, VerificationMethodOAuth:
		return nil, ErrVerificationFlowRequired
	default:
		return nil, ErrProofMethod
	}
	return CompleteSecurityVerification(identity, binding, input.Method)
}

// VerifyTwoFactorCode classifies the input before verification so one failed
// submission cannot increment the failure counter for both TOTP and backup codes.
func VerifyTwoFactorCode(twoFA *model.TwoFA, code string) error {
	if twoFA == nil || !twoFA.IsEnabled {
		return model.ErrTwoFANotEnabled
	}
	if twoFA.IsLocked() {
		return ErrVerificationLocked
	}
	code = strings.TrimSpace(code)
	var valid bool
	var err error
	if numeric, numericErr := common.ValidateNumericCode(code); numericErr == nil {
		valid, err = twoFA.ValidateTOTPAndUpdateUsage(numeric)
	} else if common.ValidateBackupCode(code) {
		valid, err = twoFA.ValidateBackupCodeAndUpdateUsage(code)
	} else {
		err = twoFA.IncrementFailedAttempts()
	}
	if err != nil {
		return err
	}
	if !valid {
		return ErrVerificationFailed
	}
	return nil
}

func GetOAuthVerificationBinding(identity AuthIdentity, scope, provider string) (string, error) {
	requirements, err := RequireVerificationMethod(identity, scope, VerificationMethodOAuth)
	if err != nil {
		return "", err
	}
	for _, linked := range requirements.OAuthProviders {
		if linked.Slug == provider {
			return linked.UserID, nil
		}
	}
	return "", ErrVerificationUnavailable
}
