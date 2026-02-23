package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	log "github.com/sirupsen/logrus"
)

// DoDeepSeekLogin prompts for DeepSeek API key and stores it in auth-dir.
func DoDeepSeekLogin(cfg *config.Config, options *LoginOptions) {
	doGenericAPIKeyLogin(cfg, options, "DeepSeek", "platform.deepseek.com", "deepseek-api-key.json", func(tokenFileRef string) {
		entry := config.DeepSeekKey{
			TokenFile: tokenFileRef,
			BaseURL:   "https://api.deepseek.com",
		}
		if len(cfg.DeepSeekKey) == 0 {
			cfg.DeepSeekKey = []config.DeepSeekKey{entry}
		} else {
			cfg.DeepSeekKey[0] = entry
		}
	})
}

// DoGroqLogin prompts for Groq API key and stores it in auth-dir.
func DoGroqLogin(cfg *config.Config, options *LoginOptions) {
	doGenericAPIKeyLogin(cfg, options, "Groq", "console.groq.com", "groq-api-key.json", func(tokenFileRef string) {
		entry := config.GroqKey{
			TokenFile: tokenFileRef,
			BaseURL:   "https://api.groq.com/openai/v1",
		}
		if len(cfg.GroqKey) == 0 {
			cfg.GroqKey = []config.GroqKey{entry}
		} else {
			cfg.GroqKey[0] = entry
		}
	})
}

// DoMistralLogin prompts for Mistral API key and stores it in auth-dir.
func DoMistralLogin(cfg *config.Config, options *LoginOptions) {
	doGenericAPIKeyLogin(cfg, options, "Mistral", "console.mistral.ai", "mistral-api-key.json", func(tokenFileRef string) {
		entry := config.MistralKey{
			TokenFile: tokenFileRef,
			BaseURL:   "https://api.mistral.ai/v1",
		}
		if len(cfg.MistralKey) == 0 {
			cfg.MistralKey = []config.MistralKey{entry}
		} else {
			cfg.MistralKey[0] = entry
		}
	})
}

// DoSiliconFlowLogin prompts for SiliconFlow API key and stores it in auth-dir.
func DoSiliconFlowLogin(cfg *config.Config, options *LoginOptions) {
	doGenericAPIKeyLogin(cfg, options, "SiliconFlow", "cloud.siliconflow.cn", "siliconflow-api-key.json", func(tokenFileRef string) {
		entry := config.SiliconFlowKey{
			TokenFile: tokenFileRef,
			BaseURL:   "https://api.siliconflow.cn/v1",
		}
		if len(cfg.SiliconFlowKey) == 0 {
			cfg.SiliconFlowKey = []config.SiliconFlowKey{entry}
		} else {
			cfg.SiliconFlowKey[0] = entry
		}
	})
}

// DoOpenRouterLogin prompts for OpenRouter API key and stores it in auth-dir.
func DoOpenRouterLogin(cfg *config.Config, options *LoginOptions) {
	doGenericAPIKeyLogin(cfg, options, "OpenRouter", "openrouter.ai/keys", "openrouter-api-key.json", func(tokenFileRef string) {
		entry := config.OpenRouterKey{
			TokenFile: tokenFileRef,
			BaseURL:   "https://openrouter.ai/api/v1",
		}
		if len(cfg.OpenRouterKey) == 0 {
			cfg.OpenRouterKey = []config.OpenRouterKey{entry}
		} else {
			cfg.OpenRouterKey[0] = entry
		}
	})
}

// DoTogetherLogin prompts for Together AI API key and stores it in auth-dir.
func DoTogetherLogin(cfg *config.Config, options *LoginOptions) {
	doGenericAPIKeyLogin(cfg, options, "Together AI", "api.together.xyz/settings/api-keys", "together-api-key.json", func(tokenFileRef string) {
		entry := config.TogetherKey{
			TokenFile: tokenFileRef,
			BaseURL:   "https://api.together.xyz/v1",
		}
		if len(cfg.TogetherKey) == 0 {
			cfg.TogetherKey = []config.TogetherKey{entry}
		} else {
			cfg.TogetherKey[0] = entry
		}
	})
}

// DoFireworksLogin prompts for Fireworks AI API key and stores it in auth-dir.
func DoFireworksLogin(cfg *config.Config, options *LoginOptions) {
	doGenericAPIKeyLogin(cfg, options, "Fireworks AI", "fireworks.ai/account/api-keys", "fireworks-api-key.json", func(tokenFileRef string) {
		entry := config.FireworksKey{
			TokenFile: tokenFileRef,
			BaseURL:   "https://api.fireworks.ai/inference/v1",
		}
		if len(cfg.FireworksKey) == 0 {
			cfg.FireworksKey = []config.FireworksKey{entry}
		} else {
			cfg.FireworksKey[0] = entry
		}
	})
}

// DoNovitaLogin prompts for Novita AI API key and stores it in auth-dir.
func DoNovitaLogin(cfg *config.Config, options *LoginOptions) {
	doGenericAPIKeyLogin(cfg, options, "Novita AI", "novita.ai/dashboard", "novita-api-key.json", func(tokenFileRef string) {
		entry := config.NovitaKey{
			TokenFile: tokenFileRef,
			BaseURL:   "https://api.novita.ai/v1",
		}
		if len(cfg.NovitaKey) == 0 {
			cfg.NovitaKey = []config.NovitaKey{entry}
		} else {
			cfg.NovitaKey[0] = entry
		}
	})
}

// DoClineLogin prompts for Cline API key and stores it as an OpenAI-compatible provider.
func DoClineLogin(cfg *config.Config, options *LoginOptions) {
	doGenericOpenAICompatLogin(
		cfg,
		options,
		"Cline",
		"cline.bot",
		"cline-api-key.json",
		"cline",
		"https://api.cline.bot/v1",
		"cline-default",
	)
}

// DoAmpLogin prompts for AMP API key and stores it as an OpenAI-compatible provider.
func DoAmpLogin(cfg *config.Config, options *LoginOptions) {
	doGenericOpenAICompatLogin(
		cfg,
		options,
		"AMP",
		"ampcode.com",
		"amp-api-key.json",
		"amp",
		"https://api.ampcode.com/v1",
		"amp-default",
	)
}

// DoFactoryAPILogin prompts for Factory API key and stores it as an OpenAI-compatible provider.
func DoFactoryAPILogin(cfg *config.Config, options *LoginOptions) {
	doGenericOpenAICompatLogin(
		cfg,
		options,
		"Factory API",
		"app.factory.ai",
		"factory-api-key.json",
		"factory-api",
		"https://api.factory.ai/v1",
		"factory-default",
	)
}

func doGenericAPIKeyLogin(cfg *config.Config, options *LoginOptions, providerName, providerURL, fileName string, updateConfig func(string)) {
	if options == nil {
		options = &LoginOptions{}
	}

	var apiKey string
	promptMsg := fmt.Sprintf("Enter %s API key (from %s): ", providerName, providerURL)
	if options.Prompt != nil {
		var err error
		apiKey, err = options.Prompt(promptMsg)
		if err != nil {
			log.Errorf("%s prompt failed: %v", providerName, err)
			return
		}
	} else {
		fmt.Print(promptMsg)
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			log.Errorf("%s: failed to read API key", providerName)
			return
		}
		apiKey = strings.TrimSpace(scanner.Text())
	}

	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		log.Errorf("%s: API key cannot be empty", providerName)
		return
	}

	authDir, err := ensureAuthDir(strings.TrimSpace(cfg.AuthDir), providerName)
	if err != nil {
		log.Errorf("%s: %v", providerName, err)
		return
	}

	tokenPath := filepath.Join(authDir, fileName)
	tokenData := map[string]string{"api_key": apiKey}
	raw, err := json.MarshalIndent(tokenData, "", "  ")
	if err != nil {
		log.Errorf("%s: failed to marshal token: %v", providerName, err)
		return
	}
	if err := os.WriteFile(tokenPath, raw, 0o600); err != nil {
		log.Errorf("%s: failed to write token file %s: %v", providerName, tokenPath, err)
		return
	}

	tokenFileRef := authDirTokenFileRef(authDir, fileName)

	updateConfig(tokenFileRef)

	configPath := options.ConfigPath
	if configPath == "" {
		log.Errorf("%s: config path not set; cannot save", providerName)
		return
	}

	if err := config.SaveConfigPreserveComments(configPath, cfg); err != nil {
		log.Errorf("%s: failed to save config: %v", providerName, err)
		return
	}

	fmt.Printf("%s API key saved to %s (auth-dir). Config updated with token-file. Restart the proxy to apply.\n", providerName, tokenPath)
}

func doGenericOpenAICompatLogin(
	cfg *config.Config,
	options *LoginOptions,
	providerName string,
	providerURL string,
	fileName string,
	compatName string,
	baseURL string,
	defaultModel string,
) {
	doGenericAPIKeyLogin(cfg, options, providerName, providerURL, fileName, func(tokenFileRef string) {
		entry := config.OpenAICompatibility{
			Name:    compatName,
			BaseURL: baseURL,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{TokenFile: tokenFileRef},
			},
			Models: []config.OpenAICompatibilityModel{
				{Name: defaultModel, Alias: defaultModel},
			},
		}

		replaced := false
		for i := range cfg.OpenAICompatibility {
			if strings.EqualFold(cfg.OpenAICompatibility[i].Name, compatName) {
				cfg.OpenAICompatibility[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, entry)
		}
	})
}
