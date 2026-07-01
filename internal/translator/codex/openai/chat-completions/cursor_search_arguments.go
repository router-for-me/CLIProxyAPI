package chat_completions

import "github.com/router-for-me/CLIProxyAPI/v7/internal/util"

func shouldNormalizeCursorSearchToolArguments(name string) bool {
	return util.ShouldNormalizeCursorToolArguments(name)
}

func normalizeCursorSearchToolArguments(name, arguments string) string {
	return normalizeCursorSearchToolArgumentsWithWorkspace(name, arguments, "")
}

func normalizeCursorSearchToolArgumentsWithWorkspace(name, arguments, workspaceRoot string) string {
	return util.NormalizeCursorToolArguments(name, arguments, workspaceRoot)
}

func cursorSearchSpecificSourceGlob() string {
	return util.CursorSearchSpecificSourceGlob()
}
