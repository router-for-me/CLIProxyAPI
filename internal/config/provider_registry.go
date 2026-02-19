package config

import "strings"

// ProviderSpec defines a provider's metadata for codegen and runtime injection.
type ProviderSpec struct {
	Name          string
	YAMLKey       string // If set, a dedicated block is generated in the Config struct
	GoName        string // Optional: Override PascalCase name in Go (defaults to Title(Name))
	BaseURL       string
	EnvVars       []string // Environment variables for automatic injection
	DefaultModels []OpenAICompatibilityModel
}

// AllProviders defines the registry of all supported LLM providers.
// This is the source of truth for generated config fields and synthesizers.
var AllProviders = []ProviderSpec{
	{
		Name:    "minimax",
		YAMLKey: "minimax",
		GoName:  "MiniMax",
		BaseURL: "https://api.minimax.chat/v1",
	},
	{
		Name:    "roo",
		YAMLKey: "roo",
		GoName:  "Roo",
		BaseURL: "https://api.roocode.com/v1",
	},
	{
		Name:    "kilo",
		YAMLKey: "kilo",
		GoName:  "Kilo",
		BaseURL: "https://api.kilo.ai/v1",
	},
	{
		Name:    "deepseek",
		YAMLKey: "deepseek",
		GoName:  "DeepSeek",
		BaseURL: "https://api.deepseek.com",
	},
	{
		Name:    "groq",
		YAMLKey: "groq",
		GoName:  "Groq",
		BaseURL: "https://api.groq.com/openai/v1",
	},
	{
		Name:    "mistral",
		YAMLKey: "mistral",
		GoName:  "Mistral",
		BaseURL: "https://api.mistral.ai/v1",
	},
	{
		Name:    "siliconflow",
		YAMLKey: "siliconflow",
		GoName:  "SiliconFlow",
		BaseURL: "https://api.siliconflow.cn/v1",
	},
	{
		Name:    "openrouter",
		YAMLKey: "openrouter",
		GoName:  "OpenRouter",
		BaseURL: "https://openrouter.ai/api/v1",
	},
	{
		Name:    "together",
		YAMLKey: "together",
		GoName:  "Together",
		BaseURL: "https://api.together.xyz/v1",
	},
	{
		Name:    "fireworks",
		YAMLKey: "fireworks",
		GoName:  "Fireworks",
		BaseURL: "https://api.fireworks.ai/inference/v1",
	},
	{
		Name:    "novita",
		YAMLKey: "novita",
		GoName:  "Novita",
		BaseURL: "https://api.novita.ai/v1",
	},
	{
		Name:    "zen",
		BaseURL: "https://opencode.ai/zen/v1",
		EnvVars: []string{"ZEN_API_KEY", "OPENCODE_API_KEY", "THGENT_ZEN_API_KEY"},
		DefaultModels: []OpenAICompatibilityModel{
			{Name: "glm-5", Alias: "glm-5"},
			{Name: "glm-5", Alias: "z-ai/glm-5"},
			{Name: "glm-5", Alias: "gpt-5-mini"},
			{Name: "glm-5", Alias: "gemini-3-flash"},
		},
	},
	{
		Name:    "nim",
		BaseURL: "https://integrate.api.nvidia.com/v1",
		EnvVars: []string{"NIM_API_KEY", "THGENT_NIM_API_KEY", "NVIDIA_API_KEY"},
		DefaultModels: []OpenAICompatibilityModel{
			{Name: "z-ai/glm-5", Alias: "z-ai/glm-5"},
			{Name: "z-ai/glm-5", Alias: "glm-5"},
			{Name: "z-ai/glm-5", Alias: "step-3.5-flash"},
		},
	},
}

// GetDedicatedProviders returns providers that have a dedicated config block.
func GetDedicatedProviders() []ProviderSpec {
	var out []ProviderSpec
	for _, p := range AllProviders {
		if p.YAMLKey != "" {
			out = append(out, p)
		}
	}
	return out
}

// GetPremadeProviders returns providers that can be injected from environment variables.
func GetPremadeProviders() []ProviderSpec {
	var out []ProviderSpec
	for _, p := range AllProviders {
		if len(p.EnvVars) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// GetProviderByName looks up a provider by its name (case-insensitive).
func GetProviderByName(name string) (ProviderSpec, bool) {
	for _, p := range AllProviders {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return ProviderSpec{}, false
}
