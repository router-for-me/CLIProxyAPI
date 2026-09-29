package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// I18n middleware detects and sets the language preference for the request
func I18n() gin.HandlerFunc {
	return func(c *gin.Context) {
		lang := detectLanguage(c)
		c.Set(string(constant.ContextKeyLanguage), lang)
		c.Next()
	}
}

// detectLanguage determines the language preference for the request
// Priority: 1. User setting (if logged in) -> 2. Accept-Language header -> 3. Default language
func detectLanguage(c *gin.Context) string {
	// 1. Try to get language from user setting (set by auth middleware)
	if userSetting, ok := common.GetContextKeyType[dto.UserSetting](c, constant.ContextKeyUserSetting); ok {
		if userSetting.Language != "" && i18n.IsSupported(userSetting.Language) {
			return userSetting.Language
		}
	}

	// 2. Parse Accept-Language header
	acceptLang := c.GetHeader("Accept-Language")
	if acceptLang != "" {
		lang := i18n.ParseAcceptLanguage(acceptLang)
		if i18n.IsSupported(lang) {
			return lang
		}
	}

	// 3. Return default language
	return i18n.DefaultLang
}

// GetLanguage returns the current language from gin context
func GetLanguage(c *gin.Context) string {
	if lang := c.GetString(string(constant.ContextKeyLanguage)); lang != "" {
		return lang
	}
	return i18n.DefaultLang
}
