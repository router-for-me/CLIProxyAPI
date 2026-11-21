package responses

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Claude models trained on Claude Code's Bash tool (which has no working-directory
// argument) prefix Codex shell calls with "cd /abs/path && ...". Codex only
// summarizes a command (Read/Search/List) when it parses as a plain command, so
// every such call is shown as a raw "cd ... && ..." line. Moving the leading
// absolute cd into the tool's workdir argument keeps the behaviour: the rest of
// the command runs in that directory either way, and Codex rejects a missing
// workdir before anything runs, like a failing cd short-circuits "&&".
var leadingCDPattern = regexp.MustCompile(`^\s*cd\s+('[^']+'|"[^"$` + "`" + `\\]+"|[A-Za-z0-9_./+@:,=-]+)\s*&&\s*`)

// shellCommandField maps Codex shell tools to the argument holding the command string.
var shellCommandField = map[string]string{
	"exec_command":  "cmd",
	"shell_command": "command",
}

func splitLeadingCD(command string) (dir, rest string, ok bool) {
	rest = command
	for {
		m := leadingCDPattern.FindStringSubmatch(rest)
		if m == nil {
			break
		}
		path := m[1]
		if len(path) >= 2 && (path[0] == '\'' || path[0] == '"') {
			path = path[1 : len(path)-1]
		}
		if !strings.HasPrefix(path, "/") {
			break
		}
		remaining := rest[len(m[0]):]
		if strings.TrimSpace(remaining) == "" {
			break
		}
		dir, rest, ok = path, remaining, true
	}
	return dir, rest, ok
}

// moveLeadingCDToWorkdir rewrites the arguments of a Codex shell tool call so that a
// leading "cd /abs && " becomes the workdir argument. Anything it cannot parse is
// returned unchanged.
func moveLeadingCDToWorkdir(toolName, args string) string {
	field, isShell := shellCommandField[toolName]
	if !isShell || !strings.Contains(args, "cd") {
		return args
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &fields); err != nil {
		return args
	}
	var command string
	if err := json.Unmarshal(fields[field], &command); err != nil {
		return args
	}
	dir, rest, ok := splitLeadingCD(command)
	if !ok {
		return args
	}
	encode := func(v string) json.RawMessage {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n"))
	}
	fields[field] = encode(rest)
	fields["workdir"] = encode(dir)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fields); err != nil {
		return args
	}
	return string(bytes.TrimRight(out.Bytes(), "\n"))
}

// clientToolName returns the Responses-side tool name (without namespace) for a Claude tool name.
func (st *claudeToResponsesState) clientToolName(claudeName string) string {
	claudeName = strings.TrimSpace(claudeName)
	if claudeName == "" {
		return ""
	}
	identity := st.ToolNames.identity(claudeName)
	if descriptor, ok := st.ToolWinners[identity]; ok && !descriptor.direct {
		return descriptor.childName
	}
	return identity
}

func (st *claudeToResponsesState) normalizeShellArgs(claudeName, args string) string {
	return moveLeadingCDToWorkdir(st.clientToolName(claudeName), args)
}

func (st *claudeToResponsesState) isShellCommandTool(claudeName string) bool {
	_, ok := shellCommandField[st.clientToolName(claudeName)]
	return ok
}
