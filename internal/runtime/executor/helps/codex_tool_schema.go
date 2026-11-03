package helps

import (
	"net/http"

	"github.com/MoeForge/moego/ojson"
	toolschema "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/tool-schema"
)

// IsCodexUserAgent reports whether headers contain a User-Agent indicating a Codex client.
func IsCodexUserAgent(headers http.Header) bool {
	return toolschema.IsCodexUserAgent(headers)
}

// NormalizeCodexToolIntegerTypes normalizes specified tool parameter declarations
// from number to integer for Codex clients across supported tool formats.
func NormalizeCodexToolIntegerTypes(body []byte, headers http.Header) []byte {
	return toolschema.NormalizeCodexToolIntegerTypes(body, headers)
}

// NormalizeCodexToolIntegerTypesDoc normalizes specified tool parameter declarations
// from number to integer for Codex clients directly on doc in place.
func NormalizeCodexToolIntegerTypesDoc(doc *ojson.Doc, headers http.Header) bool {
	return toolschema.NormalizeCodexToolIntegerTypesDoc(doc, headers)
}

// NormalizeCodexToolSchemas inspects function tools in a Codex request payload
// and simplifies pure constant union combinations (e.g. large oneOf branch sets
// representing enums with descriptions, as emitted by MCP servers) into semantically
// equivalent enum lists, and sanitizes unsupported schema patterns.
// It does not normalize tool integer types because upstream OpenAI Codex models
// strictly validate reserved tool schemas (e.g. collaboration.wait_agent).
func NormalizeCodexToolSchemas(body []byte) []byte {
	return toolschema.NormalizeCodexToolSchemas(body)
}

// NormalizeCodexToolSchemasDoc inspects function tools in doc and simplifies
// pure constant union combinations and sanitizes unsupported schema patterns in place.
func NormalizeCodexToolSchemasDoc(doc *ojson.Doc) bool {
	return toolschema.NormalizeCodexToolSchemasDoc(doc)
}
