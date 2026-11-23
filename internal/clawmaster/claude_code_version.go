package clawmaster

import (
	"fmt"
	"regexp"
	"strconv"
	"sync"
)

// The Claude Code version the pool's own calls to Anthropic present (the usage poll): the newest the
// backend has served, so they say what the pool's clients really run and never fall behind them.
// Before any client has been served it is fallbackClaudeCodeVersion, measured 2026-11-23.
var (
	claudeCodeUserAgentPattern = regexp.MustCompile(`^claude-cli/(\d+)\.(\d+)\.(\d+) \(external, [^)]+\)$`)
	fallbackClaudeCodeVersion  = claudeCodeVersion{2, 1, 330}

	newestClaudeCodeMu sync.Mutex
	newestClaudeCode   claudeCodeVersion
)

type claudeCodeVersion struct{ major, minor, patch int }

func (v claudeCodeVersion) after(o claudeCodeVersion) bool {
	if v.major != o.major {
		return v.major > o.major
	}
	if v.minor != o.minor {
		return v.minor > o.minor
	}
	return v.patch > o.patch
}

// noteClaudeCodeUserAgent records a served request's User-Agent when it is Claude Code's own form on
// the same major line as the fallback, keeping the newest version seen.
func noteClaudeCodeUserAgent(userAgent string) {
	match := claudeCodeUserAgentPattern.FindStringSubmatch(userAgent)
	if match == nil {
		return
	}
	var version claudeCodeVersion
	for i, part := range []*int{&version.major, &version.minor, &version.patch} {
		n, err := strconv.Atoi(match[i+1])
		if err != nil || n > 9999 {
			return
		}
		*part = n
	}
	if version.major != fallbackClaudeCodeVersion.major {
		return
	}
	newestClaudeCodeMu.Lock()
	if version.after(newestClaudeCode) {
		newestClaudeCode = version
	}
	newestClaudeCodeMu.Unlock()
}

// claudeCodeUserAgent is the User-Agent of the pool's own calls: Claude Code's interactive form at the
// newest version served, or the fallback.
func claudeCodeUserAgent() string {
	newestClaudeCodeMu.Lock()
	version := newestClaudeCode
	newestClaudeCodeMu.Unlock()
	if !version.after(fallbackClaudeCodeVersion) {
		version = fallbackClaudeCodeVersion
	}
	return fmt.Sprintf("claude-cli/%d.%d.%d (external, cli)", version.major, version.minor, version.patch)
}
