package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Pooling is configured per api-key credential, so tests opt in through the auth
// override instead of a global config block.
func poolEnabled() *cliproxyauth.ProxyConnectionPoolOverride {
	enabled := true
	return &cliproxyauth.ProxyConnectionPoolOverride{Enabled: &enabled}
}

func poolOptInAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: id, ProxyConnectionPool: poolEnabled()}
}

func poolAuth(id string, override *cliproxyauth.ProxyConnectionPoolOverride) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: id, ProxyConnectionPool: override}
}

// poolBlock is the block with no fields: the opt-in that uses the defaults.
func poolBlock() *cliproxyauth.ProxyConnectionPoolOverride {
	return &cliproxyauth.ProxyConnectionPoolOverride{}
}

func testConfig() *config.Config { return &config.Config{} }
