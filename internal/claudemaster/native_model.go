package claudemaster

import (
	"errors"
	"strings"
)

// NativeArguments keeps Claude's model-dependent request shape aligned with a
// pinned Claude inference model. In particular, aliases such as "sonnet" can
// resolve to a newer family than the selected backend and must not be used.
// Codex translation does not yet have a verified native-model mapping, so its
// native model arguments are preserved. Neither provider permits native fallback.
// The caller remains responsible for validating the backend's model catalog.
func NativeArguments(provider, model string, args []string) ([]string, error) {
	if provider != "claude" && provider != "codex" {
		return nil, errors.New("inference provider must be claude or codex")
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("an explicit backend model is required")
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--fallback-model" || strings.HasPrefix(arg, "--fallback-model=") {
			return nil, errors.New("native fallback models are not supported; inference is pinned to one backend model")
		}
	}
	foundModel := false
	for i := 0; provider == "claude" && i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg != "--model" && !strings.HasPrefix(arg, "--model=") {
			continue
		}
		if foundModel {
			return nil, errors.New("native Claude --model may only be supplied once")
		}
		foundModel = true
		var selected string
		if arg == "--model" {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "-") {
				return nil, errors.New("native Claude --model requires an explicit model value")
			}
			i++
			selected = args[i]
		} else {
			selected = strings.TrimPrefix(arg, "--model=")
		}
		if strings.TrimSpace(selected) == "" {
			return nil, errors.New("native Claude --model requires an explicit model value")
		}
		if selected != model {
			return nil, errors.New("native Claude --model must exactly match the selected inference model; aliases and overrides are not supported")
		}
	}
	if provider == "claude" && !foundModel {
		out := make([]string, 0, len(args)+2)
		out = append(out, "--model", model)
		return append(out, args...), nil
	}
	if args == nil {
		return nil, nil
	}
	out := make([]string, len(args))
	copy(out, args)
	return out, nil
}
