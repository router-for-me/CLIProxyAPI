package synthesizer

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// PluginAuthParser parses auth JSON owned by plugin providers.
type PluginAuthParser interface {
	ParseAuth(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error)
}

// PluginMultiAuthParser expands one auth JSON payload into multiple plugin auth records.
// Returning handled=true with an empty slice means the plugin intentionally suppresses built-in parsing.
type PluginMultiAuthParser interface {
	ParseAuths(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error)
}

// SynthesisContext provides the context needed for auth synthesis.
type SynthesisContext struct {
	// Config is the current configuration
	Config *config.Config
	// AuthDir is the directory containing auth files
	AuthDir string
	// Now is the current time for timestamps
	Now time.Time
	// IDGenerator generates stable IDs for auth entries
	IDGenerator *StableIDGenerator
	// PluginAuthParser parses plugin-owned auth files
	PluginAuthParser PluginAuthParser
	// StrictFileParsing surfaces parse errors instead of silently skipping or falling back.
	StrictFileParsing bool
	// IncompleteFiles records existing auth JSON file paths whose contents were
	// empty or not valid JSON during a non-strict directory scan, or file/directory
	// paths whose os.ReadFile or os.ReadDir failed with an error other than fs.ErrNotExist.
	IncompleteFiles map[string]struct{}
}

// RecordIncompleteFile records an existing auth JSON file whose content is not yet complete JSON
// or a file/directory path whose os.ReadFile or os.ReadDir failed with an error other than fs.ErrNotExist.
func (c *SynthesisContext) RecordIncompleteFile(path string) {
	if c == nil || path == "" {
		return
	}
	if c.IncompleteFiles == nil {
		c.IncompleteFiles = make(map[string]struct{})
	}
	c.IncompleteFiles[path] = struct{}{}
}
