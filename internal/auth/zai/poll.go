package zai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultOAuthBaseURL hosts the server-mediated CLI OAuth flow. The init
	// and poll endpoints avoid redirect URIs entirely, so browser custom-scheme
	// handling and loopback callbacks are not needed.
	DefaultOAuthBaseURL = "https://zcode.z.ai/api/v1"

	// defaultPollInterval is used when the server does not advertise one.
	defaultPollInterval = 2 * time.Second
	// maxPollDuration bounds how long we wait for the user to authorize.
	maxPollDuration = 10 * time.Minute
	// maxConsecutivePollErrors tolerates transient polling failures.
	maxConsecutivePollErrors = 5
	// pollTokenBytes is the entropy of the client-generated poll token.
	pollTokenBytes = 32
)

// FlowInfo describes one server-mediated OAuth login flow.
type FlowInfo struct {
	FlowID          string
	PollToken       string
	AuthorizeURL    string
	ExpiresAt       int64
	PollIntervalSec int
}

// SetCLIBaseURL overrides the CLI OAuth base endpoint (tests only).
func (s *Service) SetCLIBaseURL(baseURL string) {
	if s == nil {
		return
	}
	if trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/"); trimmed != "" {
		s.cliBaseURL = trimmed
	}
}

// InitFlow starts a server-mediated login: z.ai returns the authorize URL to
// open and a poll token used to wait for the user's approval.
func (s *Service) InitFlow(ctx context.Context) (*FlowInfo, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("zai oauth service unavailable")
	}
	clientToken, errToken := newPollToken()
	if errToken != nil {
		return nil, fmt.Errorf("generate Z.ai poll token: %w", errToken)
	}
	body, errMarshal := json.Marshal(map[string]string{"provider": "zai"})
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal Z.ai init request: %w", errMarshal)
	}
	var data struct {
		FlowID          string `json:"flow_id"`
		PollToken       string `json:"poll_token"`
		AuthorizeURL    string `json:"authorize_url"`
		ExpiresAt       int64  `json:"expires_at"`
		PollIntervalSec int    `json:"poll_interval_sec"`
	}
	if errDo := s.doEnvelope(ctx, http.MethodPost, s.cliBaseURL+"/oauth/cli/init", clientToken, body, &data); errDo != nil {
		return nil, errDo
	}
	if strings.TrimSpace(data.FlowID) == "" || strings.TrimSpace(data.AuthorizeURL) == "" {
		return nil, fmt.Errorf("Z.ai init response is missing flow_id or authorize_url")
	}
	// The server returns the authoritative poll token; fall back to the
	// client-generated one only if the server omitted it.
	pollToken := strings.TrimSpace(data.PollToken)
	if pollToken == "" {
		pollToken = clientToken
	}
	return &FlowInfo{
		FlowID:          strings.TrimSpace(data.FlowID),
		PollToken:       pollToken,
		AuthorizeURL:    strings.TrimSpace(data.AuthorizeURL),
		ExpiresAt:       data.ExpiresAt,
		PollIntervalSec: data.PollIntervalSec,
	}, nil
}

// WaitForFlow polls until the user approves the flow, then returns the token
// bundle (business token exchange is handled by the caller).
func (s *Service) WaitForFlow(ctx context.Context, flow *FlowInfo) (*TokenBundle, error) {
	if s == nil || flow == nil || strings.TrimSpace(flow.FlowID) == "" {
		return nil, fmt.Errorf("zai oauth flow is not initialized")
	}
	interval := time.Duration(flow.PollIntervalSec) * time.Second
	if interval < defaultPollInterval {
		interval = defaultPollInterval
	}
	deadline := time.Now().Add(maxPollDuration)
	if flow.ExpiresAt > 0 {
		if expiry := time.Unix(flow.ExpiresAt, 0); expiry.Before(deadline) {
			deadline = expiry
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	consecutiveErrors := 0
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout waiting for Z.ai authorization")
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("timeout waiting for Z.ai authorization")
			}
			var data struct {
				Status string `json:"status"`
				Token  string `json:"token"`
				User   struct {
					UserID string `json:"user_id"`
					Email  string `json:"email"`
					Name   string `json:"name"`
				} `json:"user"`
				ZAI struct {
					AccessToken string `json:"access_token"`
				} `json:"zai"`
			}
			errPoll := s.doEnvelope(ctx, http.MethodGet, s.cliBaseURL+"/oauth/cli/poll/"+flow.FlowID, flow.PollToken, nil, &data)
			if errPoll != nil {
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutivePollErrors {
					return nil, errPoll
				}
				continue
			}
			consecutiveErrors = 0
			switch strings.TrimSpace(data.Status) {
			case "pending", "":
				// Keep polling.
			case "failed":
				return nil, fmt.Errorf("Z.ai authorization failed or was denied")
			case "ready":
				oauthToken := strings.TrimSpace(data.ZAI.AccessToken)
				if oauthToken == "" {
					return nil, fmt.Errorf("Z.ai authorization response is missing access_token")
				}
				bundle := &TokenBundle{
					ZCodeToken: strings.TrimSpace(data.Token),
					OAuthToken: oauthToken,
					User: UserInfo{
						ID:    strings.TrimSpace(data.User.UserID),
						Email: strings.TrimSpace(data.User.Email),
						Name:  strings.TrimSpace(data.User.Name),
					},
				}
				return bundle, nil
			default:
				return nil, fmt.Errorf("unexpected Z.ai poll status %q", data.Status)
			}
		}
	}
}

// rawRequest executes an authenticated request and returns the raw body,
// failing on non-2xx statuses.
func (s *Service) rawRequest(ctx context.Context, method, endpoint, bearer string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, errReq := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if errReq != nil {
		return nil, errReq
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(bearer) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	}
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("Z.ai OAuth request failed: %w", errDo)
	}
	defer resp.Body.Close()
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, fmt.Errorf("read Z.ai OAuth response: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Z.ai OAuth request rejected with status %d: %s", resp.StatusCode, truncateForLog(data))
	}
	return data, nil
}

// doEnvelope executes an authenticated CLI OAuth request and decodes the data
// member of the {code,msg,data} response envelope into target.
func (s *Service) doEnvelope(ctx context.Context, method, endpoint, bearer string, body []byte, target any) error {
	data, errFetch := s.rawRequest(ctx, method, endpoint, bearer, body)
	if errFetch != nil {
		return errFetch
	}
	var envelope struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if errJSON := json.Unmarshal(data, &envelope); errJSON == nil && len(envelope.Data) > 0 {
		switch envelope.Code.String() {
		case "", "0", "200":
		default:
			if strings.TrimSpace(envelope.Msg) != "" {
				return fmt.Errorf("Z.ai OAuth rejected: %s", strings.TrimSpace(envelope.Msg))
			}
			return fmt.Errorf("Z.ai OAuth rejected with code %s", envelope.Code.String())
		}
		if errDecode := json.Unmarshal(envelope.Data, target); errDecode != nil {
			return fmt.Errorf("decode Z.ai OAuth response data: %w (body: %s)", errDecode, truncateForLog(data))
		}
		return nil
	}
	// Not an envelope (or empty data): try the raw body directly so endpoints
	// that return bare token objects still work.
	if errDecode := json.Unmarshal(data, target); errDecode != nil {
		return fmt.Errorf("decode Z.ai OAuth response: %w (body: %s)", errDecode, truncateForLog(data))
	}
	return nil
}

func truncateForLog(data []byte) string {
	snippet := strings.TrimSpace(string(data))
	if len(snippet) > 300 {
		snippet = snippet[:300] + "…"
	}
	return snippet
}

func newPollToken() (string, error) {
	buf := make([]byte, pollTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// SetBizBaseURL overrides the business API base endpoint (tests only).
func (s *Service) SetBizBaseURL(baseURL string) {
	if s == nil {
		return
	}
	if trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/"); trimmed != "" {
		s.bizBaseURL = trimmed
	}
}
