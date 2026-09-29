package middleware

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// SecureVerificationRequired protects channel key disclosure. Other sensitive
// operations validate their narrower proof scopes in their controller.
func SecureVerificationRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		channelID, err := strconv.Atoi(c.Param("id"))
		if err != nil || channelID <= 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"success": false, "code": "SECURITY_CONTEXT_INVALID", "message": service.ErrVerificationContextInvalid.Error()})
			return
		}
		context, err := common.Marshal(service.ChannelKeyReadContext{ChannelID: channelID})
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		if RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeChannelKeyRead, Context: context}) == nil {
			return
		}
		c.Set("secure_verified", true)
		c.Next()
	}
}

// RequireSecurityProof validates a proof against the authenticated dashboard
// session and writes the shared proof error contract on failure.
func RequireSecurityProof(c *gin.Context, operation service.VerificationOperation) *model.AuthFlowAuthorization {
	identity, ok := GetSessionAuthIdentity(c)
	if !ok {
		securityProofError(c, "SECURITY_PROOF_INVALID", "安全验证状态无效")
		return nil
	}
	raw := strings.TrimSpace(c.GetHeader("X-Security-Proof"))
	if raw == "" {
		securityProofError(c, "SECURITY_PROOF_REQUIRED", "需要安全验证")
		return nil
	}
	authorization, err := service.ConsumeOperationProof(raw, identity, operation)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAuthTokenExpired):
			securityProofError(c, "SECURITY_PROOF_EXPIRED", "安全验证已过期")
		case errors.Is(err, service.ErrProofScope):
			securityProofError(c, "SECURITY_PROOF_SCOPE_MISMATCH", "安全验证范围不匹配")
		case errors.Is(err, service.ErrVerificationUnavailable):
			securityProofError(c, "SECURITY_METHOD_UNAVAILABLE", service.ErrVerificationUnavailable.Error())
		case errors.Is(err, service.ErrProofMethod):
			securityProofError(c, "SECURITY_PROOF_METHOD_MISMATCH", "安全验证方式不匹配")
		case errors.Is(err, service.ErrProofConsumed):
			securityProofError(c, "SECURITY_PROOF_CONSUMED", "This verification has already been used. Please verify again.")
		case errors.Is(err, service.ErrProofContext):
			securityProofError(c, "SECURITY_PROOF_CONTEXT_MISMATCH", "Verification does not match this action's details. Please verify again.")
		case errors.Is(err, service.ErrVerificationForbidden):
			securityProofError(c, "SECURITY_ACTION_FORBIDDEN", service.ErrVerificationForbidden.Error())
		case errors.Is(err, service.ErrAuthTokenInvalid), errors.Is(err, service.ErrLoginSessionInvalid), errors.Is(err, service.ErrLoginSessionRevoked), errors.Is(err, model.ErrUserSessionInactive):
			securityProofError(c, "SECURITY_PROOF_INVALID", "安全验证状态无效")
		default:
			_ = c.Error(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"success": false, "code": "AUTH_INTERNAL_ERROR", "message": "Please try again later."})
		}
		return nil
	}
	return authorization
}

func securityProofError(c *gin.Context, code, message string) {
	c.Set("security_error_code", code)
	c.JSON(http.StatusForbidden, gin.H{
		"success": false,
		"message": message,
		"code":    code,
	})
	c.Abort()
}
