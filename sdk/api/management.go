// Package api exposes helpers for embedding CLIProxyAPI.
//
// It wraps internal management handler types so external projects can integrate
// management endpoints without importing internal packages.
package api

import (
	"github.com/gin-gonic/gin"
	internalmanagement "github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// ManagementTokenRequester exposes a limited subset of management endpoints for requesting tokens.
type ManagementTokenRequester interface {
	RequestAnthropicToken(*gin.Context)
	RequestGeminiCLIToken(*gin.Context)
	RequestCodexToken(*gin.Context)
	RequestAntigravityToken(*gin.Context)
	RequestQwenToken(*gin.Context)
	RequestKimiToken(*gin.Context)
	RequestIFlowToken(*gin.Context)
	RequestIFlowCookieToken(*gin.Context)
	GetAuthStatus(c *gin.Context)
	PostOAuthCallback(c *gin.Context)
}

type managementTokenRequester struct {
	handler *internalmanagement.Handler
}

// NewManagementTokenRequester creates a limited management handler exposing only token request endpoints.
func NewManagementTokenRequester(cfg *config.Config, manager *coreauth.Manager) ManagementTokenRequester {
	return &managementTokenRequester{
		handler: internalmanagement.NewHandlerWithoutConfigFilePath(cfg, manager),
	}
}

func (m *managementTokenRequester) RequestAnthropicToken(c *gin.Context) {
	m.handler.RequestAnthropicToken(c)
}

func (m *managementTokenRequester) RequestGeminiCLIToken(c *gin.Context) {
	m.handler.RequestGeminiCLIToken(c)
}

func (m *managementTokenRequester) RequestCodexToken(c *gin.Context) {
	m.handler.RequestCodexToken(c)
}

func (m *managementTokenRequester) RequestAntigravityToken(c *gin.Context) {
	m.handler.RequestAntigravityToken(c)
}

func (m *managementTokenRequester) RequestQwenToken(c *gin.Context) {
	m.handler.RequestQwenToken(c)
}

func (m *managementTokenRequester) RequestKimiToken(c *gin.Context) {
	m.handler.RequestKimiToken(c)
}

func (m *managementTokenRequester) RequestIFlowToken(c *gin.Context) {
	m.handler.RequestIFlowToken(c)
}

func (m *managementTokenRequester) RequestIFlowCookieToken(c *gin.Context) {
	m.handler.RequestIFlowCookieToken(c)
}

func (m *managementTokenRequester) GetAuthStatus(c *gin.Context) {
	m.handler.GetAuthStatus(c)
}

func (m *managementTokenRequester) PostOAuthCallback(c *gin.Context) {
	m.handler.PostOAuthCallback(c)
}
