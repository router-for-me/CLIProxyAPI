package helps

import "strings"

type claudeCodeUserAgent struct {
	version          claudeCLIVersion
	entrypoint       string
	agentSDKVersion  string
	profileUserAgent string
	requestFields    []string
}

// parseClaudeCodeUserAgent separates client identity from opaque request fields.
// Parsing a valid UA does not establish native-client eligibility.
func parseClaudeCodeUserAgent(userAgent string) (claudeCodeUserAgent, bool) {
	for _, char := range userAgent {
		if (char < ' ' && char != '\t') || char == '\x7f' {
			return claudeCodeUserAgent{}, false
		}
	}
	userAgent = strings.TrimSpace(userAgent)
	prefix, comment, ok := strings.Cut(userAgent, "(")
	if !ok || !strings.HasSuffix(comment, ")") || prefix == strings.TrimSpace(prefix) {
		return claudeCodeUserAgent{}, false
	}
	product := strings.TrimSpace(prefix)
	versionText, okVersion := strings.CutPrefix(product, "claude-cli/")
	if !okVersion || !claudePackageVersionPattern.MatchString(versionText) {
		return claudeCodeUserAgent{}, false
	}
	version, okVersion := parseClaudeCLIVersion(product)
	if !okVersion {
		return claudeCodeUserAgent{}, false
	}
	comment = strings.TrimSuffix(comment, ")")
	if strings.ContainsAny(comment, "()") {
		return claudeCodeUserAgent{}, false
	}
	fields := strings.Split(comment, ",")
	if len(fields) < 2 || !strings.EqualFold(fields[0], "external") {
		return claudeCodeUserAgent{}, false
	}
	entrypoint := strings.ToLower(strings.TrimSpace(fields[1]))
	if entrypoint == "" {
		return claudeCodeUserAgent{}, false
	}
	parsed := claudeCodeUserAgent{
		version: version, entrypoint: entrypoint, profileUserAgent: userAgent,
	}
	stableFields := []string{fields[0], fields[1]}
	seen := make(map[string]struct{})
	for _, field := range fields[2:] {
		name, value, okField := strings.Cut(strings.TrimSpace(field), "/")
		if !okField || !validClaudeCodeUserAgentFieldName(name) || strings.TrimSpace(value) == "" {
			return claudeCodeUserAgent{}, false
		}
		name = strings.ToLower(name)
		if _, duplicate := seen[name]; duplicate {
			return claudeCodeUserAgent{}, false
		}
		seen[name] = struct{}{}
		if name == "agent-sdk" {
			if !claudePackageVersionPattern.MatchString(value) {
				return claudeCodeUserAgent{}, false
			}
			parsed.agentSDKVersion = value
			stableFields = append(stableFields, field)
		} else {
			parsed.requestFields = append(parsed.requestFields, field)
		}
	}
	if len(parsed.requestFields) > 0 {
		// Request fields must never become a credential's stored device identity.
		parsed.profileUserAgent = prefix + "(" + strings.Join(stableFields, ",") + ")"
	}
	return parsed, true
}

func validClaudeCodeUserAgentFieldName(name string) bool {
	if name == "" {
		return false
	}
	// Extension names use HTTP token syntax; values remain opaque.
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", char) {
			continue
		}
		return false
	}
	return true
}
