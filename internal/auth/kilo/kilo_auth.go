// Package kilo provides authentication and token management functionality
// for Kilo AI services.
package kilo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	// BaseURL is the base URL for the Kilo AI API.
	BaseURL = "https://api.kilo.ai/api"
)

// DeviceAuthResponse represents the response from initiating device flow.
type DeviceAuthResponse struct {
	Code            string `json:"code"`
	VerificationURL string `json:"verificationUrl"`
	ExpiresIn       int    `json:"expiresIn"`
}

// DeviceStatusResponse represents the response when polling for device flow status.
type DeviceStatusResponse struct {
	Status    string `json:"status"`
	Token     string `json:"token"`
	UserEmail string `json:"userEmail"`
}

// Profile represents the user profile from Kilo AI.
type Profile struct {
	Email string         `json:"email"`
	Orgs  []Organization `json:"organizations"`
}

// Organization represents a Kilo AI organization.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Defaults represents default settings for an organization or user.
type Defaults struct {
	Model string `json:"model"`
}

// KiloAuth provides methods for handling the Kilo AI authentication flow.
type KiloAuth struct {
	client *http.Client
}

// NewKiloAuth creates a new instance of KiloAuth.
func NewKiloAuth() *KiloAuth {
	return &KiloAuth{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// InitiateDeviceFlow starts the device authentication flow.
func (k *KiloAuth) InitiateDeviceFlow(ctx context.Context) (*DeviceAuthResponse, error) {
	resp, err := k.client.Post(BaseURL+"/device-auth/codes", "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to initiate device flow: status %d", resp.StatusCode)
	}

	var data DeviceAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

// PollForToken polls for the device flow completion.
func (k *KiloAuth) PollForToken(ctx context.Context, code string) (*DeviceStatusResponse, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			resp, err := k.client.Get(BaseURL + "/device-auth/codes/" + code)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()

			var data DeviceStatusResponse
			if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
				return nil, err
			}

			switch data.Status {
			case "approved":
				return &data, nil
			case "denied", "expired":
				return nil, fmt.Errorf("device flow %s", data.Status)
			case "pending":
				continue
			default:
				return nil, fmt.Errorf("unknown status: %s", data.Status)
			}
		}
	}
}

// GetProfile fetches the user's profile.
func (k *KiloAuth) GetProfile(ctx context.Context, token string) (*Profile, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", BaseURL+"/profile", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create get profile request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get profile: status %d", resp.StatusCode)
	}

	var profile Profile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

// GetDefaults fetches default settings for an organization.
func (k *KiloAuth) GetDefaults(ctx context.Context, token, orgID string) (*Defaults, error) {
	url := BaseURL + "/defaults"
	if orgID != "" {
		url = BaseURL + "/organizations/" + orgID + "/defaults"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create get defaults request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get defaults: status %d", resp.StatusCode)
	}

	var defaults Defaults
	if err := json.NewDecoder(resp.Body).Decode(&defaults); err != nil {
		return nil, err
	}
	return &defaults, nil
}
