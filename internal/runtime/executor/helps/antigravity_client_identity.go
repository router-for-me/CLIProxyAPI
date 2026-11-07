package helps

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// NormalizeAntigravityGeminiSDKIdentityDoc adapts the SDK's standalone default
// identity banner to a provider-neutral description while retaining its SDK
// attribution. Only this exact system part changes: user text, custom system
// instructions, and Anthropic-backed models are left untouched. Run before
// payload configuration so an explicit user rule remains authoritative.
func NormalizeAntigravityGeminiSDKIdentityDoc(model string, doc *ojson.Doc) {
	if doc == nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini-") {
		return
	}
	const banner = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	const neutral = "You are an AI coding assistant accessed through the Claude Agent SDK."
	for i, part := range doc.Get("request.systemInstruction.parts").Array() {
		if part.Get("text").String() == banner && !part.Get("thought").Bool() {
			_ = doc.Set(fmt.Sprintf("request.systemInstruction.parts.%d.text", i), neutral)
		}
	}
}
