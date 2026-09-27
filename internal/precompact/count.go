package precompact

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// Count estimates input tokens for body in the given client format.
func Count(format sdktranslator.Format, model string, body []byte) (int, error) {
	switch format {
	case sdktranslator.FormatClaude:
		n, err := helps.CountClaudeInputTokens(body)
		return int(n), err
	case sdktranslator.FormatOpenAI:
		enc, err := helps.TokenizerForModel(model)
		if err != nil {
			return 0, err
		}
		n, err := helps.CountOpenAIChatTokens(enc, body)
		return int(n), err
	default:
		return 0, fmt.Errorf("precompact: unsupported format %q", format)
	}
}

// Supported reports whether the client format can be compacted.
func Supported(format sdktranslator.Format) bool {
	return format == sdktranslator.FormatClaude || format == sdktranslator.FormatOpenAI
}
