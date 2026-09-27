package compatmodelsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

var (
	syncOnce     sync.Once
	syncInterval = 5 * time.Minute
)

// modelsListResponse matches the standard OpenAI /v1/models response.
type modelsListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// StartOpenAICompatModelSync starts a background goroutine that periodically
// fetches model lists from all OpenAI-compatibility providers and updates
// the on-disk config when changes are detected. The config watcher will
// automatically reload the updated file into memory.
func StartOpenAICompatModelSync(ctx context.Context, configFilePath string) {
	syncOnce.Do(func() {
		go runSync(ctx, configFilePath)
	})
}

func runSync(ctx context.Context, configFilePath string) {
	// Delay first sync so the server is fully up and executors are bound.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	log.Infof("OpenAI compatibility model sync started (interval=%s)", syncInterval)

	// Run initial sync immediately after the startup delay.
	if err := doSync(configFilePath); err != nil {
		log.Warnf("OpenAI compat model sync failed: %v", err)
	}

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Debug("OpenAI compat model sync stopped")
			return
		case <-ticker.C:
			if err := doSync(configFilePath); err != nil {
				log.Warnf("OpenAI compat model sync failed: %v", err)
			}
		}
	}
}

func doSync(configFilePath string) error {
	cfg, err := config.LoadConfig(configFilePath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(cfg.OpenAICompatibility) == 0 {
		return nil
	}

	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: proxyutil.SetInsecureSkipVerify(nil, true),
	}

	changed := false
	for i := range cfg.OpenAICompatibility {
		entry := &cfg.OpenAICompatibility[i]
		if entry.Disabled || strings.TrimSpace(entry.BaseURL) == "" {
			continue
		}

		apiKey := ""
		if len(entry.APIKeyEntries) > 0 {
			apiKey = strings.TrimSpace(entry.APIKeyEntries[0].APIKey)
		}

		models, errFetch := fetchModels(client, entry.BaseURL, apiKey)
		if errFetch != nil {
			log.Debugf("OpenAI compat sync skip %s: %v", entry.Name, errFetch)
			continue
		}

		// Preserve manually configured aliases for models that still exist upstream.
		existingAliases := make(map[string]string, len(entry.Models))
		for _, m := range entry.Models {
			if alias := strings.TrimSpace(m.Alias); alias != "" {
				existingAliases[m.Name] = m.Alias
			}
		}

		newModels := make([]config.OpenAICompatibilityModel, 0, len(models))
		for _, m := range models {
			newModels = append(newModels, config.OpenAICompatibilityModel{
				Name:  m,
				Alias: existingAliases[m],
			})
		}

		if !modelsEqual(entry.Models, newModels) {
			log.Infof("OpenAI compat sync update %s: %d -> %d models", entry.Name, len(entry.Models), len(newModels))
			entry.Models = newModels
			changed = true
		}
	}

	if changed {
		if err := config.SaveConfigPreserveComments(configFilePath, cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		log.Info("OpenAI compat config saved (watcher will auto-reload)")
	}

	return nil
}

func fetchModels(client *http.Client, baseURL, apiKey string) ([]string, error) {
	base := strings.TrimSpace(baseURL)
	base = strings.TrimSuffix(base, "/")
	base = strings.TrimSuffix(base, "/v1")
	url := base + "/v1/models"

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("close models response body: %v", errClose)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result modelsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if strings.TrimSpace(m.ID) != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}

func modelsEqual(a, b []config.OpenAICompatibilityModel) bool {
	if len(a) != len(b) {
		return false
	}
	// Sort copies by Name to ensure order-independent comparison.
	aCopy := make([]config.OpenAICompatibilityModel, len(a))
	copy(aCopy, a)
	bCopy := make([]config.OpenAICompatibilityModel, len(b))
	copy(bCopy, b)
	sort.Slice(aCopy, func(i, j int) bool { return aCopy[i].Name < aCopy[j].Name })
	sort.Slice(bCopy, func(i, j int) bool { return bCopy[i].Name < bCopy[j].Name })
	for i := range aCopy {
		if aCopy[i].Name != bCopy[i].Name || aCopy[i].Alias != bCopy[i].Alias {
			return false
		}
	}
	return true
}
