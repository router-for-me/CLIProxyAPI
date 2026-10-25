package cmd

import "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

func DoKiroLogin(cfg *config.Config, options *LoginOptions) {
	doDeviceSubscriptionLogin(cfg, options, "kiro", "Kiro")
}
