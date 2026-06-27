package cursor

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type AuthParams struct {
	UUID     string
	Verifier string
	LoginURL string
}

type Tokens struct {
	AccessToken  string
	RefreshToken string
}

func GenerateAuthParams() (*AuthParams, error) {
	return nil, fmt.Errorf("cursor auth params generation not implemented")
}

func PollForAuth(context.Context, string, string) (*Tokens, error) {
	return nil, fmt.Errorf("cursor auth polling not implemented")
}

func RefreshToken(_ context.Context, refreshToken string) (*Tokens, error) {
	return &Tokens{RefreshToken: refreshToken}, nil
}

func GetTokenExpiry(string) time.Time {
	return time.Now().Add(time.Hour)
}

func ParseJWTSub(string) string {
	return ""
}

func SubToShortHash(sub string) string {
	sub = strings.TrimSpace(sub)
	if len(sub) > 8 {
		return sub[:8]
	}
	return sub
}

func CredentialFileName(prefix, subHash string) string {
	subHash = strings.TrimSpace(subHash)
	if subHash == "" {
		subHash = "user"
	}
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		return prefix + "-cursor-" + subHash + ".json"
	}
	return "cursor-" + subHash + ".json"
}

func DisplayLabel(prefix, subHash string) string {
	subHash = strings.TrimSpace(subHash)
	if subHash == "" {
		subHash = "user"
	}
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		return prefix + "/" + subHash
	}
	return subHash
}
