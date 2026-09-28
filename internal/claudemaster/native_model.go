package claudemaster

import "errors"

// NativeArguments validates the supported native CLI and returns a defensive
// copy of its arguments. Claude Code owns model and fallback selection.
func NativeArguments(provider string, args []string) ([]string, error) {
	if provider != "claude" {
		return nil, errors.New("inference provider must be claude")
	}
	return cloneNativeArguments(args), nil
}

func cloneNativeArguments(args []string) []string {
	if args == nil {
		return nil
	}
	out := make([]string, len(args))
	copy(out, args)
	return out
}
