package helps

import (
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

// CheckDevinUserTurns applies the shared user-turn policy to the parsed prompts.
// Devin can only receive inline images, so a user prompt that dropped media it
// cannot fetch or carry (a remote uri, audio, video, a document) is refused with
// an unsupported content part error when its whole user turn has nothing else to
// send. A tool result or real text in the same turn keeps the turn alive; the
// emptied prompt is then removed so that no empty user prompt, and no placeholder
// standing in for the media, reaches upstream.
func CheckDevinUserTurns(prompts []DevinPrompt) ([]DevinPrompt, error) {
	var run translatorcommon.UserRun
	for _, prompt := range prompts {
		switch {
		case prompt.Source == 2:
			run.End()
		case prompt.Source == 1 && !prompt.IsOrphanedTool:
			if prompt.DroppedPart != "" {
				run.Drop(prompt.DroppedPart)
			}
			if devinPromptHasContent(prompt) {
				run.Add()
			}
		default:
			// A tool result is content the model reads, so it keeps the surrounding user turn.
			run.Add()
		}
	}
	run.End()
	if errRun := run.Err(); errRun != nil {
		return nil, errRun
	}

	kept := make([]DevinPrompt, 0, len(prompts))
	for _, prompt := range prompts {
		if prompt.Source == 1 && !prompt.IsOrphanedTool && prompt.DroppedPart != "" && !devinPromptHasContent(prompt) {
			continue
		}
		kept = append(kept, prompt)
	}
	if len(kept) == len(prompts) {
		return prompts, nil
	}
	return kept, nil
}

// devinPromptHasContent reports whether a user prompt carries non-blank text or an image.
func devinPromptHasContent(prompt DevinPrompt) bool {
	return strings.TrimSpace(prompt.Content) != "" || len(prompt.Images) > 0
}
