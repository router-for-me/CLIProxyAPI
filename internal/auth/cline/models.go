package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// ModelsMetadataKey is the auth metadata key holding the per-account detected
// model catalog. The key's presence (even with an empty list) marks detection
// as done and suppresses further fetch attempts.
const ModelsMetadataKey = "models"

// ClineModelInfo describes one model entry from the Cline account models API.
// Entries look like {"id":"moonshotai/kimi-k3","name":"kimi-k3",
// "description":"...","tags":["NEW"]}.
type ClineModelInfo struct {
	// ID is the upstream model identifier ("provider/slug" for usage-billed
	// models, "cline-pass/..." for subscription models).
	ID string `json:"id"`
	// Name is the short display name.
	Name string `json:"name,omitempty"`
	// Description is the catalog description.
	Description string `json:"description,omitempty"`
	// Tags holds upstream tags such as "NEW".
	Tags []string `json:"tags,omitempty"`
}

// clineDetectStates tracks completed per-credential model detections.
type clineDetectState struct {
	err  error
	done chan struct{}
}

var clineDetectStates sync.Map

// FetchAvailableModels returns the models available to the account behind a
// stored Cline access token. It hits the authenticated models endpoint used
// by Cline's own clients to render the per-account model picker.
func (a *ClineAuth) FetchAvailableModels(ctx context.Context, accessToken string) ([]ClineModelInfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("cline models: access token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	endpoint := strings.TrimSuffix(a.apiBaseURL, "/") + "/ai/cline/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("cline models: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline models request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("cline models: close response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cline models: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cline models request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	models, errParse := ParseClineModels(body)
	if errParse != nil {
		return nil, fmt.Errorf("cline models: parse response: %w", errParse)
	}
	return models, nil
}

// ParseClineModels parses the Cline models response defensively. Observed shapes:
//   - bare array: [{"id":"...","name":"...","description":"...","tags":[...]}, ...]
//   - envelope: {"success":true,"data":[...]}
//   - bucketed object: {"recommended":[...],"free":[...],"clinePass":[...],"clineCloud":[...]}
//
// Buckets are merged and deduplicated by id, preserving first-seen order.
func ParseClineModels(data []byte) ([]ClineModelInfo, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("empty body")
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	switch typed := payload.(type) {
	case []any:
		return normalizeClineModelEntries(typed), nil
	case map[string]any:
		return parseClineModelBuckets(typed), nil
	default:
		return nil, fmt.Errorf("unexpected response shape %T", payload)
	}
}

// parseClineModelBuckets handles envelope and bucketed object shapes. It first
// unwraps a {"data":[...]} envelope, then merges every array bucket of the
// object ("recommended", "free", "clinePass", "clineCloud", or any other key).
func parseClineModelBuckets(obj map[string]any) []ClineModelInfo {
	if dataRaw, ok := obj["data"]; ok {
		if dataArr, isArr := dataRaw.([]any); isArr {
			return normalizeClineModelEntries(dataArr)
		}
		if nested, isObj := dataRaw.(map[string]any); isObj {
			return parseClineModelBuckets(nested)
		}
	}
	merged := make([]ClineModelInfo, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, raw := range obj {
		arr, isArr := raw.([]any)
		if !isArr {
			continue
		}
		for _, entry := range normalizeClineModelEntries(arr) {
			id := strings.TrimSpace(entry.ID)
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, entry)
		}
	}
	return merged
}

func normalizeClineModelEntries(arr []any) []ClineModelInfo {
	out := make([]ClineModelInfo, 0, len(arr))
	for _, item := range arr {
		obj, isObj := item.(map[string]any)
		if !isObj {
			continue
		}
		entry := ClineModelInfo{
			ID:          strings.TrimSpace(clineJSONString(obj["id"])),
			Name:        strings.TrimSpace(clineJSONString(obj["name"])),
			Description: strings.TrimSpace(clineJSONString(obj["description"])),
			Tags:        clineJSONStringSlice(obj["tags"]),
		}
		if entry.ID == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func clineJSONString(raw any) string {
	if s, ok := raw.(string); ok {
		return s
	}
	return ""
}

func clineJSONStringSlice(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, isStr := item.(string); isStr && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ModelsFromMetadata extracts the detected model list from auth metadata,
// tolerating both the in-memory []ClineModelInfo form and the JSON round-trip
// form ([]any of maps). Returns nil when the key is absent.
func ModelsFromMetadata(metadata map[string]any) []ClineModelInfo {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata[ModelsMetadataKey]
	if !ok || raw == nil {
		return nil
	}
	return decodeClineModels(raw)
}

// DecodeClineModels normalizes any stored representation of the detected
// model list into []ClineModelInfo.
func decodeClineModels(raw any) []ClineModelInfo {
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var payload any
	if err = json.Unmarshal(rawJSON, &payload); err != nil {
		return nil
	}
	arr, isArr := payload.([]any)
	if !isArr {
		return nil
	}
	return normalizeClineModelEntries(arr)
}

// DetectAvailableModelsOnce runs fn exactly once per key across the process:
// the first caller for a key executes fn, every later caller (concurrent or
// sequential) returns the leader's result without re-running fn. The result
// is remembered for the process lifetime, so a successful detection is never
// repeated for the same credential.
func DetectAvailableModelsOnce(flightKey string, fn func() error) error {
	key := strings.TrimSpace(flightKey)
	if key == "" {
		return fmt.Errorf("cline models: empty detection key")
	}
	stored, loaded := clineDetectStates.LoadOrStore(key, &clineDetectState{done: make(chan struct{})})
	state := stored.(*clineDetectState)
	if !loaded {
		defer close(state.done)
		if fn == nil {
			return nil
		}
		state.err = fn()
		return state.err
	}
	<-state.done
	return state.err
}
