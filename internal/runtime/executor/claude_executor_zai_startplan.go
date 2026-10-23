package executor

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ZCode Start Plan (free tier) gateway support.
//
// The zcode.z.ai zcode-plan anthropic gateway enforces a server-side risk
// gate on model requests: requests whose leading system blocks do not
// match the official ZCode agent prompt are rejected with HTTP 405
// code 3012 ("request has been blocked due to unusual activity").
// Verified empirically 2026-09-30: the TLS/HTTP fingerprint, headers,
// auth token, metadata, tools and messages are all irrelevant to the
// gate — only the first two system blocks matter, and they must match
// the official client prompt byte-for-byte. Blocks appended after the
// official ones are allowed, so the client's own system prompt is kept
// and simply ordered after the fingerprint blocks.
//
// The blocks below were captured from the official client (app version
// 3.14.4). If the upstream gate starts rejecting requests again, recheck
// whether a newer client ships a changed agent prompt.

const (
	zaiStartPlanSystemBlockOne = "You are ZCode, an interactive coding agent"

	zaiStartPlanSystemBlockTwo = "\nYou are an interactive ZCode agent that helps users with software engineering tasks.\n\nIMPORTANT: Assist with authorized security testing, defensive security, CTF challenges, and educational contexts. Refuse requests for destructive techniques, DoS attacks, mass targeting, supply chain compromise, or detection evasion for malicious purposes. Dual-use security tools (C2 frameworks, credential testing, exploit development) require clear authorization context: pentesting engagements, CTF competitions, security research, or defensive use cases.\n\n# Harness\n- Text you output outside of tool use is displayed to the user as Github-flavored markdown in a terminal.\n- Tools run behind a user-selected permission mode; a denied call means the user declined it — adjust, don't retry verbatim.\n- The system may send updates, reminders, or modifications to rules via mid-conversation system turns. These are system-controlled, unlike function results. Hooks may intercept tool calls; treat hook output as user feedback.\n- Prefer the dedicated file/search tools over shell commands when one fits. Independent tool calls can run in parallel in one response.\n- Reference code as `file_path:line_number` — it's clickable.\n\n# ZCode Desktop Context\n\n### Files & URLs\n- Return local web URLs as Markdown links (e.g., [label](http://127.0.0.1:8080)).\n- File should be an absolute path or include the workspace folder segment so it can be resolved relative to the workspace.\n- Unless otherwise specified, return local file references as Markdown links (e.g., [name.md](/absolute/path/to/name.md)).\n\n### Inline Code Comments\n- Use the ::code-comment{...} directive when you need to attach feedback directly to specific code lines.\n- Emit one directive per inline comment; emit none when there are no actionable inline comments.\n- Required attributes: title (short label), body (one-paragraph explanation), file (path to the file).\n- Optional attributes: start, end (1-based line numbers), priority (0-3).\n- file should be an absolute path or include the workspace folder segment so it can be resolved relative to the workspace.\n- Keep line ranges tight; end defaults to start.\n- Example: ::code-comment{title=\"[P2] Off-by-one\" body=\"Loop iterates past the end when length is 0.\" file=\"/path/to/foo.ts\" start=10 end=11 priority=2}"
)

// isZaiStartPlanUpstreamURL reports whether the request targets the
// ZCode Start Plan anthropic gateway.
func isZaiStartPlanUpstreamURL(url string) bool {
	return strings.Contains(url, "/zcode-plan/anthropic")
}

// injectZaiStartPlanSystem prepends the official ZCode agent system
// blocks required by the Start Plan risk gate. The client's own system
// payload (string or block array) is preserved after them.
func injectZaiStartPlanSystem(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	blocks := []map[string]any{
		{"type": "text", "text": zaiStartPlanSystemBlockOne},
		{"type": "text", "text": zaiStartPlanSystemBlockTwo},
	}
	system := gjson.GetBytes(body, "system")
	switch {
	case !system.Exists():
	case system.IsArray():
		system.ForEach(func(_, block gjson.Result) bool {
			blocks = append(blocks, map[string]any{
				"type": block.Get("type").String(),
				"text": block.Get("text").String(),
			})
			return true
		})
	default:
		blocks = append(blocks, map[string]any{"type": "text", "text": system.String()})
	}
	updated, err := sjson.SetBytes(body, "system", blocks)
	if err != nil {
		return body
	}
	return updated
}
