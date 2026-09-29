package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	passkeysvc "github.com/QuantumNous/new-api/service/passkey"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	webauthnlib "github.com/go-webauthn/webauthn/webauthn"
)

type passkeyFinishRequest struct {
	FlowToken  string          `json:"flow_token"`
	Credential json.RawMessage `json:"credential"`
}

type passkeyVerifyBeginRequest struct {
	RPID    string          `json:"rp_id"`
	Scope   string          `json:"scope"`
	Context json.RawMessage `json:"context,omitempty"`
}

func parsePasskeyFinishRequest(c *gin.Context) (*passkeyFinishRequest, error) {
	var request passkeyFinishRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		return nil, err
	}
	if request.FlowToken == "" || len(request.Credential) == 0 {
		return nil, errors.New("Passkey 流程参数不完整")
	}
	return &request, nil
}

func PasskeyRegisterBegin(c *gin.Context) {
	if !system_setting.PasskeySettingsSnapshot().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := getAuthenticatedUser(c)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	authorization := middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopePasskeyRegister})
	if authorization == nil {
		return
	}

	credential, err := model.GetPasskeyByUserID(user.Id)
	if err != nil && !errors.Is(err, model.ErrPasskeyNotFound) {
		writeSecurityOperationError(c, err)
		return
	}
	if errors.Is(err, model.ErrPasskeyNotFound) {
		credential = nil
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	selection := wa.Config.AuthenticatorSelection
	selection.UserVerification = protocol.VerificationRequired
	options := []webauthnlib.RegistrationOption{webauthnlib.WithAuthenticatorSelection(selection)}
	if credential != nil {
		descriptor := credential.ToWebAuthnCredential().Descriptor()
		options = append(options, webauthnlib.WithExclusions([]protocol.CredentialDescriptor{descriptor}))
	}

	creation, sessionData, err := wa.BeginRegistration(waUser, options...)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiErrorMsg(c, "当前认证方式不支持安全验证")
		return
	}
	flowToken, expiresAt, err := passkeysvc.CreateSessionDataFlow(
		model.AuthFlowPurposePasskeyRegister,
		passkeysvc.FlowSecurity{AuthSessionIdentity: identity, Scope: authorization.Scope, ContextHash: authorization.ContextHash, Authorization: authorization},
		sessionData,
	)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    creation,
			"flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

func PasskeyRegisterFinish(c *gin.Context) {
	if !system_setting.PasskeySettingsSnapshot().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := getAuthenticatedUser(c)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	request, err := parsePasskeyFinishRequest(c)
	if err != nil {
		common.ApiErrorMsg(c, "无效的 Passkey 验证请求")
		return
	}
	parsedCredential, err := protocol.ParseCredentialCreationResponseBytes(request.Credential)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	credentialRecord, err := model.GetPasskeyByUserID(user.Id)
	if err != nil && !errors.Is(err, model.ErrPasskeyNotFound) {
		writeSecurityOperationError(c, err)
		return
	}
	if errors.Is(err, model.ErrPasskeyNotFound) {
		credentialRecord = nil
	}

	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiErrorMsg(c, "当前认证方式不支持安全验证")
		return
	}
	sessionData, security, err := passkeysvc.PopSessionDataFlow(
		request.FlowToken,
		model.AuthFlowPurposePasskeyRegister,
		identity,
	)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	wa, err := passkeysvc.BuildWebAuthnForRPID(c.Request, sessionData.RelyingPartyID)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	c.Set("passkey_rp_id", sessionData.RelyingPartyID)
	if sessionData.UserVerification != protocol.VerificationRequired {
		writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
		return
	}
	if err := service.ValidateFlowAuthorization(identity, service.VerificationOperation{Scope: service.VerificationScopePasskeyRegister}, security.Authorization); err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credentialRecord)
	credential, err := wa.CreateCredential(waUser, *sessionData, parsedCredential)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	passkeyCredential := model.NewPasskeyCredentialFromWebAuthn(user.Id, credential)
	if passkeyCredential == nil {
		common.ApiErrorMsg(c, "无法创建 Passkey 凭证")
		return
	}

	passkeyCredential.RPID = &sessionData.RelyingPartyID
	if err := model.RegisterPasskeyForSession(identity, passkeyCredential); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "passkey_registered")
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	recordUserSecurityAudit(c, user.Id, "user.passkey_register", nil)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 注册成功",
		"data":    authRotationData(bundle),
	})
}

func PasskeyDelete(c *gin.Context) {
	user, err := getAuthenticatedUser(c)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopePasskeyDelete}) == nil {
		return
	}

	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiErrorMsg(c, "当前认证方式不支持安全验证")
		return
	}
	if err := model.DeletePasskeyForSession(identity); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "passkey_deleted")
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	recordUserSecurityAudit(c, user.Id, "user.passkey_delete", nil)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 已解绑",
		"data":    authRotationData(bundle),
	})
}

func PasskeyStatus(c *gin.Context) {
	user, err := getAuthenticatedUser(c)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	credential, err := model.GetPasskeyByUserID(user.Id)
	if errors.Is(err, model.ErrPasskeyNotFound) {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "",
			"data": gin.H{
				"enabled": false,
			},
		})
		return
	}
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	data := gin.H{
		"enabled":      true,
		"last_used_at": credential.LastUsedAt,
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

func PasskeyLoginBegin(c *gin.Context) {
	if !system_setting.PasskeySettingsSnapshot().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	var request struct {
		RPID string `json:"rp_id"`
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if common.DecodeJson(c.Request.Body, &request) != nil {
			common.ApiErrorMsg(c, "无效的 Passkey 验证请求")
			return
		}
	}
	wa, rpIDs, err := passkeysvc.BuildLoginWebAuthn(c.Request, request.RPID, "")
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	assertion, sessionData, err := wa.BeginDiscoverableLogin(webauthnlib.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	flowToken, expiresAt, err := passkeysvc.CreateSessionDataFlow(
		model.AuthFlowPurposePasskeyLogin,
		passkeysvc.FlowSecurity{},
		sessionData,
	)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    assertion,
			"rp_ids":     rpIDs,
			"flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

func PasskeyLoginFinish(c *gin.Context) {
	if !system_setting.PasskeySettingsSnapshot().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	request, err := parsePasskeyFinishRequest(c)
	if err != nil {
		common.ApiErrorMsg(c, "无效的 Passkey 验证请求")
		return
	}
	parsedCredential, err := protocol.ParseCredentialRequestResponseBytes(request.Credential)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	sessionData, _, err := passkeysvc.PopSessionDataFlow(
		request.FlowToken,
		model.AuthFlowPurposePasskeyLogin,
		service.AuthIdentity{},
	)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	wa, err := passkeysvc.BuildWebAuthnForRPID(c.Request, sessionData.RelyingPartyID)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	c.Set("passkey_rp_id", sessionData.RelyingPartyID)
	if sessionData.UserVerification != protocol.VerificationRequired {
		writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
		return
	}

	handler := func(rawID, userHandle []byte) (webauthnlib.User, error) {
		// 首先通过凭证ID查找用户
		credential, err := model.GetPasskeyByCredentialID(rawID)
		if err != nil {
			return nil, fmt.Errorf("未找到 Passkey 凭证: %w", err)
		}

		if credential.RPID != nil && *credential.RPID != "" && *credential.RPID != sessionData.RelyingPartyID {
			return nil, service.ErrVerificationFailed
		}

		// 通过凭证获取用户
		user := &model.User{Id: credential.UserID}
		if err := user.FillUserById(); err != nil {
			return nil, fmt.Errorf("用户信息获取失败: %w", err)
		}

		if user.Status != common.UserStatusEnabled {
			return nil, model.ErrUserSessionInactive
		}

		if len(userHandle) > 0 {
			userID, parseErr := strconv.Atoi(string(userHandle))
			if parseErr != nil {
				// 记录异常但继续验证，因为某些客户端可能使用非数字格式
				common.SysLog(fmt.Sprintf("PasskeyLogin: userHandle parse error for credential, length: %d", len(userHandle)))
			} else if userID != user.Id {
				return nil, errors.New("用户句柄与凭证不匹配")
			}
		}

		return passkeysvc.NewWebAuthnUser(user, credential), nil
	}

	waUser, credential, err := wa.ValidatePasskeyLogin(handler, *sessionData, parsedCredential)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	userWrapper, ok := waUser.(*passkeysvc.WebAuthnUser)
	if !ok {
		common.ApiErrorMsg(c, "Passkey 登录状态异常")
		return
	}

	modelUser := userWrapper.ModelUser()
	if modelUser == nil {
		common.ApiErrorMsg(c, "Passkey 登录状态异常")
		return
	}

	if modelUser.Status != common.UserStatusEnabled {
		common.ApiErrorMsg(c, "该用户已被禁用")
		return
	}

	if err := model.UpdatePasskeyAssertionState(modelUser.Id, credential, time.Now(), sessionData.RelyingPartyID); err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	c.Set("login_verification_method", service.VerificationMethodPasskey)
	setupLoginAtAuthVersion(modelUser, modelUser.AuthVersion, c)
}

func AdminResetPasskey(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的用户 ID")
		return
	}

	user := &model.User{Id: id}
	if err := user.FillUserById(); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	myRole := c.GetInt("role")
	if !canManageTargetRole(myRole, user.Role) {
		common.ApiErrorMsg(c, "no permission")
		return
	}

	if _, err := model.GetPasskeyByUserID(user.Id); err != nil {
		if errors.Is(err, model.ErrPasskeyNotFound) {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "该用户尚未绑定 Passkey",
			})
			return
		}
		writeSecurityOperationError(c, err)
		return
	}

	if err := model.DeletePasskeyByUserIDWithAuthVersion(user.Id); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	if _, err := model.RevokeAllUserSessions(user.Id, "admin_passkey_reset"); err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	recordManageAuditFor(c, user.Id, "user.reset_passkey", map[string]any{
		"username": user.Username,
		"id":       user.Id,
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 已重置",
	})
}

func PasskeyVerifyBegin(c *gin.Context) {
	if !system_setting.PasskeySettingsSnapshot().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := getAuthenticatedUser(c)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	var request passkeyVerifyBeginRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorMsg(c, "无效的 Passkey 验证请求")
		return
	}
	binding, err := service.BindVerificationOperation(service.VerificationOperation{Scope: request.Scope, Context: request.Context})
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
		return
	}
	if _, err := service.RequireVerificationMethod(identity, request.Scope, service.VerificationMethodPasskey); err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	credential, err := model.GetPasskeyByUserID(user.Id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "该用户尚未绑定 Passkey",
		})
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

	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	assertion, sessionData, err := wa.BeginLogin(waUser, webauthnlib.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	flowToken, expiresAt, err := passkeysvc.CreateSessionDataFlow(
		model.AuthFlowPurposePasskeyStepUp,
		passkeysvc.FlowSecurity{AuthSessionIdentity: identity, Scope: binding.Scope, ContextHash: binding.ContextHash},
		sessionData,
	)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    assertion,
			"rp_ids":     rpIDs,
			"flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

func PasskeyVerifyFinish(c *gin.Context) {
	if !system_setting.PasskeySettingsSnapshot().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := getAuthenticatedUser(c)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	request, err := parsePasskeyFinishRequest(c)
	if err != nil {
		common.ApiErrorMsg(c, "无效的 Passkey 验证请求")
		return
	}
	parsedCredential, err := protocol.ParseCredentialRequestResponseBytes(request.Credential)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	credential, err := model.GetPasskeyByUserID(user.Id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "该用户尚未绑定 Passkey",
		})
		return
	}

	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiErrorMsg(c, "当前认证方式不支持安全验证")
		return
	}
	sessionData, security, err := passkeysvc.PopSessionDataFlow(
		request.FlowToken,
		model.AuthFlowPurposePasskeyStepUp,
		identity,
	)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	wa, err := passkeysvc.BuildWebAuthnForRPID(c.Request, sessionData.RelyingPartyID)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	c.Set("passkey_rp_id", sessionData.RelyingPartyID)
	if sessionData.UserVerification != protocol.VerificationRequired {
		writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
		return
	}

	if credential.RPID != nil && *credential.RPID != "" && *credential.RPID != sessionData.RelyingPartyID {
		writeSecurityOperationError(c, service.ErrVerificationFailed)
		return
	}
	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	validatedCredential, err := wa.ValidateLogin(waUser, *sessionData, parsedCredential)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	if err := model.UpdatePasskeyAssertionState(user.Id, validatedCredential, time.Now(), sessionData.RelyingPartyID); err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	proof, err := service.CompleteSecurityVerification(identity, service.VerificationBinding{Scope: security.Scope, ContextHash: security.ContextHash}, service.VerificationMethodPasskey)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	recordUserSecurityAudit(c, user.Id, "user.security_verify", map[string]any{"method": proof.Method, "scope": proof.Scope})
	common.ApiSuccess(c, proof)
}

func getAuthenticatedUser(c *gin.Context) (*model.User, error) {
	id := c.GetInt("id")
	if id == 0 {
		return nil, service.ErrAuthTokenInvalid
	}
	user, err := model.GetUserById(id, false)
	if err != nil {
		return nil, err
	}
	if user.Status != common.UserStatusEnabled {
		return nil, model.ErrUserSessionInactive
	}
	return user, nil
}
