package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	DefaultCallbackPath = "/auth/callback"
	DefaultScope        = "openid profile email"
	DefaultCallbackPort = 38965
)

// OIDCConfig holds default values for one configurable OIDC login flow.
// Command-line flags may override any populated field at runtime.
type OIDCConfig struct {
	Name          string `yaml:"name" json:"name"`
	Domain        string `yaml:"domain" json:"domain"`
	AuthorizePath string `yaml:"authorize-path" json:"authorize-path"`
	TokenPath     string `yaml:"token-path" json:"token-path"`
	ClientID      string `yaml:"client-id" json:"client-id"`
	Scope         string `yaml:"scope" json:"scope"`
	CallbackPath  string `yaml:"callback-path" json:"callback-path"`
	RedirectURI   string `yaml:"redirect-uri" json:"redirect-uri"`
	LLMEndpoint   string `yaml:"llm-endpoint" json:"llm-endpoint"`
	// 参考 https://github.com/router-for-me/CLIProxyAPI/blob/main/docs/sdk-advanced_CN.md
	RequestFormat  string                     `yaml:"request-format" json:"request-format"`
	ResponseFormat string                     `yaml:"response-format" json:"response-format"`
	Headers        map[string]string          `yaml:"headers,omitempty" json:"headers,omitempty"`
	Models         []OpenAICompatibilityModel `yaml:"models,omitempty" json:"models,omitempty"`
}

// OIDCConfigs supports either a single mapping or a list of mappings under `oidc:`.
type OIDCConfigs []OIDCConfig

func (c OIDCConfig) ResolveRedirectURI(port int) (string, error) {
	if strings.TrimSpace(c.RedirectURI) != "" {
		return strings.TrimSpace(c.RedirectURI), nil
	}
	if port <= 0 {
		port = DefaultCallbackPort
	}
	return fmt.Sprintf("http://localhost:%d%s", port, normalizeURLPath(c.CallbackPath)), nil
}

func (c OIDCConfig) CallbackBinding(defaultPort int) (int, string, bool, error) {
	redirectURI, err := c.ResolveRedirectURI(defaultPort)
	if err != nil {
		return 0, "", false, err
	}
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return 0, "", false, fmt.Errorf("invalid redirect uri: %w", err)
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" {
		return 0, normalizeURLPath(parsed.Path), false, nil
	}
	port := parsed.Port()
	if port == "" {
		switch parsed.Scheme {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil {
		return 0, "", false, fmt.Errorf("invalid redirect uri port: %w", err)
	}
	return parsedPort, normalizeURLPath(parsed.Path), true, nil
}

func normalizeURLPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return trimmed
}
