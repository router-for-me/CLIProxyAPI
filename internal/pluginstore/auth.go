package pluginstore

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
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

type AuthConfig struct {
	Match          string   `yaml:"match,omitempty" json:"match,omitempty"`
	ApplyTo        []string `yaml:"apply-to,omitempty" json:"apply_to,omitempty"`
	Type           string   `yaml:"type,omitempty" json:"type,omitempty"`
	TokenEnv       string   `yaml:"token-env,omitempty" json:"token_env,omitempty"`
	UsernameEnv    string   `yaml:"username-env,omitempty" json:"username_env,omitempty"`
	PasswordEnv    string   `yaml:"password-env,omitempty" json:"password_env,omitempty"`
	HeaderName     string   `yaml:"header-name,omitempty" json:"header_name,omitempty"`
	HeaderValueEnv string   `yaml:"header-value-env,omitempty" json:"header_value_env,omitempty"`
	AllowInsecure  bool     `yaml:"allow-insecure,omitempty" json:"allow_insecure,omitempty"`
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
		item.UsernameEnv = strings.TrimSpace(item.UsernameEnv)
		item.PasswordEnv = strings.TrimSpace(item.PasswordEnv)
		item.HeaderName = strings.TrimSpace(item.HeaderName)
		item.HeaderValueEnv = strings.TrimSpace(item.HeaderValueEnv)
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

func AuthConfigured(auth []AuthConfig, requestURL string, kind string) bool {
	item, ok := matchingAuthConfig(auth, requestURL, kind)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(item.Type)) {
	case AuthTypeNone:
		return false
	case AuthTypeBearer, AuthTypeGitHubToken:
		envName := item.TokenEnv
		if envName == "" && item.Type == AuthTypeGitHubToken {
			envName = "GITSTORE_GIT_TOKEN"
		}
		return strings.TrimSpace(os.Getenv(envName)) != ""
	case AuthTypeBasic:
		return strings.TrimSpace(os.Getenv(item.UsernameEnv)) != "" && strings.TrimSpace(os.Getenv(item.PasswordEnv)) != ""
	case AuthTypeHeader:
		return item.HeaderName != "" && strings.TrimSpace(os.Getenv(item.HeaderValueEnv)) != ""
	default:
		return false
	}
}

func PluginAuthConfigured(source Source, plugin Plugin, auth []AuthConfig) bool {
	if AuthConfigured(auth, source.URL, RequestKindRegistry) {
		return true
	}
	switch PluginInstallType(plugin) {
	case InstallTypeDirect:
		for _, artifact := range PluginArtifacts(plugin) {
			if AuthConfigured(auth, artifact.URL, RequestKindArtifact) {
				return true
			}
		}
	case InstallTypeGitHubRelease:
		return pluginGitHubReleaseAuthConfigured(plugin, auth)
	}
	return false
}

func pluginGitHubReleaseAuthConfigured(plugin Plugin, auth []AuthConfig) bool {
	owner, repo, errRepository := GitHubRepositoryParts(plugin.Repository)
	if errRepository != nil {
		return false
	}
	releasesURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/releases/",
		url.PathEscape(owner),
		url.PathEscape(repo),
	)
	return AuthConfigured(auth, releasesURL+"latest", RequestKindMetadata) ||
		AuthConfigured(auth, releasesURL+"tags/", RequestKindMetadata)
}

func applyPluginStoreAuth(headers http.Header, auth []AuthConfig, requestURL string, kind string) error {
	item, ok := matchingAuthConfig(auth, requestURL, kind)
	if !ok {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(item.Type)) {
	case "", AuthTypeNone:
		return nil
	case AuthTypeBearer:
		token, errToken := envValueRequired(item.TokenEnv, "token-env")
		if errToken != nil {
			return errToken
		}
		headers.Set("Authorization", "Bearer "+token)
	case AuthTypeBasic:
		username, errUsername := envValueRequired(item.UsernameEnv, "username-env")
		if errUsername != nil {
			return errUsername
		}
		password, errPassword := envValueRequired(item.PasswordEnv, "password-env")
		if errPassword != nil {
			return errPassword
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		headers.Set("Authorization", "Basic "+encoded)
	case AuthTypeHeader:
		if strings.TrimSpace(item.HeaderName) == "" {
			return fmt.Errorf("plugin store auth missing header-name")
		}
		value, errValue := envValueRequired(item.HeaderValueEnv, "header-value-env")
		if errValue != nil {
			return errValue
		}
		headers.Set(item.HeaderName, value)
	case AuthTypeGitHubToken:
		token := strings.TrimSpace(os.Getenv(strings.TrimSpace(item.TokenEnv)))
		if token == "" {
			token = strings.TrimSpace(os.Getenv("GITSTORE_GIT_TOKEN"))
		}
		if token == "" {
			return fmt.Errorf("plugin store auth missing token-env")
		}
		headers.Set("Authorization", "Bearer "+token)
	default:
		return fmt.Errorf("unsupported plugin store auth type %q", item.Type)
	}
	return nil
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

func envValueRequired(envName string, field string) (string, error) {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return "", fmt.Errorf("plugin store auth missing %s", field)
	}
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return "", fmt.Errorf("plugin store auth env %s is empty", envName)
	}
	return value, nil
}
