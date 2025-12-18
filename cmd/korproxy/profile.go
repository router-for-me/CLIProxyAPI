package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

func runProfileCommand(args []string) {
	if len(args) < 1 {
		printProfileUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		runProfileList(args[1:])
	case "create":
		runProfileCreate(args[1:])
	case "switch":
		runProfileSwitch(args[1:])
	case "delete":
		runProfileDelete(args[1:])
	case "help", "-h", "--help":
		printProfileUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown profile subcommand: %s\n", args[0])
		printProfileUsage()
		os.Exit(1)
	}
}

func printProfileUsage() {
	fmt.Println(`korproxy profile - Manage profiles

Usage:
  korproxy profile <subcommand> [options]

Subcommands:
  list      List all profiles
  create    Create a new profile
  switch    Switch to a different profile
  delete    Delete a profile

Examples:
  korproxy profile list
  korproxy profile create work
  korproxy profile create work --copy-from default
  korproxy profile switch work
  korproxy profile delete work`)
}

func runProfileList(args []string) {
	fs := flag.NewFlagSet("profile list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	setVerboseFlag(fs)
	fs.Parse(args)

	cfg, err := routing.LoadConfig()
	if err != nil {
		printError("KP-CONF-201", fmt.Sprintf("Failed to load config: %v", err))
		os.Exit(1)
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(cfg.Profiles, "", "  ")
		fmt.Println(string(data))
		return
	}

	if len(cfg.Profiles) == 0 {
		fmt.Println("No profiles configured")
		return
	}

	activeID := ""
	if cfg.ActiveProfileID != nil {
		activeID = *cfg.ActiveProfileID
	}

	fmt.Println("Profiles:")
	fmt.Println("---------")
	for _, p := range cfg.Profiles {
		marker := "  "
		if p.ID == activeID {
			marker = "* "
		}
		fmt.Printf("%s%s (%s)\n", marker, p.Name, p.ID)
		if verbose {
			fmt.Printf("    Color: %s\n", p.Color)
			ruleCount := countRoutingRules(p.RoutingRules)
			fmt.Printf("    Routing Rules: %d configured\n", ruleCount)
		}
	}
	fmt.Println("\n* = active profile")
}

func countRoutingRules(rr routing.RoutingRules) int {
	count := 0
	if rr.Chat != nil {
		count++
	}
	if rr.Completion != nil {
		count++
	}
	if rr.Embedding != nil {
		count++
	}
	if rr.Other != nil {
		count++
	}
	return count
}

func runProfileCreate(args []string) {
	fs := flag.NewFlagSet("profile create", flag.ExitOnError)
	copyFrom := fs.String("copy-from", "", "Copy settings from existing profile")
	color := fs.String("color", "#3B82F6", "Profile color (hex)")
	setVerboseFlag(fs)
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: profile name required")
		fmt.Fprintln(os.Stderr, "Usage: korproxy profile create <name> [--copy-from existing] [--color #hex]")
		os.Exit(1)
	}

	name := fs.Arg(0)

	cfg, err := routing.LoadConfig()
	if err != nil {
		printError("KP-CONF-201", fmt.Sprintf("Failed to load config: %v", err))
		os.Exit(1)
	}

	// Check if profile name already exists
	for _, p := range cfg.Profiles {
		if strings.EqualFold(p.Name, name) {
			printError("KP-CONF-203", fmt.Sprintf("Profile '%s' already exists", name))
			os.Exit(1)
		}
	}

	now := time.Now()
	newProfile := routing.Profile{
		ID:           uuid.New().String(),
		Name:         name,
		Color:        *color,
		RoutingRules: routing.RoutingRules{},
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if *copyFrom != "" {
		var sourceProfile *routing.Profile
		for i := range cfg.Profiles {
			if cfg.Profiles[i].Name == *copyFrom || cfg.Profiles[i].ID == *copyFrom {
				sourceProfile = &cfg.Profiles[i]
				break
			}
		}
		if sourceProfile == nil {
			printError("KP-CONF-202", fmt.Sprintf("Source profile '%s' not found", *copyFrom))
			os.Exit(1)
		}
		// Copy routing rules from source
		newProfile.RoutingRules = sourceProfile.RoutingRules
		newProfile.DefaultProviderGroup = sourceProfile.DefaultProviderGroup
		if verbose {
			fmt.Printf("Copied routing rules from '%s'\n", sourceProfile.Name)
		}
	}

	cfg.Profiles = append(cfg.Profiles, newProfile)

	if err := routing.SaveConfig(cfg); err != nil {
		printError("KP-SYS-501", fmt.Sprintf("Failed to save config: %v", err))
		os.Exit(1)
	}

	fmt.Printf("Profile '%s' created successfully (ID: %s)\n", name, newProfile.ID)
}

func runProfileSwitch(args []string) {
	fs := flag.NewFlagSet("profile switch", flag.ExitOnError)
	setVerboseFlag(fs)
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: profile name or ID required")
		fmt.Fprintln(os.Stderr, "Usage: korproxy profile switch <name|id>")
		os.Exit(1)
	}

	nameOrID := fs.Arg(0)

	cfg, err := routing.LoadConfig()
	if err != nil {
		printError("KP-CONF-201", fmt.Sprintf("Failed to load config: %v", err))
		os.Exit(1)
	}

	var targetProfile *routing.Profile
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == nameOrID || cfg.Profiles[i].ID == nameOrID {
			targetProfile = &cfg.Profiles[i]
			break
		}
	}

	if targetProfile == nil {
		printError("KP-CONF-202", fmt.Sprintf("Profile '%s' not found", nameOrID))
		os.Exit(1)
	}

	cfg.ActiveProfileID = &targetProfile.ID

	if err := routing.SaveConfig(cfg); err != nil {
		printError("KP-SYS-501", fmt.Sprintf("Failed to save config: %v", err))
		os.Exit(1)
	}

	fmt.Printf("Switched to profile '%s'\n", targetProfile.Name)
}

func runProfileDelete(args []string) {
	fs := flag.NewFlagSet("profile delete", flag.ExitOnError)
	force := fs.Bool("force", false, "Delete without confirmation")
	setVerboseFlag(fs)
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: profile name or ID required")
		fmt.Fprintln(os.Stderr, "Usage: korproxy profile delete <name|id> [--force]")
		os.Exit(1)
	}

	nameOrID := fs.Arg(0)

	cfg, err := routing.LoadConfig()
	if err != nil {
		printError("KP-CONF-201", fmt.Sprintf("Failed to load config: %v", err))
		os.Exit(1)
	}

	var targetIndex = -1
	var targetProfile *routing.Profile
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == nameOrID || cfg.Profiles[i].ID == nameOrID {
			targetIndex = i
			targetProfile = &cfg.Profiles[i]
			break
		}
	}

	if targetProfile == nil {
		printError("KP-CONF-202", fmt.Sprintf("Profile '%s' not found", nameOrID))
		os.Exit(1)
	}

	// Check if this is the active profile
	if cfg.ActiveProfileID != nil && *cfg.ActiveProfileID == targetProfile.ID {
		if !*force {
			fmt.Fprintln(os.Stderr, "Error: Cannot delete active profile. Switch to another profile first or use --force")
			os.Exit(1)
		}
		// Clear active profile
		cfg.ActiveProfileID = nil
	}

	// Remove the profile
	cfg.Profiles = append(cfg.Profiles[:targetIndex], cfg.Profiles[targetIndex+1:]...)

	if err := routing.SaveConfig(cfg); err != nil {
		printError("KP-SYS-501", fmt.Sprintf("Failed to save config: %v", err))
		os.Exit(1)
	}

	fmt.Printf("Profile '%s' deleted successfully\n", targetProfile.Name)
}
