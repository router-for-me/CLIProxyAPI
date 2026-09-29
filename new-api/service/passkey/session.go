package passkey

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	webauthn "github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
)

const passkeyFlowTTL = 5 * time.Minute

type flowPayload struct {
	SessionData webauthn.SessionData `json:"session_data"`
	Security    FlowSecurity         `json:"security"`
}

type FlowSecurity struct {
	model.AuthSessionIdentity
	Scope          string                       `json:"scope"`
	ContextHash    string                       `json:"context_hash"`
	Authorization  *model.AuthFlowAuthorization `json:"authorization,omitempty"`
	LoginFlowID    int64                        `json:"login_flow_id,omitempty"`
	LoginExpiresAt int64                        `json:"login_expires_at,omitempty"`
}

func CreateSessionDataFlow(purpose string, security FlowSecurity, data *webauthn.SessionData) (string, int64, error) {
	if data == nil {
		return "", 0, errors.New("Passkey 会话数据不能为空")
	}
	if purpose == model.AuthFlowPurposeLoginPasskey {
		if security.UserID <= 0 || security.UserAuthVersion <= 0 || security.LoginFlowID <= 0 || security.LoginExpiresAt <= time.Now().Unix() || security.SessionID != "" || security.SessionVersion != 0 {
			return "", 0, model.ErrAuthFlowInvalid
		}
	} else if purpose != model.AuthFlowPurposePasskeyLogin && (security.UserID <= 0 || security.SessionID == "" || security.Scope == "" || security.ContextHash == "" || security.UserAuthVersion <= 0 || security.SessionVersion <= 0) {
		return "", 0, model.ErrAuthFlowInvalid
	}
	if purpose == model.AuthFlowPurposePasskeyRegister && (security.Authorization == nil || security.Authorization.ProofID <= 0 || security.Authorization.AuthSessionIdentity != security.AuthSessionIdentity) {
		return "", 0, model.ErrAuthFlowInvalid
	}
	payload, err := common.Marshal(flowPayload{SessionData: *data, Security: security})
	if err != nil {
		return "", 0, err
	}
	expiresAt := time.Now().Add(passkeyFlowTTL)
	if purpose == model.AuthFlowPurposeLoginPasskey && security.LoginExpiresAt < expiresAt.Unix() {
		expiresAt = time.Unix(security.LoginExpiresAt, 0)
	}
	token, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   purpose,
		UserId:    security.UserID,
		SessionId: security.SessionID,
		Payload:   string(payload),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", 0, err
	}
	return token, expiresAt.Unix(), nil
}

func PopSessionDataFlow(token, purpose string, identity model.AuthSessionIdentity) (*webauthn.SessionData, *FlowSecurity, error) {
	var payload flowPayload
	_, err := model.ConsumeAuthFlowWithAction(token, model.AuthFlowMatch{
		Purpose:   purpose,
		UserId:    identity.UserID,
		SessionId: identity.SessionID,
	}, func(tx *gorm.DB, flow *model.AuthFlow) error {
		if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
			return err
		}
		if payload.SessionData.RelyingPartyID == "" || payload.SessionData.Expires.IsZero() {
			return model.ErrAuthFlowInvalid
		}
		if !time.Now().Before(payload.SessionData.Expires) {
			return model.ErrAuthFlowExpired
		}
		if purpose == model.AuthFlowPurposePasskeyLogin {
			return nil
		}
		security := payload.Security
		if purpose == model.AuthFlowPurposeLoginPasskey {
			if security.AuthSessionIdentity != identity || identity.UserID <= 0 || identity.UserAuthVersion <= 0 || identity.SessionID != "" || identity.SessionVersion != 0 || security.LoginFlowID <= 0 || security.LoginExpiresAt <= time.Now().Unix() {
				return model.ErrAuthFlowInvalid
			}
			return nil
		}
		if security.AuthSessionIdentity != identity || security.Scope == "" || security.ContextHash == "" {
			return model.ErrAuthFlowInvalid
		}
		if purpose == model.AuthFlowPurposePasskeyRegister && (security.Authorization == nil || security.Authorization.ProofID <= 0 || security.Authorization.AuthSessionIdentity != identity) {
			return model.ErrAuthFlowInvalid
		}
		return model.ValidateAuthSessionWithTx(tx, identity)
	})
	if err != nil {
		return nil, nil, err
	}
	return &payload.SessionData, &payload.Security, nil
}
