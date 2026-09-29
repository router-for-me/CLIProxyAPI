package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Verify2FARequest struct {
	Code      string `json:"code" binding:"required"`
	FlowToken string `json:"flow_token,omitempty"`
}

func Setup2FA(c *gin.Context) {
	authorization := middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeTwoFASetup})
	if authorization == nil {
		return
	}
	identity, _ := middleware.GetSessionAuthIdentity(c)
	setup, err := service.StartTwoFASetup(identity, authorization)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	recordUserSecurityAudit(c, identity.UserID, "user.2fa_setup", nil)
	common.ApiSuccess(c, setup)
}

func Enable2FA(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
		return
	}
	var req Verify2FARequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if err := service.FinishTwoFASetup(identity, req.FlowToken, req.Code); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "twofa_enabled")
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	recordUserSecurityAudit(c, identity.UserID, "user.2fa_enable", nil)
	common.ApiSuccess(c, authRotationData(bundle))
}

// Disable2FA 禁用2FA
func Disable2FA(c *gin.Context) {
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeTwoFADisable}) == nil {
		return
	}
	identity, _ := middleware.GetSessionAuthIdentity(c)
	userId := identity.UserID
	if err := model.DisableTwoFAForSession(identity); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "twofa_disabled")
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	// 记录操作日志
	recordUserSecurityAudit(c, userId, "user.2fa_disable_self", nil)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "两步验证已禁用",
		"data":    authRotationData(bundle),
	})
}

// Get2FAStatus 获取用户2FA状态
func Get2FAStatus(c *gin.Context) {
	userId := c.GetInt("id")

	twoFA, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	status := map[string]any{
		"enabled": false,
		"locked":  false,
	}

	if twoFA != nil {
		status["enabled"] = twoFA.IsEnabled
		status["locked"] = twoFA.IsLocked()
		if twoFA.IsEnabled {
			// 获取剩余备用码数量
			backupCount, err := model.GetUnusedBackupCodeCount(userId)
			if err != nil {
				common.SysLog("获取备用码数量失败: " + err.Error())
			} else {
				status["backup_codes_remaining"] = backupCount
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    status,
	})
}

// RegenerateBackupCodes 重新生成备用码
func RegenerateBackupCodes(c *gin.Context) {
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeTwoFABackupCodes}) == nil {
		return
	}
	identity, _ := middleware.GetSessionAuthIdentity(c)
	userId := identity.UserID
	// 生成新的备用码
	backupCodes, err := common.GenerateBackupCodes()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "生成备用码失败",
		})
		common.SysLog("生成备用码失败: " + err.Error())
		return
	}

	// 保存新的备用码并原子推进用户鉴权版本
	if err := model.ReplaceBackupCodesForSession(identity, backupCodes); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "保存备用码失败",
		})
		common.SysLog("保存备用码失败: " + err.Error())
		return
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "twofa_backup_codes_regenerated")
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	// 记录操作日志
	recordUserSecurityAudit(c, userId, "user.2fa_backup_codes", nil)

	data := authRotationData(bundle)
	data["backup_codes"] = backupCodes
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "备用码重新生成成功",
		"data":    data,
	})
}

// Verify2FALogin 登录时验证2FA
func Verify2FALogin(c *gin.Context) {
	VerifyLogin(c)
}

// Admin2FAStats 管理员获取2FA统计信息
func Admin2FAStats(c *gin.Context) {
	stats, err := model.GetTwoFAStats()
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    stats,
	})
}

// AdminDisable2FA 管理员强制禁用用户2FA
func AdminDisable2FA(c *gin.Context) {
	userIdStr := c.Param("id")
	userId, err := strconv.Atoi(userIdStr)
	if err != nil || userId <= 0 {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "用户ID格式错误",
		})
		return
	}

	// 检查目标用户权限
	targetUser, err := model.GetUserById(userId, false)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		common.ApiErrorI18n(c, i18n.MsgUserNotExists)
		return
	}
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	myRole := c.GetInt("role")
	if !canManageTargetRole(myRole, targetUser.Role) {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "无权操作同级或更高级用户的2FA设置",
		})
		return
	}

	// 禁用2FA
	if err := model.DisableTwoFAWithAuthVersion(userId); err != nil {
		if errors.Is(err, model.ErrTwoFANotEnabled) {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "用户未启用2FA",
			})
			return
		}
		writeSecurityOperationError(c, err)
		return
	}
	if _, err := model.RevokeAllUserSessions(userId, "admin_twofa_disabled"); err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	recordManageAuditFor(c, userId, "user.2fa_disable", nil)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "用户2FA已被强制禁用",
	})
}
