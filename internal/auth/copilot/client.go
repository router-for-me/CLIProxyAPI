package copilot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
)

const Provider = "copilot"

const (
	githubURL  = "https://github.com"
	githubAPI  = "https://api.github.com"
	clientID   = "Iv1.b507a08c87ecfe98"
	defaultAPI = "https://api.individual.githubcopilot.com"
)

type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type Token struct {
	Value     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

type Account struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type HTTPError struct {
	Code int
}

func (e HTTPError) Error() string   { return fmt.Sprintf("copilot: upstream HTTP %d", e.Code) }
func (e HTTPError) StatusCode() int { return e.Code }

const CLIEditor = "copilot/1.0.114"

var CLIUserAgent = func() string {
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	return CLIEditor + " (" + platform + " v24.22.1) term/unknown"
}()

// TrustedAPI restricts where inference credentials can be sent.
func TrustedAPI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host != u.Hostname() || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	switch u.Host {
	case "api.githubcopilot.com", "api.individual.githubcopilot.com", "api.business.githubcopilot.com", "api.enterprise.githubcopilot.com":
		return true
	}
	return false
}

func (t Token) API() (string, error) {
	if t.Endpoints.API != "" {
		if !TrustedAPI(t.Endpoints.API) {
			return "", fmt.Errorf("copilot: untrusted API endpoint")
		}
		return strings.TrimRight(t.Endpoints.API, "/"), nil
	}
	for part := range strings.SplitSeq(t.Value, ";") {
		if host, ok := strings.CutPrefix(part, "proxy-ep=proxy."); ok {
			base := "https://api." + host
			if !TrustedAPI(base) {
				return "", fmt.Errorf("copilot: untrusted proxy endpoint")
			}
			return base, nil
		}
	}
	return defaultAPI, nil
}

func RequestJSON(ctx context.Context, client *http.Client, endpoint, bearer string, form url.Values, out any) error {
	method := http.MethodGet
	var body io.Reader
	if form != nil {
		method, body = http.MethodPost, strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", CLIUserAgent)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if strings.HasSuffix(endpoint, "/models") {
		req.Header.Set("Copilot-Integration-Id", "copilot-sdk")
		req.Header.Set("X-GitHub-Api-Version", "2026-09-21")
	}
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return HTTPError{resp.StatusCode}
	}
	const maxBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxBytes {
		return fmt.Errorf("copilot: upstream JSON exceeds size limit")
	}
	if errDecode := json.Unmarshal(data, out); errDecode != nil {
		return fmt.Errorf("copilot: invalid upstream JSON")
	}
	return nil
}

func StartDeviceFlow(ctx context.Context, client *http.Client) (DeviceCode, error) {
	var code DeviceCode
	err := RequestJSON(ctx, client, githubURL+"/login/device/code", "", url.Values{"client_id": {clientID}, "scope": {"read:user"}}, &code)
	if err == nil && (code.DeviceCode == "" || code.UserCode == "" || code.VerificationURI != githubURL+"/login/device" || code.ExpiresIn <= 0 || code.ExpiresIn > 3600 || code.Interval > 3600) {
		err = fmt.Errorf("copilot: invalid device authorization response")
	}
	return code, err
}

func PollDeviceFlow(ctx context.Context, client *http.Client, code DeviceCode) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(code.ExpiresIn)*time.Second)
	defer cancel()
	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	form := url.Values{"client_id": {clientID}, "device_code": {code.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}
		var response struct {
			Token string `json:"access_token"`
			Error string `json:"error"`
		}
		if err := RequestJSON(ctx, client, githubURL+"/login/oauth/access_token", "", form, &response); err != nil {
			return "", err
		}
		if response.Token != "" {
			return response.Token, nil
		}
		switch response.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return "", fmt.Errorf("copilot: GitHub device authorization denied")
		default:
			return "", fmt.Errorf("copilot: GitHub device authorization failed or expired")
		}
		timer.Reset(interval)
	}
}

func GetAccount(ctx context.Context, client *http.Client, githubToken string) (Account, error) {
	var account Account
	err := RequestJSON(ctx, client, githubAPI+"/user", githubToken, nil, &account)
	if err == nil && account.ID <= 0 {
		err = fmt.Errorf("copilot: missing GitHub account ID")
	}
	return account, err
}

func Exchange(ctx context.Context, client *http.Client, githubToken string) (Token, error) {
	var token Token
	err := RequestJSON(ctx, client, githubAPI+"/copilot_internal/v2/token", githubToken, nil, &token)
	if err == nil && (token.Value == "" || token.ExpiresAt <= time.Now().Unix()) {
		err = fmt.Errorf("copilot: invalid token response")
	}
	return token, err
}
