package common

import "strings"

// Antigravity agents (Interactions API) ship intrinsic sandbox tools such as
// read_file / write_file / execute_code. When a client-defined function re-declares
// one of those names, Google returns 500 "Unknown Error". The proxy therefore renames
// colliding client tools with the ExternalToolPrefix on the way upstream and
// strips the prefix again on every response path, so the client keeps seeing
// its original names.
const ExternalToolPrefix = "external_"

// antigravityCollidingToolNames lists client-facing tool names that collide
// with the antigravity agent's built-in sandbox tools.
var antigravityCollidingToolNames = map[string]struct{}{
	"read_file":    {},
	"write_file":   {},
	"execute_code": {},
}

// AntigravityToolNameToUpstream maps a client-facing tool name to the name
// sent to the antigravity Interactions API. Escape the entire prefix chain
// ending in an intrinsic name so real external_ names remain distinguishable.
func AntigravityToolNameToUpstream(name string) string {
	if antigravityIntrinsicBase(name) {
		return ExternalToolPrefix + name
	}
	return name
}

func antigravityIntrinsicBase(name string) bool {
	for strings.HasPrefix(name, ExternalToolPrefix) {
		name = strings.TrimPrefix(name, ExternalToolPrefix)
	}
	_, collides := antigravityCollidingToolNames[name]
	return collides
}

// AntigravityUpstreamToolNameToClient strips the external prefix from an
// upstream tool name before it reaches the client, provided that the base
// name is an intrinsic tool that was prefixed to avoid collisions. Non-colliding
// or non-prefixed names pass through unchanged.
func AntigravityUpstreamToolNameToClient(name string) string {
	if strings.HasPrefix(name, ExternalToolPrefix) {
		base := strings.TrimPrefix(name, ExternalToolPrefix)
		if antigravityIntrinsicBase(base) {
			return base
		}
	}
	return name
}
