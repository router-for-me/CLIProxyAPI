package claude

import (
	"github.com/MoeForge/moego/ojson"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

const geminiClaudeUnsupportedSignatureTarget = "unsupported"

// geminiClaudeCarrierSignatures binds assistant signature carriers to native
// parts without allowing a signature to cross another semantic block or message.
func geminiClaudeCarrierSignatures(blocks []ojson.Result) map[int]string {
	kinds := make([]string, len(blocks))
	transparent := make([]bool, len(blocks))
	previous := make([]int, len(blocks))
	next := make([]int, len(blocks))
	for i, block := range blocks {
		switch block.Get("type").String() {
		case "text":
			if block.Get("text").String() != "" {
				kinds[i] = translatorcommon.GeminiClaudeCarrierText
			} else {
				transparent[i] = true
			}
		case "tool_use":
			if block.Get("input").IsObject() {
				kinds[i] = translatorcommon.GeminiClaudeCarrierFunction
			}
		case "thinking":
			// Omitted reasoning still forms a semantic boundary for its signature.
			if block.Get("thinking").String() != "" {
				kinds[i] = translatorcommon.GeminiClaudeCarrierText
			} else {
				transparent[i] = true
			}
		}
	}
	adjacent := -1
	for i := range blocks {
		previous[i] = adjacent
		if kinds[i] != "" {
			adjacent = i
		} else if !transparent[i] {
			adjacent = -1
		}
	}
	adjacent = -1
	for i := len(blocks) - 1; i >= 0; i-- {
		next[i] = adjacent
		if kinds[i] != "" {
			adjacent = i
		} else if !transparent[i] {
			adjacent = -1
		}
	}

	var signatures map[int]string
	sources := make(map[int]int)
	for i, block := range blocks {
		if block.Get("type").String() != "thinking" || block.Get("thinking").String() != "" {
			continue
		}
		raw, direction, targetKind, marked, ok := translatorcommon.DecodeGeminiClaudeCarrierSignature(block.Get("signature").String())
		if !ok || raw == "" {
			continue
		}
		target := next[i]
		if marked {
			switch direction {
			case translatorcommon.GeminiClaudeCarrierPrevious:
				target = previous[i]
			case translatorcommon.GeminiClaudeCarrierStandalone:
				target = i
			}
			if target < 0 || (target != i && targetKind != translatorcommon.GeminiClaudeCarrierAny && targetKind != kinds[target]) {
				continue
			}
		} else {
			if target < 0 {
				target = previous[i]
			}
			if target < 0 {
				target = i
			}
		}
		blockKind := sigcompat.SignatureBlockKindGeminiModelPart
		if kinds[target] == translatorcommon.GeminiClaudeCarrierFunction {
			blockKind = sigcompat.SignatureBlockKindGeminiFunctionCall
		}
		signature, compatible := sigcompat.CompatibleSignatureForProviderBlock(sigcompat.SignatureProviderGemini, raw, blockKind)
		if !compatible || sigcompat.IsGeminiThoughtSignatureBypass(signature) {
			continue
		}
		if signatures == nil {
			signatures = make(map[int]string)
		}
		if oldSource, exists := sources[target]; exists && signatures[target] != signature {
			// Keep an earlier detached signature rather than silently replacing it.
			signatures[oldSource] = signatures[target]
		}
		signatures[target] = signature
		sources[target] = i
	}
	return signatures
}
