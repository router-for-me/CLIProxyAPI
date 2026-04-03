package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	CopilotInternalUserURL = "https://api.github.com/copilot_internal/user"
)

func FetchQuota(ctx context.Context, httpClient *http.Client, accessToken string, userAgent string) (*QuotaResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotInternalUserURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	switch resp.StatusCode {
	case 200:
	case 401:
		return nil, ErrTokenRevoked
	case 403:
		return nil, ErrRateLimited
	case 404:
		return nil, ErrNoCopilotSubscription
	default:
		return nil, fmt.Errorf("GitHub API error (status %d): %w", resp.StatusCode, ErrAPIUnavailable)
	}

	var quota QuotaResponse
	if err := json.Unmarshal(body, &quota); err != nil {
		return nil, fmt.Errorf("failed to unmarshal quota response: %w", err)
	}

	if quota.QuotaSnapshots == nil {
		return nil, ErrNoCopilotSubscription
	}

	return &quota, nil
}

func calculateUsage(snapshot QuotaSnapshot) (used int, percentUsed float64) {
	if snapshot.Unlimited || snapshot.Entitlement <= 0 {
		return 0, 0.0
	}

	used = snapshot.Entitlement - snapshot.Remaining
	if used < 0 {
		used = 0
	}

	percentUsed = float64(used) / float64(snapshot.Entitlement) * 100
	return used, percentUsed
}

func enrichSnapshot(snapshot QuotaSnapshot) AccountQuotaSnapshot {
	used, percentUsed := calculateUsage(snapshot)
	return AccountQuotaSnapshot{
		Entitlement: snapshot.Entitlement,
		Remaining:   snapshot.Remaining,
		Used:        used,
		PercentUsed: percentUsed,
		Unlimited:   snapshot.Unlimited,
	}
}

func EnrichQuotaResponse(resp *QuotaResponse) AccountQuotaSnapshots {
	if resp == nil || resp.QuotaSnapshots == nil {
		return AccountQuotaSnapshots{}
	}

	return AccountQuotaSnapshots{
		PremiumInteractions: enrichSnapshot(resp.QuotaSnapshots.PremiumInteractions),
		Completions:         enrichSnapshot(resp.QuotaSnapshots.Completions),
		Chat:                enrichSnapshot(resp.QuotaSnapshots.Chat),
	}
}
