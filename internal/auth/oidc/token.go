package oidc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

type TokenStorage struct {
	Type      string
	Metadata  map[string]any
	TokenData *TokenData
	Headers   map[string]string
	Models    []config.OpenAICompatibilityModel
}

func (ts *TokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "oidc"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("oidc token: create directory failed: %w", err)
	}
	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("oidc token: create file failed: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := misc.MergeMetadata(ts.TokenData, ts.Metadata)
	if err != nil {
		return fmt.Errorf("oidc token: merge metadata failed: %w", err)
	}
	if err = json.NewEncoder(f).Encode(data); err != nil {
		return fmt.Errorf("oidc token: encode token failed: %w", err)
	}
	return nil
}
