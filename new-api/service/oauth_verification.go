package service

import "errors"

var ErrOAuthAccountMismatch = errors.New("The OAuth account does not match the account linked to your profile.")

type OAuthVerificationFlow struct {
	Scope           string `json:"scope"`
	ContextHash     string `json:"context_hash"`
	ProviderUserID  string `json:"provider_user_id"`
	UserAuthVersion int64  `json:"auth_version"`
	SessionVersion  int64  `json:"session_version"`
}

func StartOAuthVerification(identity AuthIdentity, operation VerificationOperation, provider string) (*OAuthVerificationFlow, error) {
	binding, err := BindVerificationOperation(operation)
	if err != nil {
		return nil, err
	}
	providerUserID, err := GetOAuthVerificationBinding(identity, binding.Scope, provider)
	if err != nil {
		return nil, err
	}
	return &OAuthVerificationFlow{
		Scope: binding.Scope, ContextHash: binding.ContextHash, ProviderUserID: providerUserID,
		UserAuthVersion: identity.UserAuthVersion, SessionVersion: identity.SessionVersion,
	}, nil
}

// FinishOAuthVerification only verifies an existing binding. It deliberately
// never calls OAuth's login or find-or-create-user paths.
func FinishOAuthVerification(identity AuthIdentity, provider, actualUserID string, flow *OAuthVerificationFlow) (*SecurityProof, error) {
	if flow == nil || flow.ContextHash == "" || flow.UserAuthVersion != identity.UserAuthVersion || flow.SessionVersion != identity.SessionVersion {
		return nil, ErrAuthTokenInvalid
	}
	expectedUserID, err := GetOAuthVerificationBinding(identity, flow.Scope, provider)
	if err != nil {
		return nil, err
	}
	if actualUserID == "" || actualUserID != expectedUserID || actualUserID != flow.ProviderUserID {
		return nil, ErrOAuthAccountMismatch
	}
	return CompleteSecurityVerification(identity, VerificationBinding{Scope: flow.Scope, ContextHash: flow.ContextHash}, VerificationMethodOAuth)
}
