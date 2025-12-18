// Package main provides the CLI entry point for korproxy commands.
// It supports config management, provider testing, profile management,
// self-tests, and debug bundle generation.
package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "1.0.0"

var (
	verbose bool
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	switch os.Args[1] {
	case "config":
		runConfigCommand(os.Args[2:])
	case "provider":
		runProviderCommand(os.Args[2:])
	case "profile":
		runProfileCommand(os.Args[2:])
	case "self-test":
		runSelfTestCommand(os.Args[2:])
	case "debug-bundle":
		runDebugBundleCommand(os.Args[2:])
	case "version":
		fmt.Printf("korproxy version %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`korproxy - KorProxy CLI Tool

Usage:
  korproxy <command> [options]

Commands:
  config      Manage configuration (export, import, validate)
  provider    Manage providers (list, test)
  profile     Manage profiles (list, create, switch, delete)
  self-test   Run self-tests on all configured providers
  debug-bundle Generate a debug bundle for troubleshooting
  version     Print version information
  help        Show this help message

Run 'korproxy <command> --help' for more information about a command.

Examples:
  korproxy config export --output config-backup.json
  korproxy config import config.json --merge
  korproxy config validate config.json
  korproxy provider list
  korproxy provider test claude
  korproxy provider test --all
  korproxy profile list
  korproxy profile create work --copy-from default
  korproxy profile switch work
  korproxy profile delete work
  korproxy self-test --verbose
  korproxy debug-bundle --output debug.json`)
}

func setVerboseFlag(fs *flag.FlagSet) {
	fs.BoolVar(&verbose, "verbose", false, "Enable verbose output")
	fs.BoolVar(&verbose, "v", false, "Enable verbose output (shorthand)")
}
