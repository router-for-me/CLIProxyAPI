package helps

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// AnyrouterBalance holds the parsed account snapshot for an anyrouter.top
// auth, sourced from https://anyrouter.top/api/user/self.
//
// The "quota" / "used_quota" fields are integer points (mille-units in their
// internal economy); the frontend renders them as raw numbers since the unit
// label belongs to the platform UI rather than this proxy.
type AnyrouterBalance struct {
	UserID            int64     `json:"user_id"`
	Username          string    `json:"username,omitempty"`
	DisplayName       string    `json:"display_name,omitempty"`
	Group             string    `json:"group,omitempty"`
	Quota             int64     `json:"quota"`
	UsedQuota         int64     `json:"used_quota"`
	RequestCount      int64     `json:"request_count"`
	AffCode           string    `json:"aff_code,omitempty"`
	AffCount          int64     `json:"aff_count"`
	AffQuota          int64     `json:"aff_quota"`
	AffHistoryQuota   int64     `json:"aff_history_quota"`
	Source            string    `json:"source,omitempty"`
	FetchedAt         time.Time `json:"fetched_at"`
}

// AnyrouterCredentials carries the Cookie + new-api-user header.
// Both must come from a logged-in browser session at anyrouter.top.
type AnyrouterCredentials struct {
	Cookies    string
	NewAPIUser string // numeric user id, sent as `new-api-user` header
}

func (c AnyrouterCredentials) HasAny() bool {
	return strings.TrimSpace(c.Cookies) != "" && strings.TrimSpace(c.NewAPIUser) != ""
}

const (
	anyrouterBalanceRefreshInterval = 10 * time.Minute
	anyrouterBalanceFetchTimeout    = 15 * time.Second
	anyrouterUserSelfURL            = "https://anyrouter.top/api/user/self"
)

var (
	anyrouterBalanceCache    sync.Map
	anyrouterBalanceFetching sync.Map
)

type anyrouterBalanceEntry struct {
	balance *AnyrouterBalance
	err     error
}

func GetAnyrouterBalanceWithCreds(creds AnyrouterCredentials, cfg *config.Config, auth *cliproxyauth.Auth) *AnyrouterBalance {
	if !creds.HasAny() {
		return nil
	}
	key := anyrouterCredKey(creds)
	if val, ok := anyrouterBalanceCache.Load(key); ok {
		entry := val.(*anyrouterBalanceEntry)
		if entry.balance != nil && time.Since(entry.balance.FetchedAt) < anyrouterBalanceRefreshInterval {
			return entry.balance
		}
	}
	return fetchAndCacheAnyrouterBalance(key, creds, cfg, auth)
}

func RefreshAnyrouterBalanceWithCreds(creds AnyrouterCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*AnyrouterBalance, error) {
	if !creds.HasAny() {
		return nil, fmt.Errorf("no anyrouter credentials provided")
	}
	key := anyrouterCredKey(creds)
	anyrouterBalanceCache.Delete(key)

	balance := fetchAndCacheAnyrouterBalance(key, creds, cfg, auth)
	if balance == nil {
		if val, ok := anyrouterBalanceCache.Load(key); ok {
			entry := val.(*anyrouterBalanceEntry)
			if entry.err != nil {
				return nil, entry.err
			}
		}
		return nil, fmt.Errorf("failed to fetch anyrouter balance")
	}
	return balance, nil
}

func fetchAndCacheAnyrouterBalance(key string, creds AnyrouterCredentials, cfg *config.Config, auth *cliproxyauth.Auth) *AnyrouterBalance {
	if _, busy := anyrouterBalanceFetching.LoadOrStore(key, struct{}{}); busy {
		if val, ok := anyrouterBalanceCache.Load(key); ok {
			entry := val.(*anyrouterBalanceEntry)
			if entry.balance != nil {
				return entry.balance
			}
		}
		return nil
	}
	defer anyrouterBalanceFetching.Delete(key)

	ctx, cancel := context.WithTimeout(context.Background(), anyrouterBalanceFetchTimeout)
	defer cancel()

	balance, err := fetchAnyrouterBalance(ctx, creds, cfg, auth)
	if err != nil {
		log.Debugf("anyrouter balance fetch failed: %v", err)
		if val, ok := anyrouterBalanceCache.Load(key); ok {
			entry := val.(*anyrouterBalanceEntry)
			if entry.balance != nil {
				return entry.balance
			}
		}
		anyrouterBalanceCache.Store(key, &anyrouterBalanceEntry{err: err})
		return nil
	}
	anyrouterBalanceCache.Store(key, &anyrouterBalanceEntry{balance: balance})
	return balance
}

func anyrouterCredKey(creds AnyrouterCredentials) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(creds.Cookies)))
	h.Write([]byte{'|'})
	h.Write([]byte(strings.TrimSpace(creds.NewAPIUser)))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func httpClientForAnyrouter(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) *http.Client {
	if cfg != nil || auth != nil {
		return NewProxyAwareHTTPClient(ctx, cfg, auth, anyrouterBalanceFetchTimeout)
	}
	return http.DefaultClient
}

func fetchAnyrouterBalance(ctx context.Context, creds AnyrouterCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*AnyrouterBalance, error) {
	cookies := strings.TrimSpace(creds.Cookies)
	user := strings.TrimSpace(creds.NewAPIUser)
	if cookies == "" || user == "" {
		return nil, fmt.Errorf("missing cookie or new-api-user")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anyrouterUserSelfURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Cookie", cookies)
	req.Header.Set("new-api-user", user)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("User-Agent", "cli-proxy-api/anyrouter-balance")
	req.Header.Set("Referer", "https://anyrouter.top/console/personal")

	client := httpClientForAnyrouter(ctx, cfg, auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("user/self: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("anyrouter balance: close response body error: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user/self returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	balance, err := parseAnyrouterUserSelf(body)
	if err != nil {
		return nil, err
	}
	balance.Source = "cookie"
	balance.FetchedAt = time.Now()
	return balance, nil
}

func parseAnyrouterUserSelf(body []byte) (*AnyrouterBalance, error) {
	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			ID                int64  `json:"id"`
			Username          string `json:"username"`
			DisplayName       string `json:"display_name"`
			Group             string `json:"group"`
			Quota             int64  `json:"quota"`
			UsedQuota         int64  `json:"used_quota"`
			RequestCount      int64  `json:"request_count"`
			AffCode           string `json:"aff_code"`
			AffCount          int64  `json:"aff_count"`
			AffQuota          int64  `json:"aff_quota"`
			AffHistoryQuota   int64  `json:"aff_history_quota"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode user/self response: %w", err)
	}
	if !payload.Success {
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			msg = "anyrouter platform reported failure"
		}
		return nil, fmt.Errorf("anyrouter: %s", msg)
	}
	d := payload.Data
	return &AnyrouterBalance{
		UserID:          d.ID,
		Username:        d.Username,
		DisplayName:     d.DisplayName,
		Group:           d.Group,
		Quota:           d.Quota,
		UsedQuota:       d.UsedQuota,
		RequestCount:    d.RequestCount,
		AffCode:         d.AffCode,
		AffCount:        d.AffCount,
		AffQuota:        d.AffQuota,
		AffHistoryQuota: d.AffHistoryQuota,
	}, nil
}
