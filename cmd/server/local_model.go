package main

import (
	"flag"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func flagExplicitlySet(fs *flag.FlagSet, name string) bool {
	if fs == nil || name == "" {
		return false
	}

	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func effectiveLocalModel(flagValue bool, flagSet bool, cfg *config.Config) bool {
	if flagSet {
		return flagValue
	}
	return cfg != nil && cfg.LocalModel
}
