// Command fetch_antigravity_models connects to the Antigravity API using the
// stored auth credentials and saves the dynamically fetched model list to a
// JSON file for inspection or offline use.
//
// Usage:
//
//	go run ./cmd/fetch_antigravity_models [flags]
//
// Flags:
//
//	--auths-dir <path>  Directory containing auth JSON files (default: "auths")
//	--output    <path>  Output JSON file path             (default: "antigravity_models.json")
//	--pretty            Pretty-print the output JSON      (default: true)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	antigravityBaseURLDaily        = "https://daily-cloudcode-pa.googleapis.com"
	antigravitySandboxBaseURLDaily = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	antigravityBaseURLProd         = "https://cloudcode-pa.googleapis.com"
	antigravityModelsPath          = "/v1internal:fetchAvailableModels"
)

func init() {
	logging.SetupBaseLogger()
	log.SetLevel(log.InfoLevel)
}

// modelOutput wraps the fetched model list with fetch metadata.
type modelOutput struct {
	Models []modelEntry `json:"models"`
}

// modelEntry contains only the fields we want to keep for static model definitions.
type modelEntry struct {
	ID                  string `json:"id"`
	Object              string `json:"object"`
	OwnedBy             string `json:"owned_by"`
	Type                string `json:"type"`
	DisplayName         string `json:"display_name"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	ContextLength       int    `json:"context_length,omitempty"`
	MaxCompletionTokens int    `json:"max_completion_tokens,omitempty"`
}

func main() {
	var authsDir string
	var outputPath string
	var pretty bool

	flag.StringVar(&authsDir, "auths-dir", "auths", "Directory containing auth JSON files")
	flag.StringVar(&outputPath, "output", "antigravity_models.json", "Output JSON file path")
	flag.BoolVar(&pretty, "pretty", true, "Pretty-print the output JSON")
	flag.Parse()

	// Resolve relative paths against the working directory.
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}
	if !filepath.IsAbs(authsDir) {
		authsDir = filepath.Join(wd, authsDir)
	}
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(wd, outputPath)
	}

	fmt.Printf("Scanning auth files in: %s\n", authsDir)

	// Load all auth records from the directory.
	fileStore := sdkauth.NewFileTokenStore()
	fileStore.SetBaseDir(authsDir)

	ctx := context.Background()
	auths, err := fileStore.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to list auth files: %v\n", err)
		os.Exit(1)
	}
	if len(auths) == 0 {
		fmt.Fprintf(os.Stderr, "error: no auth files found in %s\n", authsDir)
		os.Exit(1)
	}

	// Find the first enabled antigravity auth.
	var chosen *coreauth.Auth
	for _, a := range auths {
		if a == nil || a.Disabled {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(a.Provider), "antigravity") {
			chosen = a
			break
		}
	}
	if chosen == nil {
		fmt.Fprintf(os.Stderr, "error: no enabled antigravity auth found in %s\n", authsDir)
		os.Exit(1)
	}

	fmt.Printf("Using auth: id=%s label=%s\n", chosen.ID, chosen.Label)

	// Fetch models from the upstream Antigravity API.
	fmt.Println("Fetching Antigravity model list from upstream...")

	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	models := fetchModels(fetchCtx, chosen)
	if len(models) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no models returned (API may be unavailable or token expired)")
	} else {
		fmt.Printf("Fetched %d models.\n", len(models))
	}

	// Build the output payload.
	out := modelOutput{
		Models: models,
	}

	// Marshal to JSON.
	var raw []byte
	if pretty {
		raw, err = json.MarshalIndent(out, "", "  ")
	} else {
		raw, err = json.Marshal(out)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to marshal JSON: %v\n", err)
		os.Exit(1)
	}

	if err = os.WriteFile(outputPath, raw, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to write output file %s: %v\n", outputPath, err)
		os.Exit(1)
	}

	fmt.Printf("Model list saved to: %s\n", outputPath)
}

func fetchModels(ctx context.Context, auth *coreauth.Auth) []modelEntry {
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		fmt.Fprintln(os.Stderr, "error: no access token found in auth")
		return nil
	}

	baseURLs := []string{antigravityBaseURLProd, antigravityBaseURLDaily, antigravitySandboxBaseURLDaily}

	for _, baseURL := range baseURLs {
		modelsURL := baseURL + antigravityModelsPath

		var payload []byte
		if auth != nil && auth.Metadata != nil {
			if pid, ok := auth.Metadata["project_id"].(string); ok && strings.TrimSpace(pid) != "" {
				payload = []byte(fmt.Sprintf(`{"project": "%s"}`, strings.TrimSpace(pid)))
			}
		}
		if len(payload) == 0 {
			payload = []byte(`{}`)
		}

		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, modelsURL, strings.NewReader(string(payload)))
		if errReq != nil {
			continue
		}
		httpReq.Close = true
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+accessToken)
		httpReq.Header.Set("User-Agent", "antigravity/1.21.9 darwin/arm64")

		httpClient := &http.Client{Timeout: 30 * time.Second}
		if transport, _, errProxy := proxyutil.BuildHTTPTransport(auth.ProxyURL); errProxy == nil && transport != nil {
			httpClient.Transport = transport
		}
		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			continue
		}

		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		if errRead != nil {
			continue
		}

		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			continue
		}

		result := gjson.GetBytes(bodyBytes, "models")
		if !result.Exists() {
			continue
		}

		var models []modelEntry

		for originalName, modelData := range result.Map() {
			modelID := strings.TrimSpace(originalName)
			if modelID == "" {
				continue
			}
			// Skip internal/experimental models
			switch modelID {
			case "chat_20706", "chat_23310", "tab_flash_lite_preview", "tab_jump_flash_lite_preview", "gemini-2.5-flash-thinking", "gemini-2.5-pro":
				continue
			}

			displayName := modelData.Get("displayName").String()
			if displayName == "" {
				displayName = modelID
			}

			entry := modelEntry{
				ID:          modelID,
				Object:      "model",
				OwnedBy:     "antigravity",
				Type:        "antigravity",
				DisplayName: displayName,
				Name:        modelID,
				Description: displayName,
			}

			if maxTok := modelData.Get("maxTokens").Int(); maxTok > 0 {
				entry.ContextLength = int(maxTok)
			}
			if maxOut := modelData.Get("maxOutputTokens").Int(); maxOut > 0 {
				entry.MaxCompletionTokens = int(maxOut)
			}

			models = append(models, entry)
		}

		return models
	}

	return nil
}

func metaStringValue(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	default:
		return ""
	}
}
