package rovo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SanitizeRovoFileName normalizes user identifiers for safe filename usage.
func SanitizeRovoFileName(raw string) string {
	if raw == "" {
		return ""
	}
	cleanEmail := strings.ReplaceAll(raw, "*", "x")
	var result strings.Builder
	for _, r := range cleanEmail {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '@' || r == '.' || r == '-' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// EncodeEmailToken returns base64(email:apiKey) for X-Atlassian-EncodedToken.
func EncodeEmailToken(email, apiKey string) string {
	email = strings.TrimSpace(email)
	apiKey = strings.TrimSpace(apiKey)
	if email == "" || apiKey == "" {
		return ""
	}
	raw := email + ":" + apiKey
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// FetchCloudID retrieves the Atlassian cloud ID using Rovo sites API.
// It uses Basic auth with email:apiKey and returns the first eligible site cloudId.
func FetchCloudID(ctx context.Context, email, apiKey, baseURL string, httpClient *http.Client) (string, error) {
	email = strings.TrimSpace(email)
	apiKey = strings.TrimSpace(apiKey)
	if email == "" || apiKey == "" {
		return "", fmt.Errorf("rovo: email or api key is empty")
	}

	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.atlassian.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/rovodev/v3/sites", nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Basic "+basicAuth(email, apiKey))
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("rovo: sites request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Sites []struct {
			SiteID                                string `json:"siteId"`
			SiteURL                               string `json:"siteUrl"`
			HasActiveRovoDevSKU                   bool   `json:"hasActiveRovoDevSKU"`
			HasActiveRovoDevEvery                 bool   `json:"hasActiveRovoDevEverywhereEligibleSKU"`
			IsTrial                               bool   `json:"isTrial"`
			HasActiveRovoDevEverywhereEligibleSKU bool   `json:"hasActiveRovoDevEverywhereEligibleSKU"`
		} `json:"sites"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("rovo: decode sites response failed: %w", err)
	}

	// Prefer sites with active Rovo Dev SKU.
	for _, site := range payload.Sites {
		if strings.TrimSpace(site.SiteID) != "" && site.HasActiveRovoDevSKU {
			return strings.TrimSpace(site.SiteID), nil
		}
	}

	// Fallback to the first available site.
	if len(payload.Sites) > 0 {
		if id := strings.TrimSpace(payload.Sites[0].SiteID); id != "" {
			return id, nil
		}
	}

	return "", fmt.Errorf("rovo: no cloud id found in sites response")
}

func basicAuth(email, apiKey string) string {
	raw := email + ":" + apiKey
	return base64.StdEncoding.EncodeToString([]byte(raw))
}
