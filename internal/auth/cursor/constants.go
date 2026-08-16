package cursor

const (
	LoginURL       = "https://cursor.com/loginDeepControl"
	PollURL        = "https://api2.cursor.sh/auth/poll"
	RefreshURL     = "https://api2.cursor.sh/oauth/token"
	APIBaseURL     = "https://api2.cursor.sh"
	RunPath        = "/agent.v1.AgentService/Run"
	ModelsPath     = "/agent.v1.AgentService/GetUsableModels"
	CLIClientID    = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"
	ClientVersion  = "cli-2026.01.09-231024f"
	ClientType     = "cli"
	ModelCacheKey  = "models"
	OAuthKind      = "oauth"
	Provider       = "cursor"
	LoginTimeout   = 5 * 60
	DefaultContext = 200000
	DefaultOutput  = 64000
)
