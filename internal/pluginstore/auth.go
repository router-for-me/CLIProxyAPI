package pluginstore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	RequestKindRegistry = "registry"
	RequestKindMetadata = "metadata"
	RequestKindArtifact = "artifact"

	AuthTypeNone        = "none"
	AuthTypeBearer      = "bearer"
	AuthTypeBasic       = "basic"
	AuthTypeHeader      = "header"
	AuthTypeGitHubToken = "github-token"
)

const authCommandTimeout = 5 * time.Second

var errAuthCommandTimedOut = errors.New("auth command timed out")

type AuthConfig struct {
	Match              string   `yaml:"match,omitempty" json:"match,omitempty"`
	ApplyTo            []string `yaml:"apply-to,omitempty" json:"apply_to,omitempty"`
	Type               string   `yaml:"type,omitempty" json:"type,omitempty"`
	TokenEnv           string   `yaml:"token-env,omitempty" json:"token_env,omitempty"`
	TokenCommand       string   `yaml:"token-command,omitempty" json:"token_command,omitempty"`
	UsernameEnv        string   `yaml:"username-env,omitempty" json:"username_env,omitempty"`
	UsernameCommand    string   `yaml:"username-command,omitempty" json:"username_command,omitempty"`
	PasswordEnv        string   `yaml:"password-env,omitempty" json:"password_env,omitempty"`
	PasswordCommand    string   `yaml:"password-command,omitempty" json:"password_command,omitempty"`
	HeaderName         string   `yaml:"header-name,omitempty" json:"header_name,omitempty"`
	HeaderValueEnv     string   `yaml:"header-value-env,omitempty" json:"header_value_env,omitempty"`
	HeaderValueCommand string   `yaml:"header-value-command,omitempty" json:"header_value_command,omitempty"`
	AllowInsecure      bool     `yaml:"allow-insecure,omitempty" json:"allow_insecure,omitempty"`
}

type AuthResolver struct {
	mu             sync.Mutex
	cache          map[string]*authResolution
	commandTimeout time.Duration
	onWait         func()
}

type authResolution struct {
	done  chan struct{}
	value string
	err   error
}

type authCommandRunnerFunc func(context.Context, string, ...string) ([]byte, error)

var authCommandRunner = runAuthCommand
var authCommandRunnerMu sync.Mutex

func NewAuthResolver() *AuthResolver {
	return newAuthResolver(authCommandTimeout)
}

func newAuthResolver(commandTimeout time.Duration) *AuthResolver {
	return &AuthResolver{cache: make(map[string]*authResolution), commandTimeout: commandTimeout}
}

func setAuthCommandRunnerForTest(runner authCommandRunnerFunc) func() {
	authCommandRunnerMu.Lock()
	previous := authCommandRunner
	authCommandRunner = runner
	authCommandRunnerMu.Unlock()
	return func() {
		authCommandRunnerMu.Lock()
		authCommandRunner = previous
		authCommandRunnerMu.Unlock()
	}
}

func NormalizeAuthConfigs(auth []AuthConfig) []AuthConfig {
	if len(auth) == 0 {
		return nil
	}
	out := make([]AuthConfig, 0, len(auth))
	for _, item := range auth {
		item.Match = strings.TrimSpace(item.Match)
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		item.TokenEnv = strings.TrimSpace(item.TokenEnv)
		item.TokenCommand = strings.TrimSpace(item.TokenCommand)
		item.UsernameEnv = strings.TrimSpace(item.UsernameEnv)
		item.UsernameCommand = strings.TrimSpace(item.UsernameCommand)
		item.PasswordEnv = strings.TrimSpace(item.PasswordEnv)
		item.PasswordCommand = strings.TrimSpace(item.PasswordCommand)
		item.HeaderName = strings.TrimSpace(item.HeaderName)
		item.HeaderValueEnv = strings.TrimSpace(item.HeaderValueEnv)
		item.HeaderValueCommand = strings.TrimSpace(item.HeaderValueCommand)
		if item.Type == "" {
			item.Type = AuthTypeNone
		}
		if item.Match == "" {
			continue
		}
		if len(item.ApplyTo) > 0 {
			applyTo := make([]string, 0, len(item.ApplyTo))
			seen := map[string]struct{}{}
			for _, value := range item.ApplyTo {
				value = strings.ToLower(strings.TrimSpace(value))
				if value == "" {
					continue
				}
				if _, exists := seen[value]; exists {
					continue
				}
				seen[value] = struct{}{}
				applyTo = append(applyTo, value)
			}
			item.ApplyTo = applyTo
		}
		out = append(out, item)
	}
	return out
}

func ValidateAuthConfigs(auth []AuthConfig) error {
	for _, item := range auth {
		for _, source := range []struct {
			env     string
			command string
			name    string
		}{
			{item.TokenEnv, item.TokenCommand, "token"},
			{item.UsernameEnv, item.UsernameCommand, "username"},
			{item.PasswordEnv, item.PasswordCommand, "password"},
			{item.HeaderValueEnv, item.HeaderValueCommand, "header value"},
		} {
			if strings.TrimSpace(source.env) != "" && strings.TrimSpace(source.command) != "" {
				return fmt.Errorf("plugin store auth %s source is ambiguous", source.name)
			}
		}
	}
	return nil
}

func AuthConfigured(auth []AuthConfig, requestURL string, kind string) bool {
	return AuthConfiguredContext(context.Background(), NewAuthResolver(), auth, requestURL, kind)
}

func AuthConfiguredContext(ctx context.Context, resolver *AuthResolver, auth []AuthConfig, requestURL string, kind string) bool {
	headers := http.Header{}
	return applyPluginStoreAuthContext(ctx, resolver, headers, auth, requestURL, kind) == nil && len(headers) > 0
}

func PluginAuthConfigured(source Source, plugin Plugin, auth []AuthConfig) bool {
	return PluginAuthConfiguredContext(context.Background(), NewAuthResolver(), source, plugin, auth)
}

func PluginAuthConfiguredContext(ctx context.Context, resolver *AuthResolver, source Source, plugin Plugin, auth []AuthConfig) bool {
	if AuthConfiguredContext(ctx, resolver, auth, source.URL, RequestKindRegistry) {
		return true
	}
	switch PluginInstallType(plugin) {
	case InstallTypeDirect:
		for _, artifact := range PluginArtifacts(plugin) {
			if AuthConfiguredContext(ctx, resolver, auth, artifact.URL, RequestKindArtifact) {
				return true
			}
		}
	case InstallTypeGitHubRelease:
		return pluginGitHubReleaseAuthConfiguredContext(ctx, resolver, plugin, auth)
	}
	return false
}

func pluginGitHubReleaseAuthConfigured(plugin Plugin, auth []AuthConfig) bool {
	return pluginGitHubReleaseAuthConfiguredContext(context.Background(), NewAuthResolver(), plugin, auth)
}

func pluginGitHubReleaseAuthConfiguredContext(ctx context.Context, resolver *AuthResolver, plugin Plugin, auth []AuthConfig) bool {
	owner, repo, errRepository := GitHubRepositoryParts(plugin.Repository)
	if errRepository != nil {
		return false
	}
	releasesURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/", url.PathEscape(owner), url.PathEscape(repo))
	return AuthConfiguredContext(ctx, resolver, auth, releasesURL+"latest", RequestKindMetadata) ||
		AuthConfiguredContext(ctx, resolver, auth, releasesURL+"tags/", RequestKindMetadata)
}

func applyPluginStoreAuth(headers http.Header, auth []AuthConfig, requestURL string, kind string) error {
	return applyPluginStoreAuthContext(context.Background(), NewAuthResolver(), headers, auth, requestURL, kind)
}

func applyPluginStoreAuthContext(ctx context.Context, resolver *AuthResolver, headers http.Header, auth []AuthConfig, requestURL string, kind string) error {
	item, ok := matchingAuthConfig(auth, requestURL, kind)
	if !ok {
		return nil
	}
	if resolver == nil {
		resolver = NewAuthResolver()
	}
	switch strings.ToLower(strings.TrimSpace(item.Type)) {
	case "", AuthTypeNone:
		return nil
	case AuthTypeBearer, AuthTypeGitHubToken:
		token, errToken := resolver.value(ctx, item.TokenEnv, item.TokenCommand, "token-env", "token-command")
		if errToken != nil {
			return errToken
		}
		headers.Set("Authorization", "Bearer "+token)
	case AuthTypeBasic:
		username, errUsername := resolver.value(ctx, item.UsernameEnv, item.UsernameCommand, "username-env", "username-command")
		if errUsername != nil {
			return errUsername
		}
		password, errPassword := resolver.value(ctx, item.PasswordEnv, item.PasswordCommand, "password-env", "password-command")
		if errPassword != nil {
			return errPassword
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		headers.Set("Authorization", "Basic "+encoded)
	case AuthTypeHeader:
		if strings.TrimSpace(item.HeaderName) == "" {
			return fmt.Errorf("plugin store auth missing header-name")
		}
		value, errValue := resolver.value(ctx, item.HeaderValueEnv, item.HeaderValueCommand, "header-value-env", "header-value-command")
		if errValue != nil {
			return errValue
		}
		headers.Set(item.HeaderName, value)
	default:
		return fmt.Errorf("unsupported plugin store auth type %q", item.Type)
	}
	return nil
}

func (r *AuthResolver) value(ctx context.Context, envName, command, envField, commandField string) (string, error) {
	envName = strings.TrimSpace(envName)
	command = strings.TrimSpace(command)
	if envName != "" && command != "" {
		return "", fmt.Errorf("plugin store auth %s source is ambiguous", strings.TrimSuffix(envField, "-env"))
	}
	if envName != "" {
		return r.resolve(ctx, "env", envName, func(context.Context) (string, error) {
			value := strings.TrimSpace(os.Getenv(envName))
			if value == "" {
				return "", fmt.Errorf("plugin store auth env %s is empty", envName)
			}
			return value, nil
		})
	}
	if command != "" {
		return r.resolve(ctx, "command", command, func(ctx context.Context) (string, error) {
			output, errCommand := executeAuthCommand(ctx, command, r.commandTimeout)
			if errCommand != nil {
				switch {
				case errors.Is(errCommand, errAuthCommandTimedOut):
					return "", fmt.Errorf("plugin store auth %s timed out", commandField)
				case errors.Is(errCommand, context.Canceled), errors.Is(errCommand, context.DeadlineExceeded):
					return "", errCommand
				default:
					return "", fmt.Errorf("plugin store auth %s failed", commandField)
				}
			}
			value := strings.TrimSpace(string(output))
			if value == "" {
				return "", fmt.Errorf("plugin store auth %s returned empty output", commandField)
			}
			return value, nil
		})
	}
	return "", fmt.Errorf("plugin store auth missing %s", envField)
}

func (r *AuthResolver) resolve(ctx context.Context, kind, source string, resolve func(context.Context) (string, error)) (string, error) {
	key := kind + "\x00" + source
	for {
		r.mu.Lock()
		entry, ok := r.cache[key]
		if !ok {
			entry = &authResolution{done: make(chan struct{})}
			r.cache[key] = entry
		}
		r.mu.Unlock()

		if !ok {
			entry.value, entry.err = resolve(ctx)
			if errors.Is(entry.err, context.Canceled) || errors.Is(entry.err, context.DeadlineExceeded) {
				r.mu.Lock()
				if r.cache[key] == entry {
					delete(r.cache, key)
				}
				r.mu.Unlock()
			}
			close(entry.done)
			return entry.value, entry.err
		}

		if r.onWait != nil {
			r.onWait()
		}
		select {
		case <-entry.done:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if errors.Is(entry.err, context.Canceled) || errors.Is(entry.err, context.DeadlineExceeded) {
				continue
			}
			return entry.value, entry.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func executeAuthCommand(ctx context.Context, expression string, timeout time.Duration) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	authCommandRunnerMu.Lock()
	runner := authCommandRunner
	authCommandRunnerMu.Unlock()
	name, args := authCommandShell(expression)
	output, errCommand := runner(commandCtx, name, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if commandCtx.Err() == context.DeadlineExceeded {
		return nil, errAuthCommandTimedOut
	}
	return output, errCommand
}

func authCommandShell(expression string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/C", expression}
	}
	return "/bin/sh", []string{"-c", expression}
}

func runAuthCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func validatePluginStoreRequestURL(auth []AuthConfig, requestURL string, kind string) error {
	parsed, errParse := url.Parse(strings.TrimSpace(requestURL))
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid plugin store url")
	}
	if hasSensitiveQueryParameter(parsed) {
		return fmt.Errorf("plugin store url contains sensitive query parameter")
	}
	if strings.EqualFold(parsed.Scheme, "http") && !allowInsecurePluginStoreURL(auth, requestURL, kind) {
		return fmt.Errorf("insecure plugin store url requires matching allow-insecure auth rule")
	}
	return nil
}

func allowInsecurePluginStoreURL(auth []AuthConfig, requestURL string, kind string) bool {
	item, ok := matchingAuthConfig(auth, requestURL, kind)
	return ok && item.AllowInsecure
}

func matchingAuthConfig(auth []AuthConfig, requestURL string, kind string) (AuthConfig, bool) {
	requestURL = strings.TrimSpace(requestURL)
	kind = strings.ToLower(strings.TrimSpace(kind))
	for _, item := range NormalizeAuthConfigs(auth) {
		if !pluginStoreURLMatchesAuthRule(requestURL, item.Match) {
			continue
		}
		if !authAppliesTo(item, kind) {
			continue
		}
		return item, true
	}
	return AuthConfig{}, false
}

func pluginStoreURLMatchesAuthRule(requestURL string, matchURL string) bool {
	request, errRequest := url.Parse(strings.TrimSpace(requestURL))
	if errRequest != nil || request.Scheme == "" || request.Host == "" {
		return false
	}
	rule, errRule := url.Parse(strings.TrimSpace(matchURL))
	if errRule != nil || rule.Scheme == "" || rule.Host == "" {
		return false
	}
	if !strings.EqualFold(request.Scheme, rule.Scheme) || !strings.EqualFold(request.Host, rule.Host) {
		return false
	}
	return pluginStorePathMatchesAuthRule(request.Path, rule.Path)
}

func pluginStorePathMatchesAuthRule(requestPath string, rulePath string) bool {
	if rulePath == "" || rulePath == "/" {
		return true
	}
	if requestPath == "" {
		requestPath = "/"
	}
	if requestPath == rulePath {
		return true
	}
	if strings.HasSuffix(rulePath, "/") {
		return strings.HasPrefix(requestPath, rulePath)
	}
	return strings.HasPrefix(requestPath, rulePath+"/")
}

func authAppliesTo(item AuthConfig, kind string) bool {
	if len(item.ApplyTo) == 0 {
		return true
	}
	for _, value := range item.ApplyTo {
		if strings.EqualFold(strings.TrimSpace(value), kind) {
			return true
		}
	}
	return false
}
