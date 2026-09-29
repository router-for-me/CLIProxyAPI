package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type emailBindRequest struct {
	Email     string `json:"email"`
	FlowToken string `json:"flow_token"`
	NewCode   string `json:"new_code"`
	OldCode   string `json:"old_code"`
}

func EmailBindStart(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
		return
	}
	succeeded, notificationFailed := false, false
	defer func() {
		recordUserSecurityAudit(c, identity.UserID, "user.binding_start", map[string]any{"provider": "email", "success": succeeded, "notification_failed": notificationFailed})
	}()
	var request emailBindRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		writeSecurityOperationError(c, service.ErrVerificationContextInvalid)
		return
	}
	email, err := service.ValidateAccountEmail(request.Email)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	context, err := common.Marshal(service.AccountBindingContext{Provider: "email", Email: email})
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	authorization := middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeAccountBind, Context: context})
	if authorization == nil {
		return
	}
	data, err := service.StartEmailBinding(identity, authorization, email)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	succeeded, notificationFailed = true, data.NotificationWarning
	common.ApiSuccess(c, data)
}

func EmailBindResend(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
		return
	}
	succeeded := false
	defer func() {
		recordUserSecurityAudit(c, identity.UserID, "user.email_binding_resend", map[string]any{"success": succeeded})
	}()
	var request emailBindRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.FlowToken == "" {
		writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
		return
	}
	data, err := service.ResendAccountEmailBinding(identity, request.FlowToken)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	succeeded = true
	common.ApiSuccess(c, data)
}

func EmailBind(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
		return
	}
	succeeded, notificationFailed := false, false
	defer func() {
		recordUserSecurityAudit(c, identity.UserID, "user.binding_bind", map[string]any{"provider": "email", "success": succeeded, "notification_failed": notificationFailed})
	}()
	var request emailBindRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.FlowToken == "" {
		writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
		return
	}
	state, err := service.FinishEmailBinding(identity, request.FlowToken, request.NewCode, request.OldCode)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	succeeded = true
	notificationFailed = service.NotifyAccountSecurityChange(state.CurrentEmail, "Email address changed") != nil
	if err := service.NotifyAccountSecurityChange(state.Email, "Email address confirmed"); err != nil {
		notificationFailed = true
	}
	if err := model.PublishUserAuthCache(identity.UserID); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"notification_warning": notificationFailed}})
}
