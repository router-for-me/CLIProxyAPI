package to_ir

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
)

// StreamParser maintains state for parsing SSE streams
type StreamParser struct {
	Format Format
}

// ParseSSELine parses a single SSE line (e.g. "data: {...}") into one or more UnifiedEvents
func ParseSSELine(line []byte, opts ParserOptions) ([]ir.UnifiedEvent, error) {
	if len(line) == 0 {
		return nil, nil
	}

	// Basic SSE parsing logic
	if opts.Format == FormatOpenAI {
		return ParseOpenAIChunk(line)
	}
	
	// TODO: Implement other formats as needed
	return nil, fmt.Errorf("unsupported stream format: %s", opts.Format)
}
