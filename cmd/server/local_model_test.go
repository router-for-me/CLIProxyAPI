package main

import (
	"flag"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestEffectiveLocalModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		flagSet   bool
		flagValue bool
		cfg       *config.Config
		want      bool
	}{
		{
			name:      "default false when unset",
			flagSet:   false,
			flagValue: false,
			cfg:       &config.Config{},
			want:      false,
		},
		{
			name:      "config enables local model when flag unset",
			flagSet:   false,
			flagValue: false,
			cfg:       &config.Config{SDKConfig: config.SDKConfig{LocalModel: true}},
			want:      true,
		},
		{
			name:      "command line true overrides config false",
			flagSet:   true,
			flagValue: true,
			cfg:       &config.Config{},
			want:      true,
		},
		{
			name:      "command line false overrides config true",
			flagSet:   true,
			flagValue: false,
			cfg:       &config.Config{SDKConfig: config.SDKConfig{LocalModel: true}},
			want:      false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveLocalModel(tt.flagValue, tt.flagSet, tt.cfg); got != tt.want {
				t.Fatalf("effectiveLocalModel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFlagExplicitlySet(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var localModel bool
	fs.BoolVar(&localModel, "local-model", false, "")

	if err := fs.Parse([]string{"-local-model=false"}); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !flagExplicitlySet(fs, "local-model") {
		t.Fatal("flagExplicitlySet() = false, want true")
	}
	if localModel {
		t.Fatalf("localModel = %v, want false", localModel)
	}
}
