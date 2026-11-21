package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// init handles the offline flags before main runs, so plugin bootstrap and
// other startup side effects never happen for them. Go runs init functions in
// file name order, so main.go's logger and build info setup comes first.
func init() {
	if handled, code := runOfflineCommand(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
}

// Handle offline flags before plugin bootstrap or any other startup side effect.
func runOfflineCommand(args []string, stdout, stderr io.Writer) (bool, int) {
	offline := false
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name := strings.SplitN(strings.TrimLeft(arg, "-"), "=", 2)[0]
		if name == "check-config" || name == "capabilities" {
			offline = true
		}
	}
	if !offline {
		return false, 0
	}
	flags := flag.NewFlagSet("offline", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	check := flags.Bool("check-config", false, "Validate config without starting the server")
	capabilities := flags.Bool("capabilities", false, "Print offline capabilities")
	path := flags.String("config", DefaultConfigPath, "Config file")
	if errParse := flags.Parse(args); errParse != nil || flags.NArg() != 0 || (!*check && !*capabilities) {
		fmt.Fprintln(stderr, "invalid offline command arguments")
		return true, 2
	}
	if *check {
		data, errRead := os.ReadFile(*path)
		if errRead != nil {
			fmt.Fprintln(stderr, "config check failed: cannot read file")
			return true, 1
		}
		cfg, errParse := config.ParseConfigBytes(data)
		if errParse != nil || cfg == nil {
			// Parser errors can contain config values. Never echo them here.
			fmt.Fprintln(stderr, "config check failed: invalid configuration")
			return true, 1
		}
		if _, ok := config.NormalizeRoutingStrategy(cfg.Routing.Strategy); !ok {
			fmt.Fprintln(stderr, "config check failed: unknown routing strategy")
			return true, 1
		}
		fmt.Fprintln(stdout, `{"valid":true}`)
		return true, 0
	}
	doc := ojson.NewDocString(`{}`)
	_ = doc.Set("schema_version", 1)
	_ = doc.Set("version", buildinfo.Version)
	_ = doc.Set("commit", buildinfo.Commit)
	_ = doc.Set("config_check", true)
	_ = doc.Set("routing_status_path", "/v8/management/routing/status")
	_ = doc.Set("routing_strategies", []string{"round-robin", "weighted-round-robin", "fill-first", "soonest-reset-first"})
	_ = doc.Set("safe_drain", true)
	_ = doc.Set("drain_schema_version", 1)
	_ = doc.Set("drain_status_path", "/v8/management/server/drain")
	fmt.Fprintln(stdout, doc.String())
	return true, 0
}
