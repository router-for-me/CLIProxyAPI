package copilot

// Default OAuth parameters for Copilot provider. These serve as safe
// fallbacks so that OAuth can work out-of-the-box without YAML entries,
// mirroring other providers' pattern of embedded defaults.

const (
    DefaultAuthURL     = "https://auth.copilot.example.com/oauth/authorize"
    DefaultTokenURL    = "https://auth.copilot.example.com/oauth/token"
    DefaultClientID    = "copilot_client_placeholder"
    DefaultRedirectPort = 54556
    DefaultScope       = "openid email profile offline_access"
)

// GitHub Device Flow defaults used by the reference Copilot API implementation.
const (
    DefaultGitHubClientID       = "Iv1.b507a08c87ecfe98"
    DefaultGitHubScope          = "read:user"
    DefaultGitHubBaseURL        = "https://github.com"
    DefaultGitHubAPIBaseURL     = "https://api.github.com"
    DefaultGitHubDeviceCodePath = "/login/device/code"
    DefaultGitHubAccessTokenPath = "/login/oauth/access_token"
    DefaultCopilotTokenPath     = "/copilot_internal/v2/token"
)
