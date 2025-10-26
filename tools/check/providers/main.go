package main

import (
	"fmt"
	registry "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func main() {
	reg := registry.GetGlobalRegistry()
	fmt.Println("providers gpt-5-mini:", reg.GetModelProviders("gpt-5-mini"))
	fmt.Println("providers grok-code-fast-1:", reg.GetModelProviders("grok-code-fast-1"))
}
