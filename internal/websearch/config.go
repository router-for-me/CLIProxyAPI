package websearch

import (
	"net/http"
	"os"
	"strings"
)

// Built-in provider IDs in default chain order.
const (
	ProviderCodex      = "codex"
	ProviderXAI        = "xai"
	ProviderOpenRouter = "openrouter"
	ProviderGemini     = "gemini"
	ProviderAnthropic  = "anthropic"
	ProviderPerplexity = "perplexity"
	ProviderZAI        = "zai"
	ProviderExa        = "exa"
	ProviderTinyFish   = "tinyfish"
	ProviderJina       = "jina"
	ProviderKagi       = "kagi"
	ProviderTavily     = "tavily"
	ProviderFirecrawl  = "firecrawl"
	ProviderBrave      = "brave"
	ProviderKimi       = "kimi"
	ProviderParallel   = "parallel"
	ProviderSynthetic  = "synthetic"
	ProviderOllama     = "ollama"
	ProviderSearXNG    = "searxng"
	ProviderStartpage  = "startpage"
	ProviderDuckDuckGo = "duckduckgo"
	ProviderEcosia     = "ecosia"
	ProviderGoogle     = "google"
	ProviderMojeek     = "mojeek"
	// ProviderPublic fans out over the credential-free engines. It is
	// explicit-only: the automatic chain never selects it.
	ProviderPublic = "public"
)

// DefaultProviderOrder is the automatic chain order. LLM-grounded
// providers lead on quality but need credentials; credential-free scrapers
// close the chain so some provider is usually available. public is
// explicit-only and therefore absent here.
var DefaultProviderOrder = []string{
	ProviderPerplexity,
	ProviderGemini,
	ProviderAnthropic,
	ProviderCodex,
	ProviderXAI,
	ProviderOpenRouter,
	ProviderZAI,
	ProviderExa,
	ProviderTinyFish,
	ProviderJina,
	ProviderKagi,
	ProviderTavily,
	ProviderFirecrawl,
	ProviderBrave,
	ProviderKimi,
	ProviderParallel,
	ProviderSynthetic,
	ProviderOllama,
	ProviderSearXNG,
	ProviderStartpage,
	ProviderDuckDuckGo,
	ProviderEcosia,
	ProviderGoogle,
	ProviderMojeek,
}

// Timeout bounds for a single provider transport. This is a per-provider
// ceiling, not a whole-chain deadline.
const (
	DefaultTimeoutSeconds = 30
	MaxTimeoutSeconds     = 120
	DefaultLimit          = 10

	// Public aggregate deadline behavior mirrors OMP: return the earliest
	// of all engines settling, the soft deadline with at least one
	// success, or the hard cap.
	DefaultPublicFanoutSoftSeconds = 5
	DefaultPublicFanoutHardSeconds = 30
	DefaultPublicLimit             = 15
	MaxPublicLimit                 = 30

	// Perplexity API-key defaults.
	PerplexityDefaultMaxTokens        = 8192
	PerplexityDefaultTemperature      = 0.2
	PerplexityDefaultNumSearchResults = 20
)

// Config resolves credentials and chain behavior. Explicit values win;
// empty values fall back to environment variables.
type Config struct {
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	Order          []string `yaml:"order,omitempty" json:"order,omitempty"`
	Exclude        []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	TimeoutSeconds int      `yaml:"timeout-seconds,omitempty" json:"timeout-seconds,omitempty"`
	Limit          int      `yaml:"limit,omitempty" json:"limit,omitempty"`
	MaxSearches    int      `yaml:"max-searches,omitempty" json:"max-searches,omitempty"`

	BraveAPIKey       string `yaml:"brave-api-key,omitempty" json:"-"`
	TavilyAPIKey      string `yaml:"tavily-api-key,omitempty" json:"-"`
	ExaAPIKey         string `yaml:"exa-api-key,omitempty" json:"-"`
	XAIAPIKey         string `yaml:"xai-api-key,omitempty" json:"-"`
	OpenRouterAPIKey  string `yaml:"openrouter-api-key,omitempty" json:"-"`
	CodexAPIKey       string `yaml:"codex-api-key,omitempty" json:"-"`
	AnthropicAPIKey   string `yaml:"anthropic-api-key,omitempty" json:"-"`
	PerplexityAPIKey  string `yaml:"perplexity-api-key,omitempty" json:"-"`
	PerplexityOAuth   string `yaml:"perplexity-oauth-token,omitempty" json:"-"`
	PerplexityCookies string `yaml:"perplexity-cookies,omitempty" json:"-"`
	GeminiAPIKey      string `yaml:"gemini-api-key,omitempty" json:"-"`
	ZAIAPIKey         string `yaml:"zai-api-key,omitempty" json:"-"`
	TinyFishAPIKey    string `yaml:"tinyfish-api-key,omitempty" json:"-"`
	JinaAPIKey        string `yaml:"jina-api-key,omitempty" json:"-"`
	KagiAPIKey        string `yaml:"kagi-api-key,omitempty" json:"-"`
	FirecrawlAPIKey   string `yaml:"firecrawl-api-key,omitempty" json:"-"`
	KimiAPIKey        string `yaml:"kimi-api-key,omitempty" json:"-"`
	ParallelAPIKey    string `yaml:"parallel-api-key,omitempty" json:"-"`
	SyntheticAPIKey   string `yaml:"synthetic-api-key,omitempty" json:"-"`
	OllamaAPIKey      string `yaml:"ollama-api-key,omitempty" json:"-"`

	GeminiModel        string `yaml:"gemini-search-model,omitempty" json:"gemini-search-model,omitempty"`
	AnthropicModel     string `yaml:"anthropic-search-model,omitempty" json:"anthropic-search-model,omitempty"`
	XAIModel           string `yaml:"xai-search-model,omitempty" json:"xai-search-model,omitempty"`
	CodexModel         string `yaml:"codex-search-model,omitempty" json:"codex-search-model,omitempty"`
	GeminiEndpoint     string `yaml:"gemini-base-url,omitempty" json:"gemini-base-url,omitempty"`
	AnthropicEndpoint  string `yaml:"anthropic-base-url,omitempty" json:"anthropic-base-url,omitempty"`
	XAIEndpoint        string `yaml:"xai-base-url,omitempty" json:"xai-base-url,omitempty"`
	CodexEndpoint      string `yaml:"codex-base-url,omitempty" json:"codex-base-url,omitempty"`
	OpenRouterEndpoint string `yaml:"openrouter-base-url,omitempty" json:"openrouter-base-url,omitempty"`
	OpenRouterModelID  string `yaml:"openrouter-search-model,omitempty" json:"openrouter-search-model,omitempty"`
	FirecrawlEndpoint  string `yaml:"firecrawl-base-url,omitempty" json:"firecrawl-base-url,omitempty"`

	SearXNGEndpoint string `yaml:"searxng-endpoint,omitempty" json:"searxng-endpoint,omitempty"`
	SearXNGToken    string `yaml:"searxng-token,omitempty" json:"-"`
	SearXNGUsername string `yaml:"searxng-username,omitempty" json:"searxng-username,omitempty"`
	SearXNGPassword string `yaml:"searxng-password,omitempty" json:"-"`

	// PublicFanoutSoftSeconds is the public aggregate's soft deadline:
	// it returns the earliest of all engines settling, the soft deadline
	// with at least one success, or PublicHardCapSeconds.
	PublicFanoutSoftSeconds int `yaml:"public-fanout-soft-seconds,omitempty" json:"public-fanout-soft-seconds,omitempty"`
	// PublicFanoutHardSeconds caps the public aggregate regardless of
	// stragglers; late engines are abandoned.
	PublicFanoutHardSeconds int `yaml:"public-fanout-hard-seconds,omitempty" json:"public-fanout-hard-seconds,omitempty"`

	// ProxyURL overrides the process proxy for search transports.
	// Empty honors ProxyFromEnvironment.
	ProxyURL string `yaml:"proxy-url,omitempty" json:"-"`

	doer Doer
}

// Doer abstracts the HTTP transport so tests can stub it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// WithDoer overrides the HTTP transport. Used by tests.
func (c Config) WithDoer(doer Doer) Config {
	c.doer = doer
	return c
}

// WithDefaults fills unset bounds.
func (c Config) WithDefaults() Config {
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = DefaultTimeoutSeconds
	}
	if c.TimeoutSeconds > MaxTimeoutSeconds {
		c.TimeoutSeconds = MaxTimeoutSeconds
	}
	if c.Limit <= 0 {
		c.Limit = DefaultLimit
	}
	if c.MaxSearches <= 0 {
		c.MaxSearches = 3
	}
	if c.PublicFanoutSoftSeconds <= 0 {
		c.PublicFanoutSoftSeconds = DefaultPublicFanoutSoftSeconds
	}
	if c.PublicFanoutHardSeconds <= 0 {
		c.PublicFanoutHardSeconds = DefaultPublicFanoutHardSeconds
	}
	if c.PublicFanoutHardSeconds < c.PublicFanoutSoftSeconds {
		c.PublicFanoutHardSeconds = c.PublicFanoutSoftSeconds
	}
	return c
}

// Credential returns the explicit value or, when empty, the environment
// variable. The returned string is a secret and must never be logged.
func Credential(explicit, env string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	return strings.TrimSpace(os.Getenv(env))
}

// BraveKey resolves the Brave Search API credential.
func (c Config) BraveKey() string {
	return Credential(c.BraveAPIKey, "BRAVE_API_KEY")
}

// TavilyKey resolves the Tavily API credential.
func (c Config) TavilyKey() string {
	return Credential(c.TavilyAPIKey, "TAVILY_API_KEY")
}

// ExaKey resolves the Exa API credential.
func (c Config) ExaKey() string {
	return Credential(c.ExaAPIKey, "EXA_API_KEY")
}

// XAIKey resolves the xAI API credential.
func (c Config) XAIKey() string {
	return Credential(c.XAIAPIKey, "XAI_API_KEY")
}

// CodexKey resolves the OpenAI API credential used by the Codex provider.
func (c Config) CodexKey() string {
	return Credential(c.CodexAPIKey, "OPENAI_API_KEY")
}

// XAISearchModel resolves the grounding model for xAI searches.
func (c Config) XAISearchModel() string {
	if model := strings.TrimSpace(c.XAIModel); model != "" {
		return model
	}
	if model := strings.TrimSpace(os.Getenv("XAI_SEARCH_MODEL")); model != "" {
		return model
	}
	return DefaultXAISearchModel
}

// CodexSearchModel resolves the grounding model for Codex searches.
func (c Config) CodexSearchModel() string {
	if model := strings.TrimSpace(c.CodexModel); model != "" {
		return model
	}
	if model := strings.TrimSpace(os.Getenv("CODEX_SEARCH_MODEL")); model != "" {
		return model
	}
	return DefaultCodexSearchModel
}

// XAIBaseURL resolves the xAI API base URL.
func (c Config) XAIBaseURL() string {
	if base := strings.TrimSpace(c.XAIEndpoint); base != "" {
		return strings.TrimSuffix(base, "/")
	}
	if base := strings.TrimSuffix(strings.TrimSpace(os.Getenv("XAI_BASE_URL")), "/"); base != "" {
		return base
	}
	return defaultXAIBaseURL
}

// CodexBaseURL resolves the OpenAI API base URL.
func (c Config) CodexBaseURL() string {
	if base := strings.TrimSpace(c.CodexEndpoint); base != "" {
		return strings.TrimSuffix(base, "/")
	}
	if base := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); base != "" {
		return strings.TrimSuffix(base, "/")
	}
	return defaultCodexBaseURL
}

// AnthropicKey resolves the Anthropic search credential. A search-specific
// key wins, then the general API keys, matching OMP precedence.
func (c Config) AnthropicKey() string {
	for _, candidate := range []struct{ explicit, env string }{
		{c.AnthropicAPIKey, "ANTHROPIC_SEARCH_API_KEY"},
		{c.AnthropicAPIKey, "ANTHROPIC_API_KEY"},
		{"", "ANTHROPIC_FOUNDRY_API_KEY"},
	} {
		if key := Credential(candidate.explicit, candidate.env); key != "" {
			return key
		}
	}
	return ""
}

// AnthropicBaseURL resolves the search-only base URL, else the general one.
func (c Config) AnthropicBaseURL() string {
	for _, env := range []string{"ANTHROPIC_SEARCH_BASE_URL", "ANTHROPIC_BASE_URL", "FOUNDRY_BASE_URL"} {
		if base := strings.TrimSuffix(strings.TrimSpace(os.Getenv(env)), "/"); base != "" {
			return base
		}
	}
	if base := strings.TrimSuffix(strings.TrimSpace(c.AnthropicEndpoint), "/"); base != "" {
		return base
	}
	return defaultAnthropicBaseURL
}

// AnthropicSearchModel resolves the Anthropic grounding model.
func (c Config) AnthropicSearchModel() string {
	if model := strings.TrimSpace(c.AnthropicModel); model != "" {
		return model
	}
	if model := strings.TrimSpace(os.Getenv("ANTHROPIC_SEARCH_MODEL")); model != "" {
		return model
	}
	return DefaultAnthropicSearchModel
}

// PerplexityKey resolves the Perplexity API credential.
func (c Config) PerplexityKey() string {
	return Credential(c.PerplexityAPIKey, "PERPLEXITY_API_KEY")
}

// PerplexityToken resolves the Perplexity OAuth token.
func (c Config) PerplexityToken() string {
	return Credential(c.PerplexityOAuth, "PERPLEXITY_OAUTH_TOKEN")
}

// PerplexityCookieHeader resolves the raw Perplexity consumer cookie header.
// It is the auth mode ahead of OAuth upstream, so it takes precedence
// when both are present.
func (c Config) PerplexityCookieHeader() string {
	return Credential(c.PerplexityCookies, "PERPLEXITY_COOKIES")
}

// GeminiKey resolves the Google grounding credential.
func (c Config) GeminiKey() string {
	return Credential(c.GeminiAPIKey, "GEMINI_API_KEY")
}

// GeminiSearchModel resolves the Gemini grounding model.
func (c Config) GeminiSearchModel() string {
	if model := strings.TrimSpace(c.GeminiModel); model != "" {
		return model
	}
	if model := strings.TrimSpace(os.Getenv("GEMINI_SEARCH_MODEL")); model != "" {
		return model
	}
	return DefaultGeminiSearchModel
}

// GeminiBaseURL resolves the Google generative-language endpoint.
func (c Config) GeminiBaseURL() string {
	for _, env := range []string{"GEMINI_SEARCH_BASE_URL", "GEMINI_BASE_URL"} {
		if base := strings.TrimSuffix(strings.TrimSpace(os.Getenv(env)), "/"); base != "" {
			return base
		}
	}
	if base := strings.TrimSuffix(strings.TrimSpace(c.GeminiEndpoint), "/"); base != "" {
		return base
	}
	return defaultGeminiBaseURL
}

// ZAIKey resolves the Z.AI remote MCP credential.
func (c Config) ZAIKey() string {
	return Credential(c.ZAIAPIKey, "ZAI_API_KEY")
}

// TinyFishKey resolves the TinyFish credential.
func (c Config) TinyFishKey() string {
	return Credential(c.TinyFishAPIKey, "TINYFISH_API_KEY")
}

// JinaKey resolves the Jina Reader credential.
func (c Config) JinaKey() string {
	return Credential(c.JinaAPIKey, "JINA_API_KEY")
}

// KagiKey resolves the Kagi credential.
func (c Config) KagiKey() string {
	return Credential(c.KagiAPIKey, "KAGI_API_KEY")
}

// FirecrawlKey resolves the Firecrawl credential.
func (c Config) FirecrawlKey() string {
	return Credential(c.FirecrawlAPIKey, "FIRECRAWL_API_KEY")
}

// FirecrawlBaseURL resolves the Firecrawl endpoint, honoring the
// self-hosting override aliases.
func (c Config) FirecrawlBaseURL() string {
	for _, env := range []string{"FIRECRAWL_BASE_URL", "FIRECRAWL_API_URL"} {
		if base := strings.TrimSuffix(strings.TrimSpace(os.Getenv(env)), "/"); base != "" {
			return base
		}
	}
	if base := strings.TrimSuffix(strings.TrimSpace(c.FirecrawlEndpoint), "/"); base != "" {
		return base
	}
	return defaultFirecrawlBaseURL
}

// KimiKey resolves the Kimi search credential. The Open Platform key does
// not authenticate the Kimi search service, so only search-specific
// variables are consulted.
func (c Config) KimiKey() string {
	for _, env := range []string{"MOONSHOT_SEARCH_API_KEY", "KIMI_SEARCH_API_KEY"} {
		if key := strings.TrimSpace(os.Getenv(env)); key != "" {
			return key
		}
	}
	return strings.TrimSpace(c.KimiAPIKey)
}

// ParallelKey resolves the Parallel credential.
func (c Config) ParallelKey() string {
	return Credential(c.ParallelAPIKey, "PARALLEL_API_KEY")
}

// SyntheticKey resolves the Synthetic credential.
func (c Config) SyntheticKey() string {
	return Credential(c.SyntheticAPIKey, "SYNTHETIC_API_KEY")
}

// OllamaKey resolves the Ollama Cloud credential.
func (c Config) OllamaKey() string {
	return Credential(c.OllamaAPIKey, "OLLAMA_CLOUD_API_KEY")
}

// KimiBaseURL resolves the Kimi Code search endpoint, honoring the
// self-hosted or proxied overrides.
func (c Config) KimiBaseURL() string {
	for _, env := range []string{"MOONSHOT_SEARCH_BASE_URL", "KIMI_SEARCH_BASE_URL"} {
		if base := strings.TrimSuffix(strings.TrimSpace(os.Getenv(env)), "/"); base != "" {
			return base
		}
	}
	return defaultKimiBaseURL
}

// OpenRouterKey resolves the OpenRouter credential.
func (c Config) OpenRouterKey() string {
	return Credential(c.OpenRouterAPIKey, "OPENROUTER_API_KEY")
}

// OpenRouterBaseURL resolves the OpenRouter API base.
func (c Config) OpenRouterBaseURL() string {
	if base := strings.TrimSuffix(strings.TrimSpace(c.OpenRouterEndpoint), "/"); base != "" {
		return base
	}
	if base := strings.TrimSuffix(strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")), "/"); base != "" {
		return base
	}
	return openrouterDefaultBaseURL
}

// OpenRouterSearchModel resolves the model used for web-plugin grounding.
func (c Config) OpenRouterSearchModel() string {
	if model := strings.TrimSpace(c.OpenRouterModelID); model != "" {
		return model
	}
	if model := strings.TrimSpace(os.Getenv("OPENROUTER_SEARCH_MODEL")); model != "" {
		return model
	}
	return DefaultOpenRouterSearchModel
}

// SearXNGAddress resolves the self-hosted SearXNG endpoint.
func (c Config) SearXNGAddress() string {
	if address := strings.TrimSpace(c.SearXNGEndpoint); address != "" {
		return strings.TrimSuffix(address, "/")
	}
	return strings.TrimSuffix(strings.TrimSpace(os.Getenv("SEARXNG_ENDPOINT")), "/")
}

// SearXNGAuth resolves bearer/basic credentials. Basic wins over bearer.
func (c Config) SearXNGAuth() (username, password, token string) {
	username = Credential(c.SearXNGUsername, "SEARXNG_USERNAME")
	password = os.Getenv("SEARXNG_PASSWORD")
	if strings.TrimSpace(c.SearXNGPassword) != "" {
		password = c.SearXNGPassword
	}
	token = Credential(c.SearXNGToken, "SEARXNG_TOKEN")
	return strings.TrimSpace(username), password, strings.TrimSpace(token)
}

// ChainOrder returns the effective provider order: valid first-occurrence
// IDs from Order, then unlisted built-ins in default relative order.
func (c Config) ChainOrder() []string {
	seen := make(map[string]bool, len(DefaultProviderOrder)+len(c.Order))
	var order []string
	for _, id := range c.Order {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || id == "auto" || seen[id] || !IsKnownProvider(id) {
			continue
		}
		seen[id] = true
		order = append(order, id)
	}
	for _, id := range DefaultProviderOrder {
		if !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	return order
}

// Excluded reports whether id is removed from the automatic chain.
// Forced per-request providers bypass exclusion.
func (c Config) Excluded(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, excluded := range c.Exclude {
		if strings.ToLower(strings.TrimSpace(excluded)) == id {
			return true
		}
	}
	return false
}

// ExplicitOrder reports whether id was explicitly listed in Order.
func (c Config) ExplicitOrder(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, ordered := range c.Order {
		if strings.ToLower(strings.TrimSpace(ordered)) == id {
			return true
		}
	}
	return false
}

// IsKnownProvider reports whether id names a built-in provider.
func IsKnownProvider(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case ProviderCodex, ProviderXAI, ProviderOpenRouter, ProviderGemini, ProviderAnthropic, ProviderPerplexity, ProviderZAI,
		ProviderExa, ProviderTinyFish, ProviderJina, ProviderKagi, ProviderTavily, ProviderFirecrawl,
		ProviderBrave, ProviderKimi, ProviderParallel, ProviderSynthetic, ProviderOllama, ProviderSearXNG,
		ProviderStartpage, ProviderDuckDuckGo, ProviderEcosia, ProviderGoogle, ProviderMojeek, ProviderPublic:
		return true
	default:
		return false
	}
}
