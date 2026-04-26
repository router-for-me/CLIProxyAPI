package executor

import (
	"strconv"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func needResponseHeadersFromOptions(opts cliproxyexecutor.Options) bool {
	if len(opts.Metadata) == 0 {
		return false
	}
	raw, ok := opts.Metadata[cliproxyexecutor.NeedResponseHeadersMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && parsed
	case []byte:
		parsed, err := strconv.ParseBool(strings.TrimSpace(string(v)))
		return err == nil && parsed
	default:
		return false
	}
}
