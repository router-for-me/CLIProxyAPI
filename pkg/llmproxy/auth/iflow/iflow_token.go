package iflow

import (
	"fmt"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/auth/base"
)

// IFlowTokenStorage persists iFlow OAuth credentials alongside the derived API key.
type IFlowTokenStorage struct {
	base.BaseTokenStorage

	LastRefresh string `json:"last_refresh"`
	Expire      string `json:"expired"`
	APIKey      string `json:"api_key"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Cookie      string `json:"cookie"`
}

// SaveTokenToFile serialises the token storage to disk.
func (ts *IFlowTokenStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "iflow"
	if err := ts.Save(authFilePath, ts); err != nil {
		return fmt.Errorf("iflow token: %w", err)
	}
	return nil
}
