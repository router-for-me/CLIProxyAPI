package cmd

import "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

// DoQoderLogin starts Qoder international OAuth device authorization.
func DoQoderLogin(cfg *config.Config, options *LoginOptions) {
	doDeviceSubscriptionLogin(cfg, options, "qoder", "Qoder International", nil)
}

// DoQoderCNLogin starts Qoder China OAuth device authorization.
func DoQoderCNLogin(cfg *config.Config, options *LoginOptions) {
	doDeviceSubscriptionLogin(cfg, options, "qoder-cn", "Qoder China", nil)
}
