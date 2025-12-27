package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
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

// GlobalProxySetter is an interface for token storage to set the use_global_proxy value
type GlobalProxySetter interface {
	SetUseGlobalProxy(bool)
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

	// Always ask about using global proxy if auth doesn't have proxy URL configured
	// This allows the setting to work even if user adds proxy to config.yaml later
	if record.ProxyURL == "" {
		// Determine default value based on provider
		defaultValue := true
		if provider == "qwen" || provider == "iflow" {
			defaultValue = false
		}
		shouldUseGlobalProxy := AskUseGlobalProxy(opts, defaultValue)
		record.UseGlobalProxy = shouldUseGlobalProxy

		// Update the storage if it implements GlobalProxySetter
		if storage, ok := record.Storage.(GlobalProxySetter); ok {
			storage.SetUseGlobalProxy(shouldUseGlobalProxy)
		}
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

// AskUseGlobalProxy asks the user whether to use the global proxy from config.yaml.
func AskUseGlobalProxy(opts *LoginOptions, defaultValue bool) bool {
	if opts == nil || opts.Prompt == nil {
		// If no prompt function available, return the default value
		return defaultValue
	}

	fmt.Println()
	fmt.Println("Would you like to use the global proxy from config.yaml for this authentication?")
	fmt.Println("(This allows the proxy to be applied automatically if you add it to config.yaml later)")
	if defaultValue {
		fmt.Println("yes/no, default: yes")
	} else {
		fmt.Println("yes/no, default: no")
	}

	answer, err := opts.Prompt("Use global proxy? ")
	if err != nil {
		// If we can't get user input, return the default value
		return defaultValue
	}

	answer = strings.TrimSpace(strings.ToLower(answer))
	if defaultValue {
		// If default is true, only turn off if user says no
		return answer != "n" && answer != "no"
	} else {
		// If default is false, only turn on if user says yes
		return answer == "y" || answer == "yes"
	}
}
