package management

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	log "github.com/sirupsen/logrus"
)

// launchKunKunOAuthBrowser opens an OAuth authorization URL with the configured
// alternate browser launcher, carrying the session-scoped proxy so that the
// login egress IP matches the runtime egress IP (ADR D6).
//
// It is a no-op unless a launcher is installed, and it never blocks the HTTP
// handler because the launcher waits for the interactive login to finish.
func launchKunKunOAuthBrowser(provider, sessionProxyURL, authURL string) {
	authURL = strings.TrimSpace(authURL)
	if authURL == "" || !browser.HasLauncher() {
		return
	}

	ctx := browser.WithOAuthProxyURL(context.Background(), sessionProxyURL)
	ctx = browser.WithOAuthProvider(ctx, provider)

	go func() {
		if errLaunch := browser.OpenURLWithContext(ctx, authURL); errLaunch != nil {
			log.WithFields(log.Fields{
				"provider": provider,
			}).Warnf("KunKun OAuth browser launch failed: %v", errLaunch)
		}
	}()
}
