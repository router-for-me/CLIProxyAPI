package controller

import (
	"encoding/json"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	passkeysvc "github.com/QuantumNous/new-api/service/passkey"
	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	webauthnlib "github.com/go-webauthn/webauthn/webauthn"
)

func VerifyLogin(c *gin.Context) {
	var request struct {
		FlowToken string `json:"flow_token"`
		Method    string `json:"method"`
		Code      string `json:"code"`
	}
	if common.DecodeJson(c.Request.Body, &request) != nil || request.FlowToken == "" || request.Code == "" {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if request.Method == "" {
		request.Method = service.VerificationMethodTwoFA
	}
	if request.Method != service.VerificationMethodTwoFA {
		writeSecurityOperationError(c, service.ErrProofMethod)
		return
	}
	bundle, migration, err := service.VerifyLoginCode(request.FlowToken, request.Code, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	completeVerifiedLoginResponse(c, bundle, service.VerificationMethodTwoFA, migration)
}

func LoginPasskeyBegin(c *gin.Context) {
	var request struct {
		FlowToken string `json:"flow_token"`
		RPID      string `json:"rp_id"`
	}
	if common.DecodeJson(c.Request.Body, &request) != nil || request.FlowToken == "" {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	verification, err := service.RequireLoginVerification(request.FlowToken, service.VerificationMethodPasskey)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	credential, err := model.GetPasskeyByUserID(verification.State.UserID)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	credentialRPID := ""
	if credential.RPID != nil {
		credentialRPID = *credential.RPID
	}
	wa, rpIDs, err := passkeysvc.BuildLoginWebAuthn(c.Request, request.RPID, credentialRPID)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	user := &model.User{Id: verification.State.UserID}
	options, sessionData, err := wa.BeginLogin(passkeysvc.NewWebAuthnUser(user, credential), webauthnlib.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	token, expiresAt, err := passkeysvc.CreateSessionDataFlow(model.AuthFlowPurposeLoginPasskey, passkeysvc.FlowSecurity{
		AuthSessionIdentity: model.AuthSessionIdentity{UserID: user.Id, UserAuthVersion: verification.State.AuthVersion},
		LoginFlowID:         verification.Flow.Id, LoginExpiresAt: verification.Flow.ExpiresAt.Unix(),
	}, sessionData)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"flow_token": token, "expires_at": expiresAt, "options": options, "rp_ids": rpIDs})
}

func LoginPasskeyFinish(c *gin.Context) {
	var request struct {
		FlowToken        string          `json:"flow_token"`
		PasskeyFlowToken string          `json:"passkey_flow_token"`
		Credential       json.RawMessage `json:"credential"`
	}
	if common.DecodeJson(c.Request.Body, &request) != nil || request.FlowToken == "" || request.PasskeyFlowToken == "" || len(request.Credential) == 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	verification, err := service.RequireLoginVerification(request.FlowToken, service.VerificationMethodPasskey)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(request.Credential)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	identity := model.AuthSessionIdentity{UserID: verification.State.UserID, UserAuthVersion: verification.State.AuthVersion}
	sessionData, security, err := passkeysvc.PopSessionDataFlow(request.PasskeyFlowToken, model.AuthFlowPurposeLoginPasskey, identity)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	c.Set("passkey_rp_id", sessionData.RelyingPartyID)
	if security.LoginFlowID != verification.Flow.Id || sessionData.UserVerification != protocol.VerificationRequired {
		writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
		return
	}
	credential, err := model.GetPasskeyByUserID(identity.UserID)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	wa, err := passkeysvc.BuildWebAuthnForRPID(c.Request, sessionData.RelyingPartyID)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	if credential.RPID != nil && *credential.RPID != "" && *credential.RPID != sessionData.RelyingPartyID {
		writeSecurityOperationError(c, service.ErrVerificationFailed)
		return
	}
	validated, err := wa.ValidateLogin(passkeysvc.NewWebAuthnUser(&model.User{Id: identity.UserID}, credential), *sessionData, parsed)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	if err := model.UpdatePasskeyAssertionState(identity.UserID, validated, time.Now(), sessionData.RelyingPartyID); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	bundle, migration, err := service.CompleteLoginVerification(request.FlowToken, verification, service.VerificationMethodPasskey, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	completeVerifiedLoginResponse(c, bundle, service.VerificationMethodPasskey, migration)
}

func completeVerifiedLoginResponse(c *gin.Context, bundle *service.AuthBundle, method string, migration *service.LegacyGitHubMigration) {
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	user, err := model.GetSelfUserById(identity.UserID)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.Set("login_verification_method", method)
	if migration != nil {
		// The legacy GitHub binding was rewritten together with this session.
		notificationFailed := service.NotifyAccountSecurityChange(user.Email, "Login account linked: GitHub") != nil
		recordLegacyGitHubBindingAudit(c, user, true, map[string]any{
			"legacy_id": migration.LegacyID, "provider_user_id": migration.GitHubID,
			"verification_method": method, "notification_failed": notificationFailed,
		})
	}
	writeLoginResponse(c, user, bundle)
}
