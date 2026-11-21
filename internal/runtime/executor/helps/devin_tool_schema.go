package helps

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// NormalizeDevinToolSchemas folds root-level JSON Schema unions into object
// schemas before sending tools to Devin's Claude-backed models. Codex's
// mcp__cua_repl__js declares a root oneOf that Devin rejects even if the tool
// is not called. Leave ordinary schemas (including nested unions) untouched.
func NormalizeDevinToolSchemas(tools []DevinTool) []DevinTool {
	for i := range tools {
		schema := tools[i].Parameters
		if len(schema) == 0 {
			continue
		}
		root := ojson.ParseBytes(schema)
		if !root.IsObject() || (!root.Get("oneOf").Exists() && !root.Get("anyOf").Exists() && !root.Get("allOf").Exists()) {
			continue
		}
		tools[i].Parameters = util.NormalizeClaudeToolInputSchema(schema)
	}
	return tools
}
