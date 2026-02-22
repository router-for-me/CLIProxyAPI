// Package cmd provides command-line interface helper flows for cliproxy.
package cmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

type setupOption struct {
	label string
	run   func(*config.Config, *LoginOptions)
}

// SetupOptions controls interactive wizard behavior.
type SetupOptions struct {
	// ConfigPath points to the active config file.
	ConfigPath string
	// Prompt provides custom prompt handling for tests.
	Prompt func(string) (string, error)
}

// DoSetupWizard runs an interactive first-run setup flow.
func DoSetupWizard(cfg *config.Config, options *SetupOptions) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	promptFn := options.getPromptFn()

	authDir := strings.TrimSpace(cfg.AuthDir)
	fmt.Println("Welcome to cliproxy setup.")
	fmt.Printf("Config file: %s\n", emptyOrUnset(options.ConfigPath, "(default)"))
	fmt.Printf("Auth directory: %s\n", emptyOrUnset(authDir, "~/.cli-proxy-api"))

	fmt.Println("")
	printProfileSummary(cfg)
	fmt.Println("")

	choice, err := promptFn("Continue with guided provider setup? [y/N]: ")
	if err != nil || strings.ToLower(strings.TrimSpace(choice)) != "y" {
		printPostCheckSummary(cfg)
		return
	}

	for {
		choices := setupOptions()
		fmt.Println("Available provider setup actions:")
		for i, opt := range choices {
			fmt.Printf("  %2d) %s\n", i+1, opt.label)
		}
		fmt.Printf("  %2d) %s\n", len(choices)+1, "Skip setup and print post-check summary")
		selection, errPrompt := promptFn("Select providers (comma-separated IDs, e.g. 1,3,5): ")
		if errPrompt != nil {
			fmt.Printf("Setup canceled: %v\n", errPrompt)
			return
		}

		normalized := normalizeSelectionStrings(selection)
		if len(normalized) == 0 {
			printPostCheckSummary(cfg)
			return
		}

		selectionContext := &LoginOptions{
			NoBrowser:    false,
			CallbackPort: 0,
			Prompt:       promptFn,
			ConfigPath:   options.ConfigPath,
		}
		for _, raw := range normalized {
			if raw == "" {
				continue
			}
			if raw == "skip" || raw == "s" || raw == "q" || raw == "quit" {
				printPostCheckSummary(cfg)
				return
			}
			if raw == "all" || raw == "a" {
				for _, option := range choices {
					option.run(cfg, selectionContext)
				}
				printPostCheckSummary(cfg)
				return
			}
			idx, parseErr := strconv.Atoi(raw)
			if parseErr != nil || idx < 1 || idx > len(choices) {
				fmt.Printf("Ignoring invalid provider index %q\n", raw)
				continue
			}
			option := choices[idx-1]
			option.run(cfg, selectionContext)
		}
		printPostCheckSummary(cfg)
		return
	}
}

func (options *SetupOptions) getPromptFn() func(string) (string, error) {
	if options == nil {
		return defaultProjectPrompt()
	}
	if options.Prompt != nil {
		return options.Prompt
	}
	return defaultProjectPrompt()
}

func setupOptions() []setupOption {
	return []setupOption{
		{label: "Gemini OAuth login", run: func(cfg *config.Config, loginOptions *LoginOptions) {
			DoLogin(cfg, "", loginOptions)
		}},
		{label: "Claude OAuth login", run: DoClaudeLogin},
		{label: "Codex OAuth login", run: DoCodexLogin},
		{label: "Kiro OAuth login", run: DoKiroLogin},
		{label: "GitHub Copilot OAuth login", run: DoGitHubCopilotLogin},
		{label: "MiniMax API key login", run: DoMinimaxLogin},
		{label: "Kimi API key/OAuth login", run: DoKimiLogin},
		{label: "DeepSeek API key login", run: DoDeepSeekLogin},
		{label: "Groq API key login", run: DoGroqLogin},
		{label: "Mistral API key login", run: DoMistralLogin},
		{label: "SiliconFlow API key login", run: DoSiliconFlowLogin},
		{label: "OpenRouter API key login", run: DoOpenRouterLogin},
		{label: "Together AI API key login", run: DoTogetherLogin},
		{label: "Fireworks AI API key login", run: DoFireworksLogin},
		{label: "Novita AI API key login", run: DoNovitaLogin},
		{label: "Roo Code login", run: DoRooLogin},
		{label: "Antigravity login", run: DoAntigravityLogin},
		{label: "iFlow OAuth login", run: DoIFlowLogin},
		{label: "Qwen OAuth login", run: DoQwenLogin},
	}
}

func printProfileSummary(cfg *config.Config) {
	fmt.Println("Detected auth profile signals:")
	if cfg == nil {
		fmt.Println("  - no config loaded")
		return
	}
	enabled := map[string]bool{
		"Codex API key":        len(cfg.CodexKey) > 0,
		"Claude API key":       len(cfg.ClaudeKey) > 0,
		"Gemini OAuth config":  len(cfg.GeminiKey) > 0,
		"Kiro OAuth config":    len(cfg.KiroKey) > 0,
		"Cursor OAuth config":  len(cfg.CursorKey) > 0,
		"MiniMax":              len(cfg.MiniMaxKey) > 0,
		"Kilo":                 len(cfg.KiloKey) > 0,
		"Roo":                  len(cfg.RooKey) > 0,
		"DeepSeek":             len(cfg.DeepSeekKey) > 0,
		"Groq":                 len(cfg.GroqKey) > 0,
		"Mistral":              len(cfg.MistralKey) > 0,
		"SiliconFlow":          len(cfg.SiliconFlowKey) > 0,
		"OpenRouter":           len(cfg.OpenRouterKey) > 0,
		"Together":             len(cfg.TogetherKey) > 0,
		"Fireworks":            len(cfg.FireworksKey) > 0,
		"Novita":               len(cfg.NovitaKey) > 0,
		"OpenAI compatibility": len(cfg.OpenAICompatibility) > 0,
	}

	keys := make([]string, 0, len(enabled))
	for key := range enabled {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		state := "no"
		if enabled[key] {
			state = "yes"
		}
		fmt.Printf("  - %s: %s\n", key, state)
	}
}

func printPostCheckSummary(cfg *config.Config) {
	fmt.Println("Setup summary:")
	if cfg == nil {
		fmt.Println("  - No config loaded.")
		return
	}
	fmt.Printf("  - auth-dir: %s\n", emptyOrUnset(strings.TrimSpace(cfg.AuthDir), "unset"))
	fmt.Printf("  - configured providers: codex=%d, claude=%d, kiro=%d, openai-compat=%d\n",
		len(cfg.CodexKey), len(cfg.ClaudeKey), len(cfg.KiroKey), len(cfg.OpenAICompatibility))
}

func normalizeSelectionStrings(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(part))
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func emptyOrUnset(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
