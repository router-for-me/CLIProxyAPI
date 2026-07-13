package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/extractor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// authExtractor discovers local credentials for coding agents and writes them
// as CLIProxyAPI auth JSON files under ~/.cli-proxy-api.
func main() {
	authDir, err := util.ResolveAuthDir("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth-extractor: resolve auth dir: %v\n", err)
		os.Exit(1)
	}

	found, err := extractor.ExtractAll(authDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth-extractor: %v\n", err)
		os.Exit(1)
	}
	if len(found) == 0 {
		fmt.Println("auth-extractor: no credentials found")
		return
	}
	fmt.Printf("auth-extractor: wrote credentials for %s to %s\n", strings.Join(found, ", "), authDir)
}
