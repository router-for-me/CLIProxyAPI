package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

var (
	ErrCredentialModelsUnsupported   = errors.New("credential model management is not supported")
	ErrCredentialModelsUnavailable   = errors.New("credential model catalog is unavailable")
	ErrCredentialModelsRefreshFailed = errors.New("credential model catalog refresh failed")
)

// CredentialModel uses the provider model editor's field names. Pointer fields
// distinguish inherited capabilities from explicit false or empty overrides.
type CredentialModel struct {
	Name                       string                    `json:"name"`
	Alias                      string                    `json:"alias,omitempty"`
	MaxContextLength           *int                      `json:"max-context-length,omitempty"`
	Thinking                   *registry.ThinkingSupport `json:"thinking,omitempty"`
	IsCompat                   *bool                     `json:"is-compat,omitempty"`
	SupportConfigurationUpdate *bool                     `json:"support-configuration-update,omitempty"`
	InputModalities            *[]string                 `json:"input-modalities,omitempty"`
	OutputModalities           *[]string                 `json:"output-modalities,omitempty"`
}

func (m CredentialModel) PublicName() string {
	if m.Alias != "" {
		return m.Alias
	}
	return m.Name
}

type credentialModelAliasEntry struct{ CredentialModel }

func (m credentialModelAliasEntry) GetName() string  { return m.Name }
func (m credentialModelAliasEntry) GetAlias() string { return m.PublicName() }

// CredentialModelCatalog keeps source discovery separate from saved model policy.
type CredentialModelCatalog struct {
	Models []*registry.ModelInfo       `json:"models"`
	Source registry.ModelCatalogStatus `json:"source"`
}

type CredentialModelCatalogHook func(context.Context, *Auth, bool) (*CredentialModelCatalog, error)

// DecodeCredentialModels validates both API writes and file-loaded configuration.
func DecodeCredentialModels(data []byte) ([]CredentialModel, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("models must be an array; use DELETE to restore defaults")
	}
	var models []CredentialModel
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&models); err != nil {
		return nil, fmt.Errorf("invalid credential models: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing model data")
	}
	seen := make(map[string]bool, len(models))
	for i := range models {
		m := &models[i]
		m.Name, m.Alias = strings.TrimSpace(m.Name), strings.TrimSpace(m.Alias)
		if m.Name == "" || strings.ContainsAny(m.Name+m.Alias, "#\r\n\t") {
			return nil, fmt.Errorf("model %d has an invalid name or alias", i)
		}
		key := strings.ToLower(m.PublicName())
		if seen[key] {
			return nil, fmt.Errorf("duplicate model %q", m.PublicName())
		}
		seen[key] = true
		if m.MaxContextLength != nil && *m.MaxContextLength <= 0 {
			return nil, fmt.Errorf("model %q: max-context-length must be positive", m.Name)
		}
		if m.Thinking != nil {
			if m.Thinking.Min < 0 || m.Thinking.Max < 0 || (m.Thinking.Max > 0 && m.Thinking.Min > m.Thinking.Max) {
				return nil, fmt.Errorf("model %q: invalid thinking budget", m.Name)
			}
			m.Thinking = modelconfig.NormalizeThinkingSupport(m.Thinking)
		}
		for _, modalities := range [](*[]string){m.InputModalities, m.OutputModalities} {
			if modalities == nil {
				continue
			}
			out := make([]string, 0, len(*modalities))
			used := make(map[string]bool)
			for _, value := range *modalities {
				value = strings.ToLower(strings.TrimSpace(value))
				switch value {
				case "text", "image", "audio", "video", "document":
				default:
					return nil, fmt.Errorf("model %q: unsupported modality %q", m.Name, value)
				}
				if !used[value] {
					out = append(out, value)
					used[value] = true
				}
			}
			*modalities = out
		}
	}
	if models == nil {
		models = []CredentialModel{}
	}
	return models, nil
}

func SupportsCredentialModels(a *Auth) bool {
	if a == nil || IsConfigAPIKeyAuth(a) || IsPluginVirtualAuth(a) {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	// OpenAI-compatible credentials are API-key only; the key is the credential
	// material, not a config-backed gate key.
	if provider == "openai-compatible" {
		return true
	}
	if a.AuthKind() == "apikey" {
		return false
	}
	switch provider {
	case "claude", "codex", "gemini", "gemini-interactions", "vertex", "aistudio", "kimi", "kimi-ai", "kimi.ai", "kimi.com", "kiro", "qoder", "qoder-cn", "xai", "devin", "meta", "zcode", "antigravity":
		return true
	default:
		return false
	}
}

// CredentialModels reports presence separately: an empty list publishes no models,
// while a missing list inherits the provider catalog. Malformed lists fail closed.
func (a *Auth) CredentialModels() ([]CredentialModel, bool) {
	if !SupportsCredentialModels(a) {
		return nil, false
	}
	raw, exists := a.Metadata["models"]
	if !exists || raw == nil {
		return nil, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, true
	}
	models, err := DecodeCredentialModels(data)
	if err != nil {
		return nil, true
	}
	return models, true
}
