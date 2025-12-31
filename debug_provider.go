package main

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

func main() {
	modelName := "gemini-3-pro-preview"
	
	// 1. Check if model exists in registry
	modelInfo := registry.LookupStaticModelInfo(modelName)
	if modelInfo != nil {
		fmt.Printf("✅ Model found in static definitions: %+v\n", modelInfo)
	} else {
		fmt.Printf("❌ Model NOT found in static definitions\n")
	}

	// 2. Check provider resolution
	providers := util.GetProviderName(modelName)
	fmt.Printf("Providers for '%s': %v\n", modelName, providers)

	if len(providers) == 0 {
		fmt.Println("❌ No providers found! This explains the 'unknown provider' error.")
		
		// Debug: check GetGeminiModels directly
		fmt.Println("\nDebugging GetGeminiModels:")
		for _, m := range registry.GetGeminiModels() {
			if m.ID == modelName {
				fmt.Printf("  Found in GetGeminiModels: %+v\n", m)
			}
		}
	} else {
		fmt.Println("✅ Providers resolved successfully.")
	}
}
