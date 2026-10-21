package config

const (
	DefaultPanelGitHubRepository = "https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
	DefaultPprofAddr             = "127.0.0.1:8316"
	DefaultAuthDir               = "~/.cli-proxy-api"
	DefaultDiscoveryServiceType  = "_ai-gateway._tcp"

	// DefaultUsageAPICacheSeconds is the default cache lifetime for user-facing usage reports.
	DefaultUsageAPICacheSeconds = 60
	// MaxUsageAPICacheSeconds bounds the configured usage report cache lifetime.
	MaxUsageAPICacheSeconds = 3600
)
