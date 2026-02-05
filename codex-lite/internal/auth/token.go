package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type TokenStorage struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	Email        string `json:"email"`
	Expire       string `json:"expired"`
	LastRefresh  string `json:"last_refresh"`
	Type         string `json:"type"`
}

func NewTokenStorage(resp *TokenResponse) *TokenStorage {
	claims, _ := ParseJWT(resp.IDToken)
	email := ""
	accountID := ""
	if claims != nil {
		email = claims.Email
		if len(claims.Orgs) > 0 {
			accountID = claims.Orgs[0].ID
		}
	}
	expire := time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	return &TokenStorage{
		IDToken:      resp.IDToken,
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		AccountID:    accountID,
		Email:        email,
		Expire:       expire.Format(time.RFC3339),
		LastRefresh:  time.Now().Format(time.RFC3339),
		Type:         "codex",
	}
}

func (t *TokenStorage) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func LoadToken(path string) (*TokenStorage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t TokenStorage
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}
