package executor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	tls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// utlsTransport is a minimal Chrome-fingerprint TLS transport for test use.
// Supports HTTP CONNECT proxy tunneling.
type utlsTransport struct {
	proxyURL string
}

func newUtlsTransport(proxyURL string) *utlsTransport {
	return &utlsTransport{proxyURL: proxyURL}
}

func (t *utlsTransport) dial(addr string) (net.Conn, error) {
	if t.proxyURL == "" {
		return net.Dial("tcp", addr)
	}
	u, err := url.Parse(t.proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url: %w", err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("connect to proxy: %w", err)
	}
	// HTTP CONNECT tunnel
	req, _ := http.NewRequest(http.MethodConnect, "http://"+addr, nil)
	req.Host = addr
	if err = req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}
	return conn, nil
}

func (t *utlsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	addr := host + ":443"

	conn, err := t.dial(addr)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	tlsConn := tls.UClient(conn, &tls.Config{ServerName: host}, tls.HelloChrome_Auto)
	if err = tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}

	tr := &http2.Transport{}
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("h2 conn: %w", err)
	}

	return h2Conn.RoundTrip(req)
}

// planTypePriority returns a numeric priority for a plan_type string.
// Higher value means higher priority: team > plus > free > others.
func planTypePriority(planType string) int {
	switch strings.ToLower(planType) {
	case "team":
		return 3
	case "plus":
		return 2
	case "free":
		return 1
	default:
		return 0
	}
}

// pickBestAccountID selects the best account_id from the $.accounts map returned by
// the accounts/check API. Priority: team > plus > free > any other.
// Returns empty string if no accounts are found.
func pickBestAccountID(accounts map[string]any) string {
	bestID := ""
	bestPriority := -1
	for accountID, v := range accounts {
		info, ok := v.(map[string]any)
		if !ok {
			continue
		}
		account, ok := info["account"].(map[string]any)
		if !ok {
			continue
		}
		planType, _ := account["plan_type"].(string)
		p := planTypePriority(planType)
		if p > bestPriority {
			bestPriority = p
			bestID = accountID
		}
	}
	return bestID
}

// TestCodexAccountCheck tests GET https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27
// using a real access_token. Set CODEX_ACCESS_TOKEN (and optionally CODEX_PROXY_URL) to run.
//
// Example:
//
//	CODEX_ACCESS_TOKEN=eyJ... go test ./internal/runtime/executor/... -run TestCodexAccountCheck -v
//	CODEX_ACCESS_TOKEN=eyJ... CODEX_PROXY_URL=http://127.0.0.1:7890 go test ./internal/runtime/executor/... -run TestCodexAccountCheck -v
func TestCodexAccountCheck(t *testing.T) {
	accessToken := os.Getenv("CODEX_ACCESS_TOKEN")
	if accessToken == "" {
		t.Skip("skipping: CODEX_ACCESS_TOKEN not set")
	}
	proxyURL := os.Getenv("CODEX_PROXY_URL")
	deviceID := uuid.NewString()
	targetURL := "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=-480"

	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-language", "zh-HK,zh;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("oai-device-id", deviceID)
	req.Header.Set("oai-language", "zh-HK")
	req.Header.Set("referer", "https://chatgpt.com/")
	req.Header.Set("user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	req.Header.Set("sec-ch-ua", `"Google Chrome";v="141", "Not?A_Brand";v="8", "Chromium";v="141"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"macOS"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("priority", "u=1, i")

	client := &http.Client{
		Transport: newUtlsTransport(proxyURL),
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	t.Logf("status: %d", resp.StatusCode)
	t.Logf("device_id: %s", deviceID)
	t.Logf("response: %s", string(body))

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
		return
	}

	// Parse response and pick the best account_id
	var parsed map[string]any
	if err = json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if accounts, ok := parsed["accounts"].(map[string]any); ok {
		bestID := pickBestAccountID(accounts)
		t.Logf("best_account_id (team>plus>free): %s", bestID)
	} else {
		t.Logf("no $.accounts map found in response")
	}
}
