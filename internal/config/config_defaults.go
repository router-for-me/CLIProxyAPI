package config

const (
	DefaultPanelGitHubRepository = "https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
	DefaultPprofAddr             = "127.0.0.1:8316"
	DefaultAuthDir               = "~/.cli-proxy-api"
	DefaultDiscoveryServiceType  = "_ai-gateway._tcp"
	DefaultCodexBootstrapTimeout = "30s"
)

func applyCodexRuntimeDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	cfg.DisableImageGeneration = DisableImageGenerationPassthrough
	cfg.Codex.StreamBootstrapBuffering = true
	cfg.Codex.StreamBootstrapTimeout = DefaultCodexBootstrapTimeout
}

func newOptionalFallbackConfig() *Config {
	cfg := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
	applyCodexRuntimeDefaults(cfg)
	return cfg
}
