package responsestools

import "strings"

// Adapt upstream 3ebee065's patch instructions at the canonical custom bridge.
// Aliases, history and response identity continue to use the generic bridge.
const customPatchInstructions = `Call this function with a JSON object whose input field contains the complete patch text.
Use the Codex apply_patch format, not a conventional git unified diff.
Start with *** Begin Patch and end with *** End Patch.
Use *** Add File: path, *** Delete File: path, or *** Update File: path.
Every added-file content line starts with +.
For updates, use @@; context lines start with one space, removed lines with -, and added lines with +.
Use *** Move to: path for a rename and *** End of File when required by the patch grammar.
Example input:
*** Begin Patch
*** Update File: src/main.go
@@
-old
+new
*** End Patch`

func customPatchDescription(original string) string {
	original = strings.TrimSpace(strings.ReplaceAll(original, "This is a FREEFORM tool, so do not wrap the patch in JSON.", ""))
	if original == "" {
		return customPatchInstructions
	}
	return original + "\n\n" + customPatchInstructions
}
