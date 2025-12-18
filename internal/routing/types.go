// Package routing provides request classification and routing configuration
// for the KorProxy intelligent routing system.
package routing

import (
	"encoding/json"
	"time"
)

// RequestType represents the classification of an API request.
type RequestType string

const (
	RequestTypeChat       RequestType = "chat"
	RequestTypeCompletion RequestType = "completion"
	RequestTypeEmbedding  RequestType = "embedding"
	RequestTypeOther      RequestType = "other"
)

// IsValid checks if the request type is a known value.
func (rt RequestType) IsValid() bool {
	switch rt {
	case RequestTypeChat, RequestTypeCompletion, RequestTypeEmbedding, RequestTypeOther:
		return true
	default:
		return false
	}
}

// SelectionStrategy defines how accounts are selected within a provider group.
type SelectionStrategy string

const (
	SelectionRoundRobin SelectionStrategy = "round-robin"
	SelectionRandom     SelectionStrategy = "random"
	SelectionPriority   SelectionStrategy = "priority"
)

// IsValid checks if the selection strategy is a known value.
func (ss SelectionStrategy) IsValid() bool {
	switch ss {
	case SelectionRoundRobin, SelectionRandom, SelectionPriority:
		return true
	default:
		return false
	}
}

// RoutingRules maps request types to provider group IDs.
type RoutingRules struct {
	Chat       *string `json:"chat"`
	Completion *string `json:"completion"`
	Embedding  *string `json:"embedding"`
	Other      *string `json:"other"`
}

// Get returns the provider group ID for a given request type.
func (rr *RoutingRules) Get(rt RequestType) *string {
	if rr == nil {
		return nil
	}
	switch rt {
	case RequestTypeChat:
		return rr.Chat
	case RequestTypeCompletion:
		return rr.Completion
	case RequestTypeEmbedding:
		return rr.Embedding
	case RequestTypeOther:
		return rr.Other
	default:
		return nil
	}
}

// Profile represents a workspace configuration with routing rules.
type Profile struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	Color                string       `json:"color"`
	Icon                 string       `json:"icon,omitempty"`
	RoutingRules         RoutingRules `json:"routingRules"`
	DefaultProviderGroup *string      `json:"defaultProviderGroup"`
	CreatedAt            time.Time    `json:"createdAt"`
	UpdatedAt            time.Time    `json:"updatedAt"`
}

// ProviderGroup is a logical grouping of accounts for load balancing.
type ProviderGroup struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	AccountIDs        []string          `json:"accountIds"`
	SelectionStrategy SelectionStrategy `json:"selectionStrategy"`
}

// ModelFamilies defines model name patterns for request classification.
type ModelFamilies struct {
	Chat       []string `json:"chat"`
	Completion []string `json:"completion"`
	Embedding  []string `json:"embedding"`
}

// RoutingConfig is the root configuration structure shared with Electron.
type RoutingConfig struct {
	Version         int             `json:"version"`
	ActiveProfileID *string         `json:"activeProfileId"`
	Profiles        []Profile       `json:"profiles"`
	ProviderGroups  []ProviderGroup `json:"providerGroups"`
	ModelFamilies   ModelFamilies   `json:"modelFamilies"`
}

// DefaultRoutingConfig returns a new routing config with sensible defaults.
func DefaultRoutingConfig() *RoutingConfig {
	return &RoutingConfig{
		Version:         1,
		ActiveProfileID: nil,
		Profiles:        []Profile{},
		ProviderGroups:  []ProviderGroup{},
		ModelFamilies: ModelFamilies{
			Chat: []string{
				"gpt-4*",
				"gpt-5*",
				"claude-*",
				"gemini-*-pro*",
				"gemini-3-*",
			},
			Completion: []string{
				"gpt-3.5-turbo-instruct",
				"code-*",
				"*-codex*",
			},
			Embedding: []string{
				"text-embedding-*",
				"embed-*",
			},
		},
	}
}

// GetActiveProfile returns the currently active profile, or nil if none.
func (rc *RoutingConfig) GetActiveProfile() *Profile {
	if rc == nil || rc.ActiveProfileID == nil {
		return nil
	}
	for i := range rc.Profiles {
		if rc.Profiles[i].ID == *rc.ActiveProfileID {
			return &rc.Profiles[i]
		}
	}
	return nil
}

// GetProviderGroup finds a provider group by ID.
func (rc *RoutingConfig) GetProviderGroup(id string) *ProviderGroup {
	if rc == nil || id == "" {
		return nil
	}
	for i := range rc.ProviderGroups {
		if rc.ProviderGroups[i].ID == id {
			return &rc.ProviderGroups[i]
		}
	}
	return nil
}

// ResolveProviderGroup determines which provider group should handle a request.
func (rc *RoutingConfig) ResolveProviderGroup(requestType RequestType) *ProviderGroup {
	profile := rc.GetActiveProfile()
	if profile == nil {
		return nil
	}

	groupID := profile.RoutingRules.Get(requestType)
	if groupID == nil {
		groupID = profile.DefaultProviderGroup
	}
	if groupID == nil {
		return nil
	}

	return rc.GetProviderGroup(*groupID)
}

// Validate checks the config for consistency errors.
func (rc *RoutingConfig) Validate() error {
	if rc == nil {
		return nil
	}
	if rc.Version < 1 {
		rc.Version = 1
	}
	return nil
}

// MarshalJSON implements custom JSON marshaling with time format handling.
func (p Profile) MarshalJSON() ([]byte, error) {
	type Alias Profile
	return json.Marshal(&struct {
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
		Alias
	}{
		CreatedAt: p.CreatedAt.Format(time.RFC3339),
		UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
		Alias:     Alias(p),
	})
}

// UnmarshalJSON implements custom JSON unmarshaling with time format handling.
func (p *Profile) UnmarshalJSON(data []byte) error {
	type Alias Profile
	aux := &struct {
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
		*Alias
	}{
		Alias: (*Alias)(p),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	var err error
	if aux.CreatedAt != "" {
		p.CreatedAt, err = time.Parse(time.RFC3339, aux.CreatedAt)
		if err != nil {
			return err
		}
	}
	if aux.UpdatedAt != "" {
		p.UpdatedAt, err = time.Parse(time.RFC3339, aux.UpdatedAt)
		if err != nil {
			return err
		}
	}
	return nil
}
