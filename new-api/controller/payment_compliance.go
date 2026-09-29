package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

type PaymentComplianceRequest struct {
	Confirmed bool `json:"confirmed"`
}

func requirePaymentCompliance(c *gin.Context) bool {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		common.ApiErrorI18n(c, i18n.MsgPaymentComplianceRequired)
		return false
	}
	return true
}

func ConfirmPaymentCompliance(c *gin.Context) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "This operation requires dashboard session authentication. API access token is not allowed.",
		})
		return
	}

	var req PaymentComplianceRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if !req.Confirmed {
		common.ApiErrorMsg(c, "请确认合规声明")
		return
	}

	now := time.Now().Unix()
	userId := c.GetInt("id")
	clientIP := c.ClientIP()

	updates := map[string]string{
		"payment_setting.compliance_confirmed":     "true",
		"payment_setting.compliance_terms_version": operation_setting.CurrentComplianceTermsVersion,
		"payment_setting.compliance_confirmed_at":  strconv.FormatInt(now, 10),
		"payment_setting.compliance_confirmed_by":  strconv.Itoa(userId),
		"payment_setting.compliance_confirmed_ip":  clientIP,
	}

	for key, value := range updates {
		if err := model.UpdateOption(key, value); err != nil {
			common.ApiError(c, err)
			return
		}
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf(
		"payment compliance confirmed user_id=%d ip=%s terms_version=%s confirmed_at=%d",
		userId,
		clientIP,
		operation_setting.CurrentComplianceTermsVersion,
		now,
	))

	common.ApiSuccess(c, gin.H{
		"confirmed":     true,
		"terms_version": operation_setting.CurrentComplianceTermsVersion,
		"confirmed_at":  now,
		"confirmed_by":  userId,
	})
}
