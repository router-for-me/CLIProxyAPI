package cursor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps/cursorproto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"
)

var refreshGroup singleflight.Group

// AuthParams contains the browser URL and the secrets used by Cursor's PKCE poll flow.
type AuthParams struct {
	Verifier  string
	Challenge string
	UUID      string
	LoginURL  string
}

// Tokens contains the access and refresh tokens returned by Cursor.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// ModelDetails is the persisted subset of Cursor's dynamic model catalog.
type ModelDetails struct {
	ID             string   `json:"id"`
	DisplayName    string   `json:"display_name,omitempty"`
	DisplayModelID string   `json:"display_model_id,omitempty"`
	Aliases        []string `json:"aliases,omitempty"`
	Thinking       bool     `json:"thinking,omitempty"`
	MaxMode        bool     `json:"max_mode,omitempty"`
}

// Client implements Cursor's browser OAuth and model discovery calls.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

func NewClient(cfg *config.Config, proxyURL string) *Client {
	client := &http.Client{Timeout: 30 * time.Second}
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if strings.TrimSpace(proxyURL) == "" {
			proxyURL = cfg.ProxyURL
		}
	}
	sdkCfg.ProxyURL = strings.TrimSpace(proxyURL)
	return &Client{httpClient: util.SetProxy(&sdkCfg, client), baseURL: APIBaseURL}
}

func GenerateAuthParams() (*AuthParams, error) {
	verifierBytes := make([]byte, 96)
	if _, errRead := rand.Read(verifierBytes); errRead != nil {
		return nil, fmt.Errorf("cursor oauth: generate verifier: %w", errRead)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	flowID := uuid.NewString()
	query := url.Values{
		"challenge":      {challenge},
		"uuid":           {flowID},
		"mode":           {"login"},
		"redirectTarget": {"cli"},
	}
	return &AuthParams{
		Verifier:  verifier,
		Challenge: challenge,
		UUID:      flowID,
		LoginURL:  LoginURL + "?" + query.Encode(),
	}, nil
}

func (c *Client) Poll(ctx context.Context, flowID, verifier string) (*Tokens, error) {
	if c == nil || c.httpClient == nil {
		return nil, fmt.Errorf("cursor oauth: client is nil")
	}
	delay := time.Second
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("cursor oauth: polling cancelled: %w", ctx.Err())
		case <-time.After(delay):
		}

		pollURL := c.baseURL + "/auth/poll?" + url.Values{
			"uuid":     {flowID},
			"verifier": {verifier},
		}.Encode()
		req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if errRequest != nil {
			return nil, fmt.Errorf("cursor oauth: create poll request: %w", errRequest)
		}
		resp, errDo := c.httpClient.Do(req)
		if errDo != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("cursor oauth: polling cancelled: %w", ctx.Err())
			}
			return nil, fmt.Errorf("cursor oauth: poll request: %w", errDo)
		}
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("cursor oauth: close poll response")
		}
		if errRead != nil {
			return nil, fmt.Errorf("cursor oauth: read poll response: %w", errRead)
		}
		if resp.StatusCode == http.StatusNotFound {
			delay = minDuration(time.Duration(float64(delay)*1.2), 10*time.Second)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("cursor oauth: poll failed with status %d", resp.StatusCode)
		}
		var payload struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		}
		if errJSON := json.Unmarshal(body, &payload); errJSON != nil {
			return nil, fmt.Errorf("cursor oauth: decode poll response: %w", errJSON)
		}
		payload.AccessToken = strings.TrimSpace(payload.AccessToken)
		payload.RefreshToken = strings.TrimSpace(payload.RefreshToken)
		if payload.AccessToken == "" || payload.RefreshToken == "" {
			return nil, fmt.Errorf("cursor oauth: poll returned incomplete credentials")
		}
		return &Tokens{
			AccessToken:  payload.AccessToken,
			RefreshToken: payload.RefreshToken,
			ExpiresAt:    JWTExpiry(payload.AccessToken),
		}, nil
	}
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("cursor oauth: refresh token is empty")
	}
	keyHash := sha256.Sum256([]byte(refreshToken))
	key := hex.EncodeToString(keyHash[:])
	value, errRefresh, _ := refreshGroup.Do(key, func() (any, error) {
		return c.refreshOnce(ctx, refreshToken)
	})
	if errRefresh != nil {
		return nil, errRefresh
	}
	tokens, ok := value.(*Tokens)
	if !ok || tokens == nil {
		return nil, fmt.Errorf("cursor oauth: invalid refresh result")
	}
	return tokens, nil
}

func (c *Client) refreshOnce(ctx context.Context, refreshToken string) (*Tokens, error) {
	payload, errJSON := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     CLIClientID,
		"refresh_token": refreshToken,
	})
	if errJSON != nil {
		return nil, fmt.Errorf("cursor oauth: encode refresh request: %w", errJSON)
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/oauth/token", bytes.NewReader(payload))
	if errRequest != nil {
		return nil, fmt.Errorf("cursor oauth: create refresh request: %w", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, errDo := c.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("cursor oauth: refresh request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("cursor oauth: close refresh response")
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, fmt.Errorf("cursor oauth: read refresh response: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cursor oauth: refresh failed with status %d", resp.StatusCode)
	}
	var result struct {
		AccessTokenSnake  string `json:"access_token"`
		RefreshTokenSnake string `json:"refresh_token"`
		AccessTokenCamel  string `json:"accessToken"`
		RefreshTokenCamel string `json:"refreshToken"`
	}
	if errDecode := json.Unmarshal(body, &result); errDecode != nil {
		return nil, fmt.Errorf("cursor oauth: decode refresh response: %w", errDecode)
	}
	accessToken := firstNonEmpty(result.AccessTokenSnake, result.AccessTokenCamel)
	if accessToken == "" {
		return nil, fmt.Errorf("cursor oauth: refresh returned empty access token")
	}
	newRefreshToken := firstNonEmpty(result.RefreshTokenSnake, result.RefreshTokenCamel, accessToken)
	return &Tokens{AccessToken: accessToken, RefreshToken: newRefreshToken, ExpiresAt: JWTExpiry(accessToken)}, nil
}

func (c *Client) DiscoverModels(ctx context.Context, accessToken string) ([]ModelDetails, error) {
	requestBody, errMarshal := proto.Marshal(&cursorproto.GetUsableModelsRequest{})
	if errMarshal != nil {
		return nil, fmt.Errorf("cursor models: marshal request: %w", errMarshal)
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+ModelsPath, bytes.NewReader(requestBody))
	if errRequest != nil {
		return nil, fmt.Errorf("cursor models: create request: %w", errRequest)
	}
	applyAPIHeaders(req, accessToken, false)
	resp, errDo := c.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("cursor models: request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("cursor models: close response")
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if errRead != nil {
		return nil, fmt.Errorf("cursor models: read response: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cursor models: discovery failed with status %d", resp.StatusCode)
	}
	body = unwrapUnaryConnectFrame(body)
	var decoded cursorproto.GetUsableModelsResponse
	if errDecode := proto.Unmarshal(body, &decoded); errDecode != nil {
		return nil, fmt.Errorf("cursor models: decode response: %w", errDecode)
	}
	models := make([]ModelDetails, 0, len(decoded.Models))
	seen := make(map[string]struct{}, len(decoded.Models))
	for _, model := range decoded.Models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ModelId)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, ModelDetails{
			ID:             id,
			DisplayName:    strings.TrimSpace(model.DisplayName),
			DisplayModelID: strings.TrimSpace(model.DisplayModelId),
			Aliases:        append([]string(nil), model.Aliases...),
			Thinking:       model.ThinkingDetails != nil,
			MaxMode:        model.MaxMode != nil && model.GetMaxMode(),
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("cursor models: discovery returned no usable models")
	}
	return models, nil
}

func applyAPIHeaders(req *http.Request, accessToken string, streaming bool) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("X-Ghost-Mode", "true")
	req.Header.Set("X-Cursor-Client-Version", ClientVersion)
	req.Header.Set("X-Cursor-Client-Type", ClientType)
	req.Header.Set("X-Request-ID", uuid.NewString())
	if streaming {
		req.Header.Set("Content-Type", "application/connect+proto")
		req.Header.Set("Connect-Protocol-Version", "1")
		return
	}
	req.Header.Set("Content-Type", "application/proto")
}

func JWTExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Now().Add(time.Hour)
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		return time.Now().Add(time.Hour)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if errJSON := json.Unmarshal(payload, &claims); errJSON != nil || claims.Exp <= 0 {
		return time.Now().Add(time.Hour)
	}
	return time.Unix(claims.Exp, 0).UTC()
}

func CredentialFileName(accessToken string) string {
	identity := jwtIdentity(accessToken)
	if identity == "" {
		identity = accessToken
	}
	digest := sha256.Sum256([]byte(identity))
	return "cursor-" + hex.EncodeToString(digest[:4]) + ".json"
}

func JWTEmail(accessToken string) string {
	claims := jwtClaims(accessToken)
	for _, key := range []string{"email", "preferred_username"} {
		if value, ok := claims[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func jwtIdentity(accessToken string) string {
	claims := jwtClaims(accessToken)
	for _, key := range []string{"sub", "userId", "user_id", "uuid", "email"} {
		if value, ok := claims[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		return nil
	}
	claims := make(map[string]any)
	if errJSON := json.Unmarshal(payload, &claims); errJSON != nil {
		return nil
	}
	return claims
}

func unwrapUnaryConnectFrame(body []byte) []byte {
	if len(body) < 5 || body[0]&2 != 0 {
		return body
	}
	length := int(body[1])<<24 | int(body[2])<<16 | int(body[3])<<8 | int(body[4])
	if length < 0 || length > len(body)-5 {
		return body
	}
	return body[5 : 5+length]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
