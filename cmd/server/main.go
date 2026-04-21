package main

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	log "github.com/sirupsen/logrus"
)

var (
	Version           = "dev"
	Commit            = "none"
	BuildDate         = "unknown"
	DefaultConfigPath = ""
)

func init() {
	logging.SetupBaseLogger()
	buildinfo.Version = Version
	buildinfo.Commit = Commit
	buildinfo.BuildDate = BuildDate
}

func main() {
	printVersion()

	flags := parseRuntimeFlags()
	state, err := prepareStartup(flags)
	if err != nil {
		log.Errorf("startup failed: %v", err)
		return
	}

	dispatchCommand(flags, state)
}

func printVersion() {
	fmt.Printf(
		"CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s\n",
		buildinfo.Version,
		buildinfo.Commit,
		buildinfo.BuildDate,
	)
}
