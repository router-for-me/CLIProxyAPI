package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/minimax"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoMinimaxLogin triggers the OAuth device-code flow for the MiniMax provider and saves tokens.
// The region selects which provider key the credential is filed under, mirroring
// the way kimi.com and kimi.ai are kept as separate providers. When no region is
// supplied on the command line and a terminal is attached, the user is asked to pick.
func DoMinimaxLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	region := minimax.NormalizeRegion(options.Region)
	if strings.TrimSpace(options.Region) == "" {
		if selected, ok := promptMinimaxRegion(promptFn); ok {
			region = selected
		}
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
		Prompt:       promptFn,
	}

	provider := minimax.ProviderForRegion(region)
	record, savedPath, err := manager.Login(context.Background(), provider, cfg, authOpts)
	if err != nil {
		log.Errorf("MiniMax authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("MiniMax authentication successful!")
}

// promptMinimaxRegion asks which MiniMax region to authenticate against. It
// returns false without prompting when the caller is not attached to a terminal,
// so non-interactive runs fall back to the global default instead of blocking.
func promptMinimaxRegion(promptFn func(string) (string, error)) (string, bool) {
	if promptFn == nil || !stdinIsTerminal() {
		return "", false
	}
	return promptMinimaxRegionWithAnswers(promptFn)
}

// promptMinimaxRegionWithAnswers runs the region prompt loop, retrying on an
// unrecognized answer.
func promptMinimaxRegionWithAnswers(promptFn func(string) (string, error)) (string, bool) {
	fmt.Println("MiniMax serves two regions:")
	fmt.Printf("  1) Global  %s\n", minimax.ResolveAPIBaseURL(minimax.RegionGlobal))
	fmt.Printf("  2) China   %s\n\n", minimax.ResolveAPIBaseURL(minimax.RegionCN))

	for {
		answer, err := promptFn("Select a region [1/2, default 1]: ")
		if err != nil {
			log.Warnf("MiniMax region prompt failed, using global: %v", err)
			return "", false
		}
		if region := matchMinimaxRegionAnswer(answer); region != "" {
			return region, true
		}
		fmt.Println("Please enter 1 for Global or 2 for China.")
	}
}

// matchMinimaxRegionAnswer maps a user answer to a region, returning an empty
// string when the answer is not recognized.
func matchMinimaxRegionAnswer(answer string) string {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "1", "global", "g":
		return minimax.RegionGlobal
	case "2", "cn", "china", "c":
		return minimax.RegionCN
	default:
		return ""
	}
}

// stdinIsTerminal reports whether stdin is attached to an interactive terminal.
// It is a variable so tests can substitute a deterministic value.
var stdinIsTerminal = func() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
