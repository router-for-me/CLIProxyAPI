package helps

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

// Opencode CLI identity synthesis.
//
// The OpenCode Zen Go upstream (opencode.ai) routes requests through
// Cloudflare and requires CLI-shaped requests: without the x-opencode-*
// identity headers the Console Go tier rejects with "MissingSessionID —
// request is missing x-opencode-session". OmniRoute solves this by
// synthesizing the identity of the opencode CLI for every request; this
// file ports that behavior:
//
//	User-Agent:         opencode
//	x-opencode-client:  desktop
//	x-opencode-project: global
//	x-opencode-request: fresh random UUID per request
//	x-opencode-session: deterministic conversation fingerprint
//	                    (sha256(model|sys|user0|tools)[:16]) so upstream
//	                    prompt caching hits within a conversation, falling
//	                    back to a random UUID when nothing hashable exists.
//
// Client-supplied x-opencode-* headers (forwarded via the row's custom
// headers) always win: stamps only fill gaps.

const (
	// OpenCodeDefaultUserAgent is the CLI identity OpenCode's free tier
	// expects (generic client UAs get 429'd from datacenter IPs).
	OpenCodeDefaultUserAgent = "opencode"
	// OpenCodeDefaultClient / OpenCodeDefaultProject mirror OmniRoute's
	// cliDefaults for the desktop CLI.
	OpenCodeDefaultClient  = "desktop"
	OpenCodeDefaultProject = "global"
)

// opencodeHeaderNames are the wire header names stamped on every request.
const (
	hdrOpenCodeSession = "x-opencode-session"
	hdrOpenCodeRequest = "x-opencode-request"
	hdrOpenCodeClient  = "x-opencode-client"
	hdrOpenCodeProject = "x-opencode-project"
)

// StampsOpencodeCLIHeaders reports whether an executor invocation targets
// the opencode-go channel, mirroring the executor's Identifier.
func StampsOpencodeCLIHeaders(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "opencode-go")
}

// StampsOpencodeCLIHeadersFromCtx reports whether the request context
// carries the opencode-go executor marker set by OpenCodeGoExecutor.
// Executed via the shared executors, the real channel identity lives only
// on the dispatching wrapper, so the wrapper communicates it via context.
func StampsOpencodeCLIHeadersFromCtx(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(opencodeGoMarkerKey{}).(bool)
	return ok && v
}

// opencodeGoMarkerKey is the context key OpenCodeGoExecutor uses to mark
// requests destined for the opencode-go channel.
type opencodeGoMarkerKey struct{}

// WithOpencodeGoMarker returns a child context marked as an opencode-go
// request so shared executors know to stamp the CLI identity headers.
func WithOpencodeGoMarker(ctx context.Context) context.Context {
	return context.WithValue(ctx, opencodeGoMarkerKey{}, true)
}

// StampOpencodeCLIHeaders mutates attrs in place, adding the synthesized
// CLI identity headers as "header:*" attributes the shared executors
// already apply via util.ApplyCustomHeadersFromAttrs. Existing header:*
// attributes win (client-supplied identity beats synthesis), except
// User-Agent: a present-but-generic UA is replaced with the CLI identity,
// matching OmniRoute's "opencode.ai's free tier rejects generic client
// UAs" behavior. A UA that already looks like the opencode CLI
// (opencode-cli/...) is preserved.
//
// attrs is the auth's attribute map; the caller must pass a writable copy
// (see OpenCodeGoExecutor).
func StampOpencodeCLIHeaders(attrs map[string]string, model string, payload []byte) {
	if attrs == nil {
		return
	}
	// Client-supplied x-opencode-* always wins.
	if attrs["header:"+hdrOpenCodeSession] == "" {
		attrs["header:"+hdrOpenCodeSession] = OpencodeSessionFingerprint(model, payload)
	}
	if attrs["header:"+hdrOpenCodeRequest] == "" {
		attrs["header:"+hdrOpenCodeRequest] = randomUUID()
	}
	if attrs["header:"+hdrOpenCodeClient] == "" {
		attrs["header:"+hdrOpenCodeClient] = OpenCodeDefaultClient
	}
	if attrs["header:"+hdrOpenCodeProject] == "" {
		attrs["header:"+hdrOpenCodeProject] = OpenCodeDefaultProject
	}
	ua := attrs["header:User-Agent"]
	if ua == "" {
		ua = attrs["header:user-agent"]
	}
	switch {
	case ua == "":
		attrs["header:User-Agent"] = OpenCodeDefaultUserAgent
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(ua)), "opencode-cli/"):
		// Real CLI identity — preserved.
	default:
		// Generic client UA (curl, python-requests, …): replaced. The
		// upstream 429s datacenter traffic without the CLI identity.
		attrs["header:User-Agent"] = OpenCodeDefaultUserAgent
	}
}

// OpencodeSessionFingerprint derives the deterministic conversation
// session id: sha256("model:<m>|sys:<h8>|user0:<h8>|tools:<h8>")[:16]
// following OmniRoute's sessionManager.generateSessionId. Empty when the
// payload has nothing hashable (callers then rely on the caller to fall
// back; StampOpencodeCLIHeaders falls back to a random UUID).
func OpencodeSessionFingerprint(model string, payload []byte) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	}
	if len(payload) == 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	if model != "" {
		parts = append(parts, "model:"+model)
	}
	if sys := openCodeSystemPrompt(payload); sys != "" {
		parts = append(parts, "sys:"+hashShortOpenCode(sys))
	}
	if first := openCodeFirstUserMessage(payload); first != "" {
		parts = append(parts, "user0:"+hashShortOpenCode(first))
	}
	if tools := openCodeToolNamesSignature(payload); tools != "" {
		parts = append(parts, "tools:"+hashShortOpenCode(tools))
	}
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])[:16]
}

// openCodeSystemPrompt mirrors OmniRoute: Claude-style top-level "system"
// first, else the first system/developer message in messages.
func openCodeSystemPrompt(payload []byte) string {
	if v := gjson.GetBytes(payload, "system"); v.Exists() {
		return openCodeStringify(v)
	}
	var first string
	gjson.GetBytes(payload, "messages").ForEach(func(_, m gjson.Result) bool {
		role := m.Get("role").String()
		if role == "system" || role == "developer" {
			first = openCodeStringify(m.Get("content"))
			return false
		}
		return true
	})
	return first
}

// openCodeFirstUserMessage returns the first user message's content,
// stringified.
func openCodeFirstUserMessage(payload []byte) string {
	var first string
	gjson.GetBytes(payload, "messages").ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() == "user" {
			first = openCodeStringify(m.Get("content"))
			return false
		}
		return true
	})
	return first
}

// openCodeToolNamesSignature joins the sorted tool names with commas.
func openCodeToolNamesSignature(payload []byte) string {
	names := make([]string, 0, 8)
	gjson.GetBytes(payload, "tools").ForEach(func(_, t gjson.Result) bool {
		name := t.Get("name").String()
		if name == "" {
			name = t.Get("function.name").String()
		}
		if name != "" {
			names = append(names, name)
		}
		return true
	})
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// openCodeStringify renders a gjson value as its raw string when possible,
// else its JSON serialization.
func openCodeStringify(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	raw := v.Raw
	if raw == "" {
		return ""
	}
	return raw
}

func hashShortOpenCode(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:8]
}

// randomUUID produces a RFC 4122 v4 UUID for the per-request identity
// headers. crypto/rand failure yields a sha256-of-time fallback rather
// than an empty header.
func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		sum := sha256.Sum256([]byte(time.Now().Format(time.RFC3339Nano)))
		return hex.EncodeToString(sum[:])[:32]
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// StripThinkingSuffixForOpencode parses the operator-facing suffixed alias
// down to the base model id so the session fingerprint stays
// conversation-stable.
func StripThinkingSuffixForOpencode(model string) string {
	return thinking.ParseSuffix(model).ModelName
}
