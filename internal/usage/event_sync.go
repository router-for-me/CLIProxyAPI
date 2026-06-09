package usage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type usageEventSyncClient struct {
	url        string
	token      string
	hmacSecret string
	httpClient *http.Client
}

func newUsageEventSyncClient(url, token, hmacSecret string) *usageEventSyncClient {
	return &usageEventSyncClient{
		url:        strings.TrimSpace(url),
		token:      strings.TrimSpace(token),
		hmacSecret: strings.TrimSpace(hmacSecret),
		httpClient: http.DefaultClient,
	}
}

func (c *usageEventSyncClient) sync(ctx context.Context, event UsageEvent) error {
	if c == nil || c.url == "" {
		return nil
	}
	if c.token == "" {
		return fmt.Errorf("usage event sync token is empty")
	}
	if c.hmacSecret == "" {
		return fmt.Errorf("usage event sync hmac secret is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal usage event sync body: %w", err)
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create usage event sync request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-internal-token", c.token)
	request.Header.Set("x-usage-timestamp", timestamp)
	request.Header.Set("x-usage-signature", signUsageEventSyncBody(c.hmacSecret, timestamp, body))

	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("post usage event sync request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("usage event sync status %d", response.StatusCode)
	}
	return nil
}

func signUsageEventSyncBody(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
