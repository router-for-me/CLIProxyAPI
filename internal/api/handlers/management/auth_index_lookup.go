package management

import (
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func findAuthByStableIndex(auths []*coreauth.Auth, authIndex string) *coreauth.Auth {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return nil
	}

	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if auth.StableIndex() == authIndex {
			return auth
		}
	}
	return nil
}
