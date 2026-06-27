package codebuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

type AuthState struct {
	AuthURL string
	State   string
}

type TokenStorage struct {
	AccessToken  string
	RefreshToken string
	UserID       string
	Domain       string
	ExpiresIn    int64
}

func (s *TokenStorage) SaveTokenToFile(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (a *CodeBuddyAuth) FetchAuthState(context.Context) (*AuthState, error) {
	return nil, fmt.Errorf("codebuddy auth state fetch not implemented")
}

func (a *CodeBuddyAuth) PollForToken(context.Context, string) (*TokenStorage, error) {
	return nil, fmt.Errorf("codebuddy token polling not implemented")
}

func (a *CodeBuddyAuth) RefreshToken(_ context.Context, accessToken, refreshToken, userID, domain string) (*TokenStorage, error) {
	return &TokenStorage{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		UserID:       userID,
		Domain:       domain,
	}, nil
}

func GetUserFriendlyMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
