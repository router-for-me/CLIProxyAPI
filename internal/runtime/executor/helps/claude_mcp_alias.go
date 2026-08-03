package helps

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"strings"
)

var claudeMCPBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

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

// ClaudeMCPToolAlias derives a Claude Code-style MCP tool name. Aliases from
// one caller share a virtual server component. The tool component combines a
// stable keyed ID with a truncated semantic suffix so the model can distinguish
// tools by name while the request-local symbol table restores the exact original.
// A higher attempt changes the stable ID when a collision must be avoided.
func ClaudeMCPToolAlias(secret, original string, attempt uint32) string {
	serverDigest := claudeMCPAliasDigest(secret, "server", "", 0)
	toolDigest := claudeMCPAliasDigest(secret, "tool", original, attempt)
	server := claudeMCPBase32.EncodeToString(serverDigest[:])[:12]
	toolID := claudeMCPBase32.EncodeToString(toolDigest[:])[:12]
	semantic := claudeMCPToolSemanticSuffix(original, 32)
	return "mcp__" + server + "__" + toolID + "_" + semantic
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

func claudeMCPAliasDigest(secret, purpose, original string, attempt uint32) [sha256.Size]byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("cpa-claude-mcp-alias-v2\x00"))
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(original))
	var counter [4]byte
	binary.BigEndian.PutUint32(counter[:], attempt)
	_, _ = mac.Write(counter[:])
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}
