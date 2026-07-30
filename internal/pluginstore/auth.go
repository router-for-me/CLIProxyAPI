package pluginstore

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
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

// Secret holds short-lived credential material that can be overwritten after use.
type Secret []byte

// Clear overwrites the secret and releases its backing slice reference.
func (s *Secret) Clear() {
	if s == nil {
		return
	}
	for index := range *s {
		(*s)[index] = 0
	}
	*s = nil
}

type ResolvedAuthConfig struct {
	Match       string   `yaml:"match,omitempty" json:"match,omitempty"`
	ApplyTo     []string `yaml:"apply-to,omitempty" json:"apply_to,omitempty"`
	Type        string   `yaml:"type,omitempty" json:"type,omitempty"`
	Token       Secret   `yaml:"token,omitempty" json:"token,omitempty"`
	Username    Secret   `yaml:"username,omitempty" json:"username,omitempty"`
	Password    Secret   `yaml:"password,omitempty" json:"password,omitempty"`
	HeaderName  string   `yaml:"header-name,omitempty" json:"header_name,omitempty"`
	HeaderValue Secret   `yaml:"header-value,omitempty" json:"header_value,omitempty"`
}

func (c *ResolvedAuthConfig) Clear() {
	if c == nil {
		return
	}
	c.Token.Clear()
	c.Username.Clear()
	c.Password.Clear()
	c.HeaderValue.Clear()
	c.ApplyTo = nil
}

func ClearResolvedAuthConfigs(auth []ResolvedAuthConfig) {
	for index := range auth {
		auth[index].Clear()
	}
}

func ResolvedAuthForRequest(auth []ResolvedAuthConfig, requestURL string, kind string) (ResolvedAuthConfig, bool) {
	item, ok := matchingResolvedAuthConfig(auth, requestURL, kind)
	if !ok {
		return ResolvedAuthConfig{}, false
	}
	return cloneResolvedAuthConfig(item), true
}

func ValidateResolvedAuthConfig(item ResolvedAuthConfig) error {
	parsed, errParse := url.Parse(strings.TrimSpace(item.Match))
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("plugin store resolved auth match is invalid")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("plugin store resolved auth match must use https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("plugin store resolved auth match must not contain credentials, query, or fragment")
	}
	for _, kind := range item.ApplyTo {
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case RequestKindRegistry, RequestKindMetadata, RequestKindArtifact:
		default:
			return fmt.Errorf("plugin store resolved auth has unsupported apply_to %q", kind)
		}
	}
	switch strings.ToLower(strings.TrimSpace(item.Type)) {
	case "", AuthTypeNone:
		return nil
	case AuthTypeBearer, AuthTypeGitHubToken:
		if len(item.Token) == 0 {
			return fmt.Errorf("plugin store resolved auth token is empty")
		}
	case AuthTypeBasic:
		if len(item.Username) == 0 || len(item.Password) == 0 {
			return fmt.Errorf("plugin store resolved basic auth is incomplete")
		}
	case AuthTypeHeader:
		if strings.TrimSpace(item.HeaderName) == "" || strings.ContainsAny(item.HeaderName, "\r\n:") {
			return fmt.Errorf("plugin store resolved auth header name is invalid")
		}
		if len(item.HeaderValue) == 0 || secretContainsCRLF(item.HeaderValue) {
			return fmt.Errorf("plugin store resolved auth header value is invalid")
		}
	default:
		return fmt.Errorf("unsupported plugin store resolved auth type %q", item.Type)
	}
	return nil
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
		return strings.TrimSpace(os.Getenv(item.TokenEnv)) != ""
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
	_, errApply := applyPluginStoreAuthForClient(headers, nil, auth, requestURL, kind)
	return errApply
}

func applyPluginStoreAuthForClient(headers http.Header, resolved []ResolvedAuthConfig, auth []AuthConfig, requestURL string, kind string) (bool, error) {
	if item, ok := matchingResolvedAuthConfig(resolved, requestURL, kind); ok {
		applied, errApply := applyResolvedPluginStoreAuth(headers, item)
		return applied, errApply
	}
	item, ok := matchingAuthConfig(auth, requestURL, kind)
	if !ok {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(item.Type)) {
	case "", AuthTypeNone:
		return false, nil
	case AuthTypeBearer:
		token, errToken := envValueRequired(item.TokenEnv, "token-env")
		if errToken != nil {
			return false, errToken
		}
		headers.Set("Authorization", "Bearer "+token)
	case AuthTypeBasic:
		username, errUsername := envValueRequired(item.UsernameEnv, "username-env")
		if errUsername != nil {
			return false, errUsername
		}
		password, errPassword := envValueRequired(item.PasswordEnv, "password-env")
		if errPassword != nil {
			return false, errPassword
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		headers.Set("Authorization", "Basic "+encoded)
	case AuthTypeHeader:
		if strings.TrimSpace(item.HeaderName) == "" {
			return false, fmt.Errorf("plugin store auth missing header-name")
		}
		value, errValue := envValueRequired(item.HeaderValueEnv, "header-value-env")
		if errValue != nil {
			return false, errValue
		}
		headers.Set(item.HeaderName, value)
	case AuthTypeGitHubToken:
		token, errToken := envValueRequired(item.TokenEnv, "token-env")
		if errToken != nil {
			return false, errToken
		}
		headers.Set("Authorization", "Bearer "+token)
	default:
		return false, fmt.Errorf("unsupported plugin store auth type %q", item.Type)
	}
	return true, nil
}

func applyResolvedPluginStoreAuth(headers http.Header, item ResolvedAuthConfig) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(item.Type)) {
	case "", AuthTypeNone:
		return false, nil
	case AuthTypeBearer, AuthTypeGitHubToken:
		if len(item.Token) == 0 {
			return false, fmt.Errorf("plugin store resolved auth token is empty")
		}
		headers.Set("Authorization", "Bearer "+string(item.Token))
	case AuthTypeBasic:
		if len(item.Username) == 0 || len(item.Password) == 0 {
			return false, fmt.Errorf("plugin store resolved basic auth is incomplete")
		}
		credential := make([]byte, 0, len(item.Username)+1+len(item.Password))
		credential = append(credential, item.Username...)
		credential = append(credential, ':')
		credential = append(credential, item.Password...)
		encoded := base64.StdEncoding.EncodeToString(credential)
		for index := range credential {
			credential[index] = 0
		}
		headers.Set("Authorization", "Basic "+encoded)
	case AuthTypeHeader:
		if strings.TrimSpace(item.HeaderName) == "" {
			return false, fmt.Errorf("plugin store resolved auth missing header-name")
		}
		if len(item.HeaderValue) == 0 {
			return false, fmt.Errorf("plugin store resolved auth header value is empty")
		}
		headers.Set(item.HeaderName, string(item.HeaderValue))
	default:
		return false, fmt.Errorf("unsupported plugin store resolved auth type %q", item.Type)
	}
	return true, nil
}

func validatePluginStoreRequestURL(auth []AuthConfig, requestURL string, kind string) error {
	parsed, errParse := url.Parse(strings.TrimSpace(requestURL))
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid plugin store url")
	}
	if parsed.User != nil {
		return fmt.Errorf("plugin store url must not contain credentials")
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

func validateResolvedAuthExpiry(auth []ResolvedAuthConfig, expiresAt time.Time, now time.Time, requestURL string, kind string) error {
	if expiresAt.IsZero() {
		return nil
	}
	if _, ok := matchingResolvedAuthConfig(auth, requestURL, kind); !ok {
		return nil
	}
	if !now.Before(expiresAt) {
		return fmt.Errorf("plugin store resolved auth expired")
	}
	return nil
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

func matchingResolvedAuthConfig(auth []ResolvedAuthConfig, requestURL string, kind string) (ResolvedAuthConfig, bool) {
	requestURL = strings.TrimSpace(requestURL)
	kind = strings.ToLower(strings.TrimSpace(kind))
	for _, item := range auth {
		if !pluginStoreURLMatchesAuthRule(requestURL, strings.TrimSpace(item.Match)) {
			continue
		}
		if !resolvedAuthAppliesTo(item, kind) {
			continue
		}
		return item, true
	}
	return ResolvedAuthConfig{}, false
}

func resolvedAuthAppliesTo(item ResolvedAuthConfig, kind string) bool {
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

func cloneResolvedAuthConfig(item ResolvedAuthConfig) ResolvedAuthConfig {
	item.ApplyTo = append([]string(nil), item.ApplyTo...)
	item.Token = append(Secret(nil), item.Token...)
	item.Username = append(Secret(nil), item.Username...)
	item.Password = append(Secret(nil), item.Password...)
	item.HeaderValue = append(Secret(nil), item.HeaderValue...)
	return item
}

func resolvedAuthConfigured(item ResolvedAuthConfig) bool {
	switch strings.ToLower(strings.TrimSpace(item.Type)) {
	case AuthTypeBearer, AuthTypeGitHubToken:
		return len(item.Token) > 0
	case AuthTypeBasic:
		return len(item.Username) > 0 && len(item.Password) > 0
	case AuthTypeHeader:
		return strings.TrimSpace(item.HeaderName) != "" && len(item.HeaderValue) > 0
	default:
		return false
	}
}

func secretContainsCRLF(secret Secret) bool {
	for _, value := range secret {
		if value == '\r' || value == '\n' {
			return true
		}
	}
	return false
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
