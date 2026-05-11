package auth

import (
	"context"
	"fmt"
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Manager aggregates authenticators and coordinates persistence via a token store.
type Manager struct {
	authenticators map[string]Authenticator
	store          coreauth.Store
}

// NewManager constructs a manager with the provided token store and authenticators.
// If store is nil, the caller must set it later using SetStore.
func NewManager(store coreauth.Store, authenticators ...Authenticator) *Manager {
	mgr := &Manager{
		authenticators: make(map[string]Authenticator),
		store:          store,
	}
	for i := range authenticators {
		mgr.Register(authenticators[i])
	}
	return mgr
}

// Register adds or replaces an authenticator keyed by its provider identifier.
func (m *Manager) Register(a Authenticator) {
	if a == nil {
		return
	}
	if m.authenticators == nil {
		m.authenticators = make(map[string]Authenticator)
	}
	m.authenticators[a.Provider()] = a
}

// SetStore updates the token store used for persistence.
func (m *Manager) SetStore(store coreauth.Store) {
	m.store = store
}

// Login executes the provider login flow and persists the resulting auth record.
func (m *Manager) Login(ctx context.Context, provider string, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, string, error) {
	auth, ok := m.authenticators[provider]
	if !ok {
		return nil, "", fmt.Errorf("cliproxy auth: authenticator %s not registered", provider)
	}

	record, err := auth.Login(ctx, cfg, opts)
	if err != nil {
		return nil, "", err
	}
	if record == nil {
		return nil, "", fmt.Errorf("cliproxy auth: authenticator %s returned nil record", provider)
	}

	if m.store == nil {
		return record, "", nil
	}

	if cfg != nil {
		if dirSetter, ok := m.store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(cfg.AuthDir)
		}
	}

	savedPath, err := m.store.Save(ctx, record)
	if err != nil {
		return record, "", err
	}
	return record, savedPath, nil
}

type ProviderInfo struct {
	Key             string   `json:"key"`
	DisplayName     string   `json:"display_name"`
	FlowType        string   `json:"flow_type"`
	AuthURLEndpoint string   `json:"auth_url_endpoint"`
	Aliases         []string `json:"aliases,omitempty"`
	Configured      bool     `json:"configured"`
}

var providerMetadata = map[string]ProviderInfo{
	"claude": {
		Key:             "claude",
		DisplayName:     "Claude (Anthropic)",
		FlowType:        "authorization_code_pkce",
		AuthURLEndpoint: "/anthropic-auth-url",
		Aliases:         []string{"anthropic"},
	},
	"codex": {
		Key:             "codex",
		DisplayName:     "Codex (OpenAI)",
		FlowType:        "authorization_code_pkce",
		AuthURLEndpoint: "/codex-auth-url",
		Aliases:         []string{"openai"},
	},
	"gemini": {
		Key:             "gemini",
		DisplayName:     "Gemini CLI",
		FlowType:        "google_oauth2",
		AuthURLEndpoint: "/gemini-cli-auth-url",
		Aliases:         []string{"google"},
	},
	"antigravity": {
		Key:             "antigravity",
		DisplayName:     "Antigravity",
		FlowType:        "google_oauth2",
		AuthURLEndpoint: "/antigravity-auth-url",
		Aliases:         []string{"anti-gravity"},
	},
	"kimi": {
		Key:             "kimi",
		DisplayName:     "Kimi",
		FlowType:        "device_code",
		AuthURLEndpoint: "/kimi-auth-url",
	},
	"kiro": {
		Key:             "kiro",
		DisplayName:     "Kiro",
		FlowType:        "aws_builder_id",
		AuthURLEndpoint: "/kiro-auth-url",
	},
	"github-copilot": {
		Key:             "github-copilot",
		DisplayName:     "GitHub Copilot",
		FlowType:        "device_code",
		AuthURLEndpoint: "/github-auth-url",
		Aliases:         []string{"github"},
	},
	"gitlab": {
		Key:             "gitlab",
		DisplayName:     "GitLab",
		FlowType:        "authorization_code_pkce",
		AuthURLEndpoint: "/gitlab-auth-url",
	},
	"codebuddy": {
		Key:             "codebuddy",
		DisplayName:     "CodeBuddy",
		FlowType:        "token",
		AuthURLEndpoint: "",
	},
	"codebuddy-ai": {
		Key:             "codebuddy-ai",
		DisplayName:     "CodeBuddy AI",
		FlowType:        "token",
		AuthURLEndpoint: "",
	},
	"cursor": {
		Key:             "cursor",
		DisplayName:     "Cursor",
		FlowType:        "pkce_polling",
		AuthURLEndpoint: "/cursor-auth-url",
	},
	"qoder": {
		Key:             "qoder",
		DisplayName:     "Qoder",
		FlowType:        "pkce_custom_uri",
		AuthURLEndpoint: "/qoder-auth-url",
	},
	"codearts": {
		Key:             "codearts",
		DisplayName:     "CodeArts",
		FlowType:        "web_oauth",
		AuthURLEndpoint: "",
	},
	"joycode": {
		Key:             "joycode",
		DisplayName:     "JoyCode",
		FlowType:        "web_oauth",
		AuthURLEndpoint: "",
	},
	"kilo": {
		Key:             "kilo",
		DisplayName:     "Kilo",
		FlowType:        "device_code",
		AuthURLEndpoint: "/kilo-auth-url",
	},
}

func (m *Manager) ListProviders() []ProviderInfo {
	configuredKeys := make(map[string]bool)
	if m.authenticators != nil {
		for key := range m.authenticators {
			configuredKeys[key] = true
		}
	}

	seen := make(map[string]bool)
	result := make([]ProviderInfo, 0, len(providerMetadata)+len(configuredKeys))

	for key, info := range providerMetadata {
		info.Configured = configuredKeys[key]
		result = append(result, info)
		seen[key] = true
	}

	for key := range configuredKeys {
		if !seen[key] {
			result = append(result, ProviderInfo{
				Key:         key,
				DisplayName: key,
				FlowType:    "unknown",
				Configured:  true,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

// SaveAuth persists an auth record using the configured store.
func (m *Manager) SaveAuth(record *coreauth.Auth, cfg *config.Config) (string, error) {
	if m.store == nil {
		return "", fmt.Errorf("no store configured")
	}
	if cfg != nil {
		if dirSetter, ok := m.store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(cfg.AuthDir)
		}
	}
	return m.store.Save(context.Background(), record)
}
