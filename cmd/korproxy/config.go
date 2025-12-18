package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/errors"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

func runConfigCommand(args []string) {
	if len(args) < 1 {
		printConfigUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "export":
		runConfigExport(args[1:])
	case "import":
		runConfigImport(args[1:])
	case "validate":
		runConfigValidate(args[1:])
	case "help", "-h", "--help":
		printConfigUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown config subcommand: %s\n", args[0])
		printConfigUsage()
		os.Exit(1)
	}
}

func printConfigUsage() {
	fmt.Println(`korproxy config - Manage configuration

Usage:
  korproxy config <subcommand> [options]

Subcommands:
  export      Export current configuration to JSON
  import      Import configuration from JSON file
  validate    Validate a configuration file

Examples:
  korproxy config export                        # Output to stdout
  korproxy config export --output config.json   # Output to file
  korproxy config import config.json            # Replace config
  korproxy config import config.json --merge    # Merge with existing
  korproxy config validate config.json          # Validate file`)
}

func runConfigExport(args []string) {
	fs := flag.NewFlagSet("config export", flag.ExitOnError)
	output := fs.String("output", "", "Output file path (default: stdout)")
	fs.Parse(args)

	cfg, err := routing.LoadConfig()
	if err != nil {
		printError("KP-CONF-201", fmt.Sprintf("Failed to load config: %v", err))
		os.Exit(1)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		printError("KP-SYS-501", fmt.Sprintf("Failed to marshal config: %v", err))
		os.Exit(1)
	}

	if *output == "" {
		fmt.Println(string(data))
	} else {
		if err := os.WriteFile(*output, data, 0644); err != nil {
			printError("KP-SYS-503", fmt.Sprintf("Failed to write file: %v", err))
			os.Exit(1)
		}
		fmt.Printf("Configuration exported to %s\n", *output)
	}
}

func runConfigImport(args []string) {
	fs := flag.NewFlagSet("config import", flag.ExitOnError)
	merge := fs.Bool("merge", false, "Merge with existing configuration")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: file path required")
		fmt.Fprintln(os.Stderr, "Usage: korproxy config import <file.json> [--merge]")
		os.Exit(1)
	}

	filePath := fs.Arg(0)
	data, err := os.ReadFile(filePath)
	if err != nil {
		printError("KP-CONF-201", fmt.Sprintf("Failed to read file: %v", err))
		os.Exit(1)
	}

	var newCfg routing.RoutingConfig
	if err := json.Unmarshal(data, &newCfg); err != nil {
		printError("KP-CONF-203", fmt.Sprintf("Failed to parse JSON: %v", err))
		os.Exit(1)
	}

	if err := newCfg.Validate(); err != nil {
		printError("KP-CONF-203", fmt.Sprintf("Validation failed: %v", err))
		os.Exit(1)
	}

	if *merge {
		existingCfg, err := routing.LoadConfig()
		if err != nil {
			printError("KP-CONF-201", fmt.Sprintf("Failed to load existing config: %v", err))
			os.Exit(1)
		}
		mergeConfigs(existingCfg, &newCfg)
		if err := routing.SaveConfig(existingCfg); err != nil {
			printError("KP-SYS-501", fmt.Sprintf("Failed to save config: %v", err))
			os.Exit(1)
		}
		fmt.Println("Configuration merged successfully")
	} else {
		if err := routing.SaveConfig(&newCfg); err != nil {
			printError("KP-SYS-501", fmt.Sprintf("Failed to save config: %v", err))
			os.Exit(1)
		}
		fmt.Println("Configuration imported successfully")
	}
}

func mergeConfigs(existing, new *routing.RoutingConfig) {
	existingProfileIDs := make(map[string]bool)
	for _, p := range existing.Profiles {
		existingProfileIDs[p.ID] = true
	}
	for _, p := range new.Profiles {
		if !existingProfileIDs[p.ID] {
			existing.Profiles = append(existing.Profiles, p)
		}
	}

	existingGroupIDs := make(map[string]bool)
	for _, g := range existing.ProviderGroups {
		existingGroupIDs[g.ID] = true
	}
	for _, g := range new.ProviderGroups {
		if !existingGroupIDs[g.ID] {
			existing.ProviderGroups = append(existing.ProviderGroups, g)
		}
	}

	if new.ActiveProfileID != nil && existing.ActiveProfileID == nil {
		existing.ActiveProfileID = new.ActiveProfileID
	}
}

func runConfigValidate(args []string) {
	fs := flag.NewFlagSet("config validate", flag.ExitOnError)
	fs.Parse(args)

	var data []byte
	var err error
	var source string

	if fs.NArg() < 1 {
		source = "stdin"
		data, err = io.ReadAll(os.Stdin)
	} else {
		source = fs.Arg(0)
		data, err = os.ReadFile(source)
	}

	if err != nil {
		printError("KP-CONF-201", fmt.Sprintf("Failed to read %s: %v", source, err))
		os.Exit(1)
	}

	var cfg routing.RoutingConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		printError("KP-CONF-203", fmt.Sprintf("Invalid JSON: %v", err))
		os.Exit(1)
	}

	validationErrors := validateConfigStruct(&cfg)
	if len(validationErrors) > 0 {
		fmt.Fprintln(os.Stderr, "Validation errors:")
		for _, e := range validationErrors {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
		os.Exit(1)
	}

	if err := cfg.Validate(); err != nil {
		printError("KP-CONF-203", fmt.Sprintf("Validation failed: %v", err))
		os.Exit(1)
	}

	fmt.Println("Configuration is valid")
}

func validateConfigStruct(cfg *routing.RoutingConfig) []string {
	var errs []string

	if cfg.Version < 1 {
		errs = append(errs, "version must be >= 1")
	}

	profileIDs := make(map[string]bool)
	for i, p := range cfg.Profiles {
		if p.ID == "" {
			errs = append(errs, fmt.Sprintf("profile[%d]: id is required", i))
		} else if profileIDs[p.ID] {
			errs = append(errs, fmt.Sprintf("profile[%d]: duplicate id '%s'", i, p.ID))
		} else {
			profileIDs[p.ID] = true
		}
		if p.Name == "" {
			errs = append(errs, fmt.Sprintf("profile[%d]: name is required", i))
		}
	}

	groupIDs := make(map[string]bool)
	for i, g := range cfg.ProviderGroups {
		if g.ID == "" {
			errs = append(errs, fmt.Sprintf("providerGroup[%d]: id is required", i))
		} else if groupIDs[g.ID] {
			errs = append(errs, fmt.Sprintf("providerGroup[%d]: duplicate id '%s'", i, g.ID))
		} else {
			groupIDs[g.ID] = true
		}
		if g.Name == "" {
			errs = append(errs, fmt.Sprintf("providerGroup[%d]: name is required", i))
		}
		if !g.SelectionStrategy.IsValid() && g.SelectionStrategy != "" {
			errs = append(errs, fmt.Sprintf("providerGroup[%d]: invalid selectionStrategy '%s'", i, g.SelectionStrategy))
		}
	}

	if cfg.ActiveProfileID != nil && *cfg.ActiveProfileID != "" {
		if !profileIDs[*cfg.ActiveProfileID] {
			errs = append(errs, fmt.Sprintf("activeProfileId '%s' references non-existent profile", *cfg.ActiveProfileID))
		}
	}

	return errs
}

func printError(code, message string) {
	kpErr := errors.GetError(code)
	if kpErr != nil {
		fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", code, kpErr.Message, message)
	} else {
		fmt.Fprintf(os.Stderr, "[%s] %s\n", code, message)
	}
}
