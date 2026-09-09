package precompact

import (
	"context"
	"strings"
)

// Summarizer produces a compact summary of a transcript. Implementations call
// the auxiliary model; previous is a prior summary to extend (may be empty).
type Summarizer interface {
	Summarize(ctx context.Context, model, previous, transcript string) (string, error)
}

// SummaryInstruction is the fixed prompt used for the auxiliary call.
const SummaryInstruction = `You are compacting a long coding-assistant conversation so it fits a smaller context window. Write a dense summary that lets the assistant continue seamlessly. Preserve, in this order:
1. The user's goal and any constraints they stated.
2. Files touched (absolute paths) and what changed in each.
3. Commands run and their results, including errors verbatim where short.
4. Decisions made and why.
5. Open items and the immediate next step.
Use plain prose and short lists. Do not invent details. Do not address the user. Output only the summary.`

// BuildPrompt joins a previous summary and a new transcript into one user message.
func BuildPrompt(previous, transcript string) string {
	var b strings.Builder
	if strings.TrimSpace(previous) != "" {
		b.WriteString("Previous summary (extend it, do not drop facts):\n\n")
		b.WriteString(previous)
		b.WriteString("\n\n---\n\nNew conversation since then:\n\n")
	} else {
		b.WriteString("Conversation:\n\n")
	}
	b.WriteString(transcript)
	return b.String()
}
