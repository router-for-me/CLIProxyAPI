package helps

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"strings"

	log "github.com/sirupsen/logrus"
)

// IsClaudeMCPToolName reports whether name follows Claude Code's MCP tool
// convention and contains only characters accepted by Anthropic tool names.
func IsClaudeMCPToolName(name string) bool {
	if len(name) == 0 || len(name) > 64 || !strings.HasPrefix(name, "mcp__") {
		return false
	}
	rest := strings.TrimPrefix(name, "mcp__")
	separator := strings.Index(rest, "__")
	if separator <= 0 || separator+2 >= len(rest) {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

// ClaudeMCPAliasWordCount is the BIP-39 English dictionary size used for the
// virtual server pair and the one-word tool ID.
func ClaudeMCPAliasWordCount() int {
	return len(claudeMCPAliasEnglishWords)
}

// ClaudeMCPToolAlias derives a Claude Code-style MCP tool name. Aliases from
// one caller share a virtual server component. The tool component combines a
// stable keyed ID with a truncated semantic suffix so the model can distinguish
// tools by name while the request-local symbol table restores the exact original.
// A higher attempt linearly probes the next word when a collision must be avoided.
// Server and tool IDs use BIP-39 English words so weak models are less likely
// to drift high-entropy Base32 fragments.
func ClaudeMCPToolAlias(secret, original string, attempt uint32) string {
	toolDigest := claudeMCPAliasDigest(secret, "tool", original)
	return claudeMCPAliasFor(
		claudeMCPAliasServerComponent(secret),
		claudeMCPAliasWord(toolDigest[:], 0, attempt),
		original,
	)
}

// AllocateClaudeMCPToolAlias picks an alias that is not already reserved.
// Attempts are capped at the wordlist size so names that sanitize to the same
// suffix cannot spin forever. ok is false only when every one-word tool ID for
// this semantic is already reserved.
func AllocateClaudeMCPToolAlias(secret, original string, reserved map[string]bool) (string, bool) {
	words := claudeMCPAliasEnglishWords
	totalWords := len(words)
	if totalWords == 0 {
		log.Error("claude oauth mcp alias: embedded BIP-39 wordlist is empty, tool aliasing is disabled")
		return "", false
	}
	server := claudeMCPAliasServerComponent(secret)
	toolDigest := claudeMCPAliasDigest(secret, "tool", original)
	baseIndex := int(binary.BigEndian.Uint16(toolDigest[0:2])) % totalWords

	for attempt := 0; attempt < totalWords; attempt++ {
		alias := claudeMCPAliasFor(server, words[(baseIndex+attempt)%totalWords], original)
		if reserved != nil && reserved[alias] {
			continue
		}
		return alias, true
	}
	return "", false
}

// claudeMCPAliasFor assembles the final alias for one server/tool word pair.
// Both the single-shot and the allocating entry point must build names here so
// the two cannot drift apart.
func claudeMCPAliasFor(server, toolID, original string) string {
	prefix := "mcp__" + server + "__" + toolID + "_"
	maxSemanticLen := 64 - len(prefix)
	if maxSemanticLen < 1 {
		maxSemanticLen = 1
	}
	return prefix + claudeMCPToolSemanticSuffix(original, maxSemanticLen)
}

// claudeMCPAliasServerComponent derives the caller-stable two-word virtual
// server shared by every alias generated for one credential.
func claudeMCPAliasServerComponent(secret string) string {
	serverDigest := claudeMCPAliasDigest(secret, "server", "")
	return claudeMCPAliasWord(serverDigest[:], 0, 0) + "_" + claudeMCPAliasWord(serverDigest[:], 2, 0)
}

func claudeMCPAliasWord(digest []byte, offset int, attempt uint32) string {
	words := claudeMCPAliasEnglishWords
	if len(words) == 0 || offset < 0 || offset+2 > len(digest) {
		return "tool"
	}
	base := int(binary.BigEndian.Uint16(digest[offset : offset+2]))
	return words[(base+int(attempt))%len(words)]
}

func claudeMCPToolSemanticSuffix(original string, maxLength int) string {
	var semantic strings.Builder
	semantic.Grow(min(len(original), maxLength))
	pendingSeparator := false
	for _, char := range original {
		valid := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-'
		if !valid {
			pendingSeparator = semantic.Len() > 0
			continue
		}
		if pendingSeparator && semantic.Len()+1 < maxLength {
			semantic.WriteByte('_')
		}
		pendingSeparator = false
		if semantic.Len() >= maxLength {
			break
		}
		semantic.WriteRune(char)
	}
	result := strings.Trim(semantic.String(), "_-")
	if result == "" {
		return "tool"
	}
	return result
}

func claudeMCPAliasDigest(secret, purpose, original string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("cpa-claude-mcp-alias-v2\x00"))
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(original))
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}
