package gemini

// WebLoginOptions provides optional behavior for Gemini OAuth login flows.
type WebLoginOptions struct {
	// NoBrowser disables automatic browser opening when true.
	NoBrowser bool
	// Prompt can be used by callers to customize interactive prompts.
	Prompt func(prompt string) (string, error)
}
