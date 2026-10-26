package cmd

import "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

// DoKiroLogin logs in with AWS Builder ID, or with Google/GitHub when method names one.
func DoKiroLogin(cfg *config.Config, options *LoginOptions, method string) {
	doDeviceSubscriptionLogin(cfg, options, "kiro", "Kiro", map[string]string{"method": method})
}
