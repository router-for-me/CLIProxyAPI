// Command fetch_api_cost_pricing downloads the LiteLLM model price catalog
// and writes the filtered snapshot embedded by internal/apicost.
//
// Usage:
//
//	go run ./cmd/fetch_api_cost_pricing [--output internal/apicost/litellm_pricing.json]
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/apicost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpfetch"
)

func main() {
	output := flag.String("output", "internal/apicost/litellm_pricing.json", "Output snapshot path")
	flag.Parse()
	if errRun := run(*output); errRun != nil {
		fmt.Fprintln(os.Stderr, errRun)
		os.Exit(1)
	}
}

func run(output string) error {
	data, errFetch := httpfetch.GetBytes(context.Background(), http.DefaultClient, apicost.LiteLLMPricingURL, nil, 64<<20)
	if errFetch != nil {
		return fmt.Errorf("fetch pricing: %w", errFetch)
	}
	filtered, errFilter := apicost.FilterLiteLLM(data)
	if errFilter != nil {
		return fmt.Errorf("filter pricing: %w", errFilter)
	}
	if _, errParse := apicost.ParseLiteLLM(filtered); errParse != nil {
		return fmt.Errorf("validate snapshot: %w", errParse)
	}
	if errWrite := os.WriteFile(output, append(filtered, '\n'), 0o644); errWrite != nil {
		return fmt.Errorf("write snapshot: %w", errWrite)
	}
	fmt.Printf("wrote %s (%d bytes)\n", output, len(filtered)+1)
	return nil
}
