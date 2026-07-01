package util

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var cursorWorkspacePathPattern = regexp.MustCompile(`(?i)workspace_path="([^"]+)"`)

func ShouldNormalizeCursorToolArguments(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "rg", "grep", "glob", "readfile", "read_file", "subagent":
		return true
	default:
		return false
	}
}

func NormalizeCursorToolArguments(name, arguments, workspaceRoot string) string {
	if !ShouldNormalizeCursorToolArguments(name) || strings.TrimSpace(arguments) == "" {
		return arguments
	}

	args := gjson.Parse(arguments)
	if !args.IsObject() {
		return arguments
	}
	if isCursorGlobTool(name) {
		return normalizeCursorGlobToolArguments(arguments, args, workspaceRoot)
	}
	if isCursorReadFileTool(name) {
		return normalizeCursorReadFileToolArguments(arguments, args, workspaceRoot)
	}
	if isCursorSubagentTool(name) {
		return normalizeCursorSubagentToolArguments(arguments, args)
	}

	updated := []byte(arguments)
	changed := false

	pathValue := strings.TrimSpace(args.Get("path").String())
	globValue := strings.TrimSpace(args.Get("glob").String())
	if pathValue != "" && looksLikeCursorSearchFilePath(pathValue) {
		dir, base := splitCursorSearchPath(pathValue)
		if dir != "" && base != "" {
			dir = normalizeCursorWorkspacePath(dir, workspaceRoot)
			if next, err := sjson.SetBytes(updated, "path", dir); err == nil {
				updated = next
				pathValue = dir
				changed = true
			}
			if next, err := sjson.SetBytes(updated, "glob", base); err == nil {
				updated = next
				globValue = base
				changed = true
			}
		}
	} else {
		normalizedPath := normalizeCursorWorkspacePath(pathValue, workspaceRoot)
		if normalizedPath != pathValue {
			if next, err := sjson.SetBytes(updated, "path", normalizedPath); err == nil {
				updated = next
				pathValue = normalizedPath
				changed = true
			}
		}
		if nextPath, nextGlob, ok := shiftCursorSearchGlobPrefixIntoPath(pathValue, globValue); ok {
			nextPath = normalizeCursorWorkspacePath(nextPath, workspaceRoot)
			if next, err := sjson.SetBytes(updated, "path", nextPath); err == nil {
				updated = next
				pathValue = nextPath
				changed = true
			}
			if next, err := sjson.SetBytes(updated, "glob", nextGlob); err == nil {
				updated = next
				globValue = nextGlob
				changed = true
			}
		}
		if shouldMakeCursorSearchGlobRecursive(globValue) {
			if next, err := sjson.SetBytes(updated, "glob", "**/"+globValue); err == nil {
				updated = next
				changed = true
			}
		} else if isCursorSearchTooBroadGlob(globValue) {
			if next, err := sjson.SetBytes(updated, "glob", ""); err == nil {
				updated = next
				changed = true
			}
		}
	}

	if !changed {
		return arguments
	}
	return string(updated)
}

func ExtractCursorWorkspaceRoot(rawJSON []byte) string {
	if len(rawJSON) == 0 {
		return ""
	}

	var value any
	if err := json.Unmarshal(rawJSON, &value); err != nil {
		return extractCursorWorkspaceRootFromText(string(rawJSON))
	}

	var roots []string
	walkCursorWorkspaceStrings(value, &roots)
	if len(roots) == 0 {
		return ""
	}
	return roots[len(roots)-1]
}

func CursorSearchSpecificSourceGlob() string {
	return "**/*.{js,jsx,ts,tsx,json,md,mdx,css,scss,yml,yaml}"
}

func walkCursorWorkspaceStrings(value any, roots *[]string) {
	switch typed := value.(type) {
	case string:
		if root := extractCursorWorkspaceRootFromText(typed); root != "" {
			*roots = append(*roots, root)
		}
	case []any:
		for _, item := range typed {
			walkCursorWorkspaceStrings(item, roots)
		}
	case map[string]any:
		for _, item := range typed {
			walkCursorWorkspaceStrings(item, roots)
		}
	}
}

func extractCursorWorkspaceRootFromText(text string) string {
	match := cursorWorkspacePathPattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func isCursorGlobTool(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "glob")
}

func isCursorReadFileTool(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return normalized == "readfile" || normalized == "read_file"
}

func isCursorSubagentTool(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "subagent")
}

func normalizeCursorGlobToolArguments(arguments string, args gjson.Result, workspaceRoot string) string {
	updated := []byte(arguments)
	changed := false

	targetDirectoryKey, targetDirectory := firstCursorSearchArg(args, "target_directory", "targetDirectory", "path")
	globPatternKey, globPattern := firstCursorSearchArg(args, "glob_pattern", "globPattern", "pattern", "filePattern", "glob")

	if targetDirectoryKey != "" {
		normalizedTarget := normalizeCursorWorkspacePath(targetDirectory, workspaceRoot)
		if normalizedTarget != targetDirectory {
			if next, err := sjson.SetBytes(updated, targetDirectoryKey, normalizedTarget); err == nil {
				updated = next
				targetDirectory = normalizedTarget
				changed = true
			}
		}
	} else if workspaceRoot != "" {
		if next, err := sjson.SetBytes(updated, "target_directory", workspaceRoot); err == nil {
			updated = next
			targetDirectoryKey = "target_directory"
			targetDirectory = workspaceRoot
			changed = true
		}
	}

	if targetDirectory != "" && globPattern != "" {
		if nextPath, nextGlob, ok := shiftCursorSearchGlobPrefixIntoPath(targetDirectory, globPattern); ok {
			nextPath = normalizeCursorWorkspacePath(nextPath, workspaceRoot)
			if next, err := sjson.SetBytes(updated, targetDirectoryKey, nextPath); err == nil {
				updated = next
				targetDirectory = nextPath
				changed = true
			}
			if globPatternKey != "" {
				if next, err := sjson.SetBytes(updated, globPatternKey, nextGlob); err == nil {
					updated = next
					globPattern = nextGlob
					changed = true
				}
			}
		}
	}

	if shouldMakeCursorSearchGlobRecursive(globPattern) {
		if next, err := sjson.SetBytes(updated, globPatternKey, "**/"+globPattern); err == nil {
			updated = next
			globPattern = "**/" + globPattern
			changed = true
		}
	}
	if isCursorSearchTooBroadGlob(globPattern) {
		if next, err := sjson.SetBytes(updated, globPatternKey, CursorSearchSpecificSourceGlob()); err == nil {
			updated = next
			changed = true
		}
	}

	if !changed {
		return arguments
	}
	return string(updated)
}

func normalizeCursorReadFileToolArguments(arguments string, args gjson.Result, workspaceRoot string) string {
	pathKey, pathValue := firstCursorSearchArg(args, "path", "file_path", "filePath", "filepath", "file")
	if pathKey == "" {
		return arguments
	}
	normalizedPath := normalizeCursorWorkspacePath(pathValue, workspaceRoot)
	if normalizedPath == pathValue {
		return arguments
	}
	updated, err := sjson.SetBytes([]byte(arguments), pathKey, normalizedPath)
	if err != nil {
		return arguments
	}
	return string(updated)
}

func normalizeCursorSubagentToolArguments(arguments string, args gjson.Result) string {
	environment := strings.TrimSpace(args.Get("environment").String())
	if strings.EqualFold(environment, "cloud") || !args.Get("cloud_base_branch").Exists() {
		return arguments
	}

	updated, err := sjson.DeleteBytes([]byte(arguments), "cloud_base_branch")
	if err != nil {
		return arguments
	}
	return string(updated)
}

func firstCursorSearchArg(args gjson.Result, keys ...string) (string, string) {
	for _, key := range keys {
		value := strings.TrimSpace(args.Get(key).String())
		if value != "" {
			return key, value
		}
	}
	return "", ""
}

func looksLikeCursorSearchFilePath(value string) bool {
	value = strings.TrimRight(strings.TrimSpace(value), `/\`)
	if value == "" {
		return false
	}
	lastSlash := strings.LastIndex(value, "/")
	lastBackslash := strings.LastIndex(value, `\`)
	if lastBackslash > lastSlash {
		lastSlash = lastBackslash
	}
	base := value
	if lastSlash >= 0 {
		base = value[lastSlash+1:]
	}
	return strings.Contains(base, ".") && base != "." && base != ".."
}

func splitCursorSearchPath(value string) (string, string) {
	value = strings.TrimRight(strings.TrimSpace(value), `/\`)
	lastSlash := strings.LastIndex(value, "/")
	lastBackslash := strings.LastIndex(value, `\`)
	if lastBackslash > lastSlash {
		lastSlash = lastBackslash
	}
	if lastSlash < 0 || lastSlash == len(value)-1 {
		return "", value
	}
	return value[:lastSlash], value[lastSlash+1:]
}

func shiftCursorSearchGlobPrefixIntoPath(pathValue, globValue string) (string, string, bool) {
	prefix, rest, ok := splitCursorSearchGlobPrefix(globValue)
	if !ok {
		return "", "", false
	}
	return joinCursorSearchPath(pathValue, prefix), rest, true
}

func splitCursorSearchGlobPrefix(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	parts := strings.Split(normalized, "/")
	if len(parts) <= 1 {
		return "", "", false
	}

	firstPattern := -1
	for i, part := range parts {
		if containsCursorGlobMeta(part) {
			firstPattern = i
			break
		}
	}
	if firstPattern == 0 {
		return "", "", false
	}
	if firstPattern < 0 {
		prefix := strings.Join(parts[:len(parts)-1], "/")
		rest := parts[len(parts)-1]
		return prefix, rest, prefix != "" && rest != ""
	}
	prefix := strings.Join(parts[:firstPattern], "/")
	rest := strings.Join(parts[firstPattern:], "/")
	return prefix, rest, prefix != "" && rest != ""
}

func containsCursorGlobMeta(value string) bool {
	return strings.ContainsAny(value, "*?[]{}!")
}

func normalizeCursorWorkspacePath(pathValue, workspaceRoot string) string {
	pathValue = strings.TrimSpace(pathValue)
	workspaceRoot = strings.TrimRight(strings.TrimSpace(workspaceRoot), `/\`)
	if workspaceRoot == "" {
		return pathValue
	}
	if pathValue == "" || pathValue == "." || pathValue == "./" || pathValue == `.\` {
		return workspaceRoot
	}
	if isCursorSearchAbsolutePath(pathValue) || strings.HasPrefix(pathValue, "~") {
		return pathValue
	}
	return joinCursorSearchPath(workspaceRoot, pathValue)
}

func joinCursorSearchPath(base, child string) string {
	base = strings.TrimRight(strings.TrimSpace(base), `/\`)
	child = strings.Trim(strings.TrimSpace(child), `/\`)
	if base == "" || child == "" {
		if base != "" {
			return base
		}
		return child
	}
	if isCursorSearchAbsolutePath(child) {
		return strings.ReplaceAll(child, "/", `\`)
	}
	if cursorPathHasSuffix(base, child) {
		return base
	}
	sep := "/"
	if strings.Contains(base, `\`) && !strings.Contains(base, "/") {
		sep = `\`
		child = strings.ReplaceAll(child, "/", `\`)
	}
	return base + sep + child
}

func isCursorSearchAbsolutePath(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, `\`) ||
		(len(value) >= 2 && value[1] == ':')
}

func cursorPathHasSuffix(base, suffix string) bool {
	base = normalizeCursorSearchPathForCompare(base)
	suffix = normalizeCursorSearchPathForCompare(suffix)
	if base == "" || suffix == "" {
		return false
	}
	return base == suffix || strings.HasSuffix(base, "/"+suffix)
}

func normalizeCursorSearchPathForCompare(value string) string {
	value = strings.ReplaceAll(strings.Trim(value, `/\`), `\`, "/")
	value = strings.Trim(value, "/")
	return strings.ToLower(value)
}

func shouldMakeCursorSearchGlobRecursive(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return !strings.Contains(value, "**") &&
		!strings.Contains(value, "/") &&
		!strings.Contains(value, `\`)
}

func isCursorSearchTooBroadGlob(value string) bool {
	normalized := strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	normalized = strings.Trim(normalized, "/")
	switch normalized {
	case "*", "**", "**/*", "**/**":
		return true
	default:
		return false
	}
}
