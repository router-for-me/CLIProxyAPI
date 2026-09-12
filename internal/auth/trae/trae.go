// Package trae provides helpers for reusing a local Trae CLI login.
package trae

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

const (
	Provider             = "trae"
	DefaultCNBaseURL     = "https://copilot-cn.bytedance.net"
	DefaultGlobalBaseURL = "https://copilot.byteintl.net"
	DefaultSGBaseURL     = "https://copilot-sg.byteintl.net"
	RawChatPath          = "/api/ide/v2/llm_raw_chat"
	AppID                = "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8"
)

// Installation contains the local Trae CLI executable and state paths.
type Installation struct {
	CLIPath    string
	Home       string
	AuthPath   string
	ModelsPath string
}

// Credentials is the non-persisted credential view loaded from Trae CLI state.
type Credentials struct {
	AccessToken    string
	UserID         string
	ExpiresAt      string
	Region         string
	LoginMethod    string
	CredentialKind string
	LastRefresh    string
}

// Model is the model catalog shape printed by `traecli models --json`.
type Model struct {
	Name               string   `json:"name"`
	ConfigName         string   `json:"config_name,omitempty"`
	BackendModel       string   `json:"backend_model,omitempty"`
	Provider           string   `json:"provider,omitempty"`
	Description        string   `json:"description,omitempty"`
	ContextWindow      int      `json:"context_window,omitempty"`
	SupportedMIMETypes []string `json:"supported_mime_types,omitempty"`
	Meta               struct {
		Trae struct {
			ContextWindow    int  `json:"contextWindow,omitempty"`
			MaxContextWindow int  `json:"maxContextWindow,omitempty"`
			SupportsMaxMode  bool `json:"supportsMaxMode,omitempty"`
		} `json:"trae,omitempty"`
	} `json:"_meta,omitempty"`
}

type authFile struct {
	AuthMode    string `json:"auth_mode"`
	LastRefresh string `json:"last_refresh"`
	Trae        struct {
		AccessToken    string `json:"access_token"`
		UserID         string `json:"user_id"`
		ExpiresAt      string `json:"expires_at"`
		Region         string `json:"region"`
		LoginMethod    string `json:"login_method"`
		CredentialKind string `json:"credential_kind"`
	} `json:"trae"`
}

type cachedModelsFile struct {
	ClientVersion string `json:"client_version"`
	Models        []struct {
		Slug            string   `json:"slug"`
		ConfigName      string   `json:"config_name"`
		PromptModelID   string   `json:"prompt_model_id"`
		Description     string   `json:"description"`
		ContextWindow   int      `json:"context_window"`
		InputModalities []string `json:"input_modalities"`
	} `json:"models"`
}

// ResolveInstallation finds the installed Trae CLI and its local state files.
func ResolveInstallation() (Installation, error) {
	cliPath := strings.TrimSpace(os.Getenv("TRAECLI_PATH"))
	if cliPath == "" {
		var errLookPath error
		cliPath, errLookPath = exec.LookPath("traecli")
		if errLookPath != nil {
			cliPath, errLookPath = exec.LookPath("traex")
		}
		if errLookPath != nil {
			return Installation{}, fmt.Errorf("trae: local traecli executable not found: %w", errLookPath)
		}
	}
	cliPath, errAbs := filepath.Abs(cliPath)
	if errAbs != nil {
		return Installation{}, fmt.Errorf("trae: resolve CLI path: %w", errAbs)
	}

	home := firstNonEmptyEnv("TRAE_HOME", "TRAECLI_HOME")
	if home == "" {
		userHome, errHome := os.UserHomeDir()
		if errHome != nil {
			return Installation{}, fmt.Errorf("trae: resolve user home: %w", errHome)
		}
		home = filepath.Join(userHome, ".trae")
	}
	home, errAbs = filepath.Abs(home)
	if errAbs != nil {
		return Installation{}, fmt.Errorf("trae: resolve state home: %w", errAbs)
	}
	authPath := strings.TrimSpace(os.Getenv("TRAE_AUTH_PATH"))
	if authPath == "" {
		authPath = filepath.Join(home, "cli", "auth.json")
	}
	modelsPath := strings.TrimSpace(os.Getenv("TRAE_MODELS_PATH"))
	if modelsPath == "" {
		modelsPath = filepath.Join(home, "cli", "models_cache.json")
	}
	return Installation{CLIPath: cliPath, Home: home, AuthPath: authPath, ModelsPath: modelsPath}, nil
}

// LoadCredentials reads the current access token from Trae CLI state.
func LoadCredentials(path string) (Credentials, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Credentials{}, errors.New("trae: credential path is empty")
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return Credentials{}, fmt.Errorf("trae: read credentials: %w", errRead)
	}
	var stored authFile
	if errUnmarshal := json.Unmarshal(raw, &stored); errUnmarshal != nil {
		return Credentials{}, fmt.Errorf("trae: parse credentials: %w", errUnmarshal)
	}
	if !strings.EqualFold(strings.TrimSpace(stored.AuthMode), Provider) {
		return Credentials{}, fmt.Errorf("trae: local CLI is logged in with %q instead of Trae", stored.AuthMode)
	}
	if strings.TrimSpace(stored.Trae.AccessToken) == "" {
		return Credentials{}, errors.New("trae: local CLI credential has no access token")
	}
	return Credentials{
		AccessToken:    strings.TrimSpace(stored.Trae.AccessToken),
		UserID:         strings.TrimSpace(stored.Trae.UserID),
		ExpiresAt:      strings.TrimSpace(stored.Trae.ExpiresAt),
		Region:         strings.ToUpper(strings.TrimSpace(stored.Trae.Region)),
		LoginMethod:    strings.TrimSpace(stored.Trae.LoginMethod),
		CredentialKind: strings.TrimSpace(stored.Trae.CredentialKind),
		LastRefresh:    strings.TrimSpace(stored.LastRefresh),
	}, nil
}

// FetchModels asks the installed CLI for its live, account-filtered model catalog.
func FetchModels(ctx context.Context, cliPath string) ([]Model, string, error) {
	cliPath = strings.TrimSpace(cliPath)
	if cliPath == "" {
		return nil, "", errors.New("trae: CLI path is empty")
	}
	command := exec.CommandContext(ctx, cliPath, "models", "--json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if errRun := command.Run(); errRun != nil {
		return nil, "", fmt.Errorf("trae: list models: %w: %s", errRun, strings.TrimSpace(stderr.String()))
	}
	var models []Model
	if errUnmarshal := json.Unmarshal(stdout.Bytes(), &models); errUnmarshal != nil {
		return nil, "", fmt.Errorf("trae: parse model catalog: %w", errUnmarshal)
	}
	models = normalizeModels(models)
	if len(models) == 0 {
		return nil, "", errors.New("trae: local CLI returned an empty model catalog")
	}
	return models, cliVersion(ctx, cliPath), nil
}

// LoadCachedModels reads Trae CLI's cached model catalog as an offline fallback.
func LoadCachedModels(path string) ([]Model, string, error) {
	raw, errRead := os.ReadFile(strings.TrimSpace(path))
	if errRead != nil {
		return nil, "", fmt.Errorf("trae: read model cache: %w", errRead)
	}
	var cached cachedModelsFile
	if errUnmarshal := json.Unmarshal(raw, &cached); errUnmarshal != nil {
		return nil, "", fmt.Errorf("trae: parse model cache: %w", errUnmarshal)
	}
	models := make([]Model, 0, len(cached.Models))
	for _, item := range cached.Models {
		backend := firstNonEmpty(item.PromptModelID, item.ConfigName, item.Slug)
		if backend != "" && !strings.HasSuffix(strings.ToLower(backend), "__dev") {
			backend += "__dev"
		}
		model := Model{
			Name:          item.Slug,
			ConfigName:    firstNonEmpty(item.ConfigName, item.Slug),
			BackendModel:  backend,
			Provider:      Provider,
			Description:   item.Description,
			ContextWindow: item.ContextWindow,
		}
		for _, modality := range item.InputModalities {
			if strings.EqualFold(modality, "image") {
				model.SupportedMIMETypes = append(model.SupportedMIMETypes, "image/*")
			}
		}
		models = append(models, model)
	}
	models = normalizeModels(models)
	if len(models) == 0 {
		return nil, cached.ClientVersion, errors.New("trae: cached model catalog is empty")
	}
	return models, cached.ClientVersion, nil
}

// ModelsFromMetadata decodes the catalog stored in a CLIProxyAPI auth record.
func ModelsFromMetadata(metadata map[string]any) []Model {
	if metadata == nil {
		return nil
	}
	rawModels, ok := metadata["models"]
	if !ok {
		return nil
	}
	raw, errMarshal := json.Marshal(rawModels)
	if errMarshal != nil {
		return nil
	}
	var models []Model
	if json.Unmarshal(raw, &models) != nil {
		return nil
	}
	return normalizeModels(models)
}

// ModelFor resolves a public, config, or backend model identifier.
func ModelFor(metadata map[string]any, requested string) (Model, bool) {
	requested = strings.TrimSpace(requested)
	for _, model := range ModelsFromMetadata(metadata) {
		for _, candidate := range []string{model.Name, model.ConfigName, model.BackendModel} {
			if strings.EqualFold(requested, strings.TrimSpace(candidate)) {
				return model, true
			}
		}
	}
	return Model{}, false
}

// RegistryModels converts imported Trae models into CLIProxyAPI model metadata.
func RegistryModels(metadata map[string]any) []*registry.ModelInfo {
	models := ModelsFromMetadata(metadata)
	if len(models) == 0 {
		modelsPath := metadataString(metadata, "trae_models_path")
		cached, _, errCached := LoadCachedModels(modelsPath)
		if errCached == nil {
			models = cached
		}
	}
	out := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		inputModalities := []string{"TEXT"}
		for _, mimeType := range model.SupportedMIMETypes {
			if strings.HasPrefix(strings.ToLower(mimeType), "image/") || strings.EqualFold(mimeType, "image/*") {
				inputModalities = append(inputModalities, "IMAGE")
				break
			}
		}
		levels := []string{"low", "medium", "high", "xhigh"}
		if model.Meta.Trae.SupportsMaxMode {
			levels = append(levels, "max")
		}
		out = append(out, &registry.ModelInfo{
			ID:                         model.Name,
			Object:                     "model",
			OwnedBy:                    Provider,
			Type:                       "openai",
			DisplayName:                model.Name,
			Name:                       model.Name,
			Version:                    model.BackendModel,
			Description:                model.Description,
			InputTokenLimit:            model.ContextWindow,
			ContextLength:              model.ContextWindow,
			MaxContextLength:           model.ContextWindow,
			MaxCompletionTokens:        64_000,
			SupportedInputModalities:   inputModalities,
			SupportedOutputModalities:  []string{"TEXT"},
			SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
			Thinking:                   &registry.ThinkingSupport{Levels: levels},
		})
	}
	return out
}

// AccessToken reloads the token from Trae CLI state, falling back to an explicitly stored token.
func AccessToken(metadata map[string]any) (string, error) {
	path := metadataString(metadata, "trae_auth_path")
	if path != "" {
		credentials, errLoad := LoadCredentials(path)
		if errLoad == nil {
			return credentials.AccessToken, nil
		}
		if metadataString(metadata, "access_token") == "" {
			return "", errLoad
		}
	}
	if token := metadataString(metadata, "access_token"); token != "" {
		return token, nil
	}
	return "", errors.New("trae: no local access token is available")
}

// BaseURLs returns endpoint candidates in the same regional order as Trae CLI.
func BaseURLs(metadata map[string]any) []string {
	if override := strings.TrimSpace(os.Getenv("TRAE_API_BASE_URL")); override != "" {
		return []string{strings.TrimSuffix(override, "/")}
	}
	urls := metadataStrings(metadata, "base_urls")
	if baseURL := metadataString(metadata, "base_url"); baseURL != "" {
		urls = append([]string{baseURL}, urls...)
	}
	if len(urls) == 0 {
		if strings.EqualFold(metadataString(metadata, "region"), "CN") {
			urls = []string{DefaultCNBaseURL, DefaultSGBaseURL, DefaultGlobalBaseURL}
		} else {
			urls = []string{DefaultGlobalBaseURL, DefaultSGBaseURL, DefaultCNBaseURL}
		}
	}
	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))
	for _, rawURL := range urls {
		rawURL = strings.TrimSuffix(strings.TrimSpace(rawURL), "/")
		if rawURL == "" {
			continue
		}
		key := strings.ToLower(rawURL)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, rawURL)
	}
	return out
}

func normalizeModels(models []Model) []Model {
	out := make([]Model, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model.Name = strings.TrimSpace(model.Name)
		if model.Name == "" || (!strings.EqualFold(model.Provider, Provider) && strings.TrimSpace(model.Provider) != "") {
			continue
		}
		model.Provider = Provider
		model.ConfigName = firstNonEmpty(model.ConfigName, model.Name)
		model.BackendModel = firstNonEmpty(model.BackendModel, model.ConfigName, model.Name)
		if !strings.HasSuffix(strings.ToLower(model.BackendModel), "__dev") {
			model.BackendModel += "__dev"
		}
		if model.ContextWindow <= 0 {
			model.ContextWindow = model.Meta.Trae.ContextWindow
		}
		key := strings.ToLower(model.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func cliVersion(ctx context.Context, cliPath string) string {
	command := exec.CommandContext(ctx, cliPath, "--version")
	output, errRun := command.Output()
	if errRun != nil {
		return ""
	}
	fields := strings.Fields(string(output))
	for _, field := range fields {
		if field != "" && field[0] >= '0' && field[0] <= '9' {
			return strings.TrimSpace(strings.SplitN(field, "(", 2)[0])
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func metadataStrings(metadata map[string]any, key string) []string {
	if metadata == nil {
		return nil
	}
	switch values := metadata[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
