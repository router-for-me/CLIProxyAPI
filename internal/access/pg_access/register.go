package pgaccess

import (
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// Register installs (or removes) the PG-backed access provider against the
// supplied APIKeyStore. When lookup is nil the provider is unregistered so
// requests fall through to the inline config provider. Callers should invoke
// this once during server bootstrap (after PostgresStore is initialized) and
// again on hot-reload if the PG backend configuration changes.
func Register(lookup Lookup) {
	if lookup == nil {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypePGAPIKey)
		return
	}
	sdkaccess.RegisterProvider(sdkaccess.AccessProviderTypePGAPIKey, New(lookup))
}
