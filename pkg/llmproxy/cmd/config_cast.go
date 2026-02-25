package cmd

import (
	"unsafe"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// castToInternalConfig converts a pkg/llmproxy/config.Config pointer to an internal/config.Config pointer.
// This is safe because internal/config.Config is a subset of pkg/llmproxy/config.Config,
// and the memory layout of the common fields is identical.
// The extra fields in pkg/llmproxy/config.Config are ignored during the cast.
func castToInternalConfig(cfg *config.Config) *internalconfig.Config {
	return (*internalconfig.Config)(unsafe.Pointer(cfg))
}

// castToSDKConfig converts a pkg/llmproxy/config.Config pointer to an sdk/config.Config pointer.
// This is safe because sdk/config.Config is an alias for internal/config.Config, which is a subset
// of pkg/llmproxy/config.Config. The memory layout of the common fields is identical.
func castToSDKConfig(cfg *config.Config) *sdkconfig.Config {
	return (*sdkconfig.Config)(unsafe.Pointer(cfg))
}
