package codebuddy

import (
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
)

const (
	DefaultDomain = "https://api.codebuddy.app"
	BaseURL       = "https://api.codebuddy.app"
	UserAgent     = "CodeBuddy/1.0"
)

// CodeBuddyAuth is a stub for CodeBuddy authentication.
type CodeBuddyAuth struct {
	cfg *config.Config
}

// NewCodeBuddyAuth creates a new CodeBuddy auth handler.
func NewCodeBuddyAuth(cfg *config.Config) *CodeBuddyAuth {
	return &CodeBuddyAuth{cfg: cfg}
}
