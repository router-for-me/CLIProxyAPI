package session

import "strings"

// Confidence classifies how much a session identifier can be trusted to
// represent one client conversation.
const (
	// ConfidenceHigh marks identifiers a client sends explicitly for one conversation.
	ConfidenceHigh = "high"
	// ConfidenceMedium marks identifiers that usually track a conversation but are
	// documented for another purpose, or that CPA inferred from stable request context.
	ConfidenceMedium = "medium"
	// ConfidenceLow marks identifiers that only approximate a conversation.
	ConfidenceLow = "low"
)

// Scope records what a session identifier actually addresses.
const (
	// ScopeSession addresses one client conversation.
	ScopeSession = "session"
	// ScopeThread addresses one thread or sub-agent inside a conversation.
	ScopeThread = "thread"
	// ScopeUser addresses an end user rather than a conversation.
	ScopeUser = "user"
	// ScopeTransport addresses one long-lived downstream connection.
	ScopeTransport = "transport"
)

// Source records which signal produced a session identifier.
const (
	// SourceGatewayHeader is the unified X-Gateway-Session-Id protocol.
	SourceGatewayHeader = "gateway_header"
	// SourceClientNativeHeader is a session header a coding agent sends natively.
	SourceClientNativeHeader = "client_native_header"
	// SourceMetadataUserID is a Claude Code session parsed out of metadata.user_id.
	SourceMetadataUserID = "metadata_user_id"
	// SourceClientRequestID is the x-client-request-id header, used only when a
	// request carries no other explicit root identifier.
	//
	// Live captures show the header tracks the current thread, not the root session:
	// a Codex sub-agent sends its own thread identifier there while session-id stays
	// on the root. Collecting it alongside a real root identifier would add one
	// throwaway alias per sub-agent to the root alias group, so it is a last resort.
	SourceClientRequestID = "client_request_id"
	// SourceBodySession is an explicit session field in the request body.
	SourceBodySession = "body_session"
	// SourcePromptCacheKey is the Responses prompt_cache_key field.
	SourcePromptCacheKey = "prompt_cache_key"
	// SourceBodyConversation is a Responses conversation reference.
	SourceBodyConversation = "body_conversation"
	// SourceLegacyMetadataUser is a bare metadata.user_id value.
	SourceLegacyMetadataUser = "legacy_metadata_user"
	// SourceExecutionSession is a downstream execution session owned by CPA.
	SourceExecutionSession = "execution_session"
	// SourceDerived is the stable identity derived from request context.
	SourceDerived = "derived"
	// SourceMessageHash is the legacy first-message hash fallback.
	SourceMessageHash = "message_hash"
)

// Identity describes one extracted client session, its provenance, and the
// related identifiers that address the same logical conversation.
// The representative ID is stored in ID, and any additional explicit
// session aliases are stored in Aliases. Routing operations must inspect
// all IDs returned by IDs().
type Identity struct {
	// ID is the representative namespaced session identifier used for display and compatibility.
	ID string
	// Aliases lists other identifiers known to address the same session.
	Aliases []string
	// Source names the signal the representative identifier came from.
	Source string
	// Confidence reports how strongly the representative identifier represents one conversation.
	Confidence string
	// Scope reports what the representative identifier addresses.
	Scope string
	// ClientType names the detected downstream client, when it is unambiguous.
	ClientType string
	// ThreadID carries the current thread or sub-agent identifier, for observability.
	ThreadID string
	// ParentThreadID carries the parent thread identifier, for observability.
	ParentThreadID string
	// RequestKind names what the client is doing, when it says so: Codex reports
	// turn, compact, review, title, prewarm and similar, and Claude Code reports
	// background work. It lets a session timeline separate real conversation turns
	// from housekeeping requests. Observability only.
	RequestKind string
	// ThreadSource names where the thread came from, when the client says so:
	// user, subagent, fork and similar. Observability only.
	ThreadSource string
	// TurnID identifies one client turn, which may span several upstream requests.
	// It changes every turn and is never a session identifier. Observability only.
	TurnID string
	// ClientProvided reports whether the client sent the representative identifier itself.
	ClientProvided bool
}

// IsZero reports whether no session identifier could be extracted.
func (i Identity) IsZero() bool {
	return i.ID == ""
}

// FallbackID returns the first alias, if present, for compatibility.
func (i Identity) FallbackID() string {
	if len(i.Aliases) == 0 {
		return ""
	}
	return i.Aliases[0]
}

// IDs returns the deduplicated representative identifier and all aliases.
func (i Identity) IDs() []string {
	id := strings.TrimSpace(i.ID)
	if id == "" {
		return nil
	}
	out := make([]string, 0, 1+len(i.Aliases))
	seen := make(map[string]struct{}, 1+len(i.Aliases))
	out = append(out, id)
	seen[id] = struct{}{}
	for _, alias := range i.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		out = append(out, alias)
	}
	return out
}
