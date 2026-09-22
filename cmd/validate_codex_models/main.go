// Command validate_codex_models validates a Codex client model catalog file.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func main() {
	var inputPath string
	flag.StringVar(&inputPath, "file", "", "Codex client model catalog JSON file")
	flag.Parse()

	if strings.TrimSpace(inputPath) == "" {
		fmt.Fprintln(os.Stderr, "error: --file is required")
		os.Exit(2)
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read %s: %v\n", inputPath, err)
		os.Exit(1)
	}
	if err = registry.ValidateCodexClientModelsJSON(data); err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid Codex client model catalog %s: %v\n", inputPath, err)
		os.Exit(1)
	}
	fmt.Printf("Validated Codex client model catalog: %s\n", inputPath)
}
