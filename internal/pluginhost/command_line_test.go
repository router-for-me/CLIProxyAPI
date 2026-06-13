package pluginhost

import (
	"bytes"
	"context"
	"flag"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRegisterCommandLineFlagsSkipsNativeAndUsesPriority(t *testing.T) {
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	flagSet.SetOutput(&bytes.Buffer{})
	flagSet.Bool("native", false, "native flag")

	high := &commandLinePluginDouble{
		flags: []pluginapi.CommandLineFlag{
			{Name: "native", Type: "bool", Usage: "conflicting native flag"},
			{Name: "help", Type: "bool", Usage: "reserved help flag"},
			{Name: "h", Type: "bool", Usage: "reserved short help flag"},
			{Name: "shared", Type: "string", Usage: "shared flag"},
		},
	}
	low := &commandLinePluginDouble{
		flags: []pluginapi.CommandLineFlag{
			{Name: "shared", Type: "string", Usage: "lower priority shared flag"},
			{Name: "low-only", Type: "int", Usage: "low priority flag"},
		},
	}
	host := newHostWithRecords(
		capabilityRecord{id: "low", priority: 1, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{CommandLinePlugin: low}}},
		capabilityRecord{id: "high", priority: 10, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{CommandLinePlugin: high}}},
	)

	host.RegisterCommandLineFlags(context.Background(), flagSet)

	if flagSet.Lookup("native") == nil {
		t.Fatal("native flag missing")
	}
	if flagSet.Lookup("shared") == nil {
		t.Fatal("shared plugin flag missing")
	}
	if flagSet.Lookup("low-only") == nil {
		t.Fatal("low-only plugin flag missing")
	}
	if got := host.commandLineFlags["shared"].pluginID; got != "high" {
		t.Fatalf("shared owner = %q, want high", got)
	}
	if _, exists := host.commandLineFlags["native"]; exists {
		t.Fatal("native flag was claimed by plugin")
	}
	if _, exists := host.commandLineFlags["help"]; exists {
		t.Fatal("reserved help flag was claimed by plugin")
	}
	if _, exists := host.commandLineFlags["h"]; exists {
		t.Fatal("reserved h flag was claimed by plugin")
	}
}

func TestExecuteCommandLinePassesAllArgsAndTriggeredFlags(t *testing.T) {
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	flagSet.SetOutput(&bytes.Buffer{})
	plugin := &commandLinePluginDouble{
		flags: []pluginapi.CommandLineFlag{{
			Name: "plugin-command",
			Type: "bool",
		}},
	}
	host := newHostWithRecords(capabilityRecord{
		id:     "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{CommandLinePlugin: plugin}},
	})
	host.runtimeConfig = &config.Config{AuthDir: "/tmp/plugin-auth"}
	host.RegisterCommandLineFlags(context.Background(), flagSet)

	if errParse := flagSet.Parse([]string{"-plugin-command", "tail"}); errParse != nil {
		t.Fatalf("Parse() error = %v", errParse)
	}
	if !host.HasTriggeredCommandLineFlags() {
		t.Fatal("HasTriggeredCommandLineFlags() = false, want true")
	}

	exitCode, handled := host.ExecuteCommandLine(context.Background(), "cliproxy", []string{"-plugin-command", "tail"}, "/tmp/config.yaml", flagSet)
	if !handled {
		t.Fatal("ExecuteCommandLine() handled = false, want true")
	}
	if exitCode != 0 {
		t.Fatalf("ExecuteCommandLine() exitCode = %d, want 0", exitCode)
	}
	if len(plugin.execRequests) != 1 {
		t.Fatalf("execute calls = %d, want 1", len(plugin.execRequests))
	}
	req := plugin.execRequests[0]
	if req.Program != "cliproxy" || req.ConfigPath != "/tmp/config.yaml" {
		t.Fatalf("execution request = %#v, want program and config path", req)
	}
	if req.Host.AuthDir != "/tmp/plugin-auth" {
		t.Fatalf("execution request host = %#v, want auth dir", req.Host)
	}
	if len(req.Args) != 2 || req.Args[0] != "-plugin-command" || req.Args[1] != "tail" {
		t.Fatalf("Args = %#v, want full args", req.Args)
	}
	if got := req.TriggeredFlags["plugin-command"]; !got.Set || got.Value != "true" {
		t.Fatalf("TriggeredFlags[plugin-command] = %#v, want set true", got)
	}
}

func TestExecuteCommandLinePersistsReturnedAuths(t *testing.T) {
	authDir := t.TempDir()
	store := &commandLineAuthStore{}
	origStore := sdkAuth.GetTokenStore()
	sdkAuth.RegisterTokenStore(store)
	defer sdkAuth.RegisterTokenStore(origStore)

	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	flagSet.SetOutput(&bytes.Buffer{})
	plugin := &commandLinePluginDouble{
		flags: []pluginapi.CommandLineFlag{{
			Name: "plugin-login",
			Type: "bool",
		}},
		response: pluginapi.CommandLineExecutionResponse{
			Stdout: []byte("login ok\n"),
			Auths: []pluginapi.AuthData{{
				Provider:    "Sample-Provider",
				ID:          "sample-provider.json",
				FileName:    "sample-provider.json",
				Label:       "Luis",
				StorageJSON: []byte(`{"token":"secret"}`),
			}},
		},
	}
	host := newHostWithRecords(capabilityRecord{
		id:     "sample-provider",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{CommandLinePlugin: plugin}},
	})
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.RegisterCommandLineFlags(context.Background(), flagSet)

	if errParse := flagSet.Parse([]string{"-plugin-login"}); errParse != nil {
		t.Fatalf("Parse() error = %v", errParse)
	}

	exitCode, handled := host.ExecuteCommandLine(context.Background(), "cliproxy", []string{"-plugin-login"}, "/tmp/config.yaml", flagSet)
	if !handled {
		t.Fatal("ExecuteCommandLine() handled = false, want true")
	}
	if exitCode != 0 {
		t.Fatalf("ExecuteCommandLine() exitCode = %d, want 0", exitCode)
	}
	if store.baseDir != authDir {
		t.Fatalf("store baseDir = %q, want %q", store.baseDir, authDir)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved auths = %d, want 1", len(store.saved))
	}
	saved := store.saved[0]
	if saved.Provider != "sample-provider" || saved.ID != "sample-provider.json" || saved.FileName != "sample-provider.json" {
		t.Fatalf("saved auth = %#v, want normalized sample provider auth", saved)
	}
	if saved.Storage == nil {
		t.Fatal("saved auth storage = nil, want plugin token storage")
	}
	if store.paths[0] != filepath.Join(authDir, "sample-provider.json") {
		t.Fatalf("saved path = %q, want auth dir path", store.paths[0])
	}
}

type commandLinePluginDouble struct {
	flags        []pluginapi.CommandLineFlag
	execRequests []pluginapi.CommandLineExecutionRequest
	response     pluginapi.CommandLineExecutionResponse
}

func (p *commandLinePluginDouble) RegisterCommandLine(context.Context, pluginapi.CommandLineRegistrationRequest) (pluginapi.CommandLineRegistrationResponse, error) {
	return pluginapi.CommandLineRegistrationResponse{Flags: p.flags}, nil
}

func (p *commandLinePluginDouble) ExecuteCommandLine(ctx context.Context, req pluginapi.CommandLineExecutionRequest) (pluginapi.CommandLineExecutionResponse, error) {
	p.execRequests = append(p.execRequests, req)
	return p.response, nil
}

type commandLineAuthStore struct {
	baseDir string
	saved   []*coreauth.Auth
	paths   []string
}

func (s *commandLineAuthStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (s *commandLineAuthStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	s.saved = append(s.saved, auth.Clone())
	path := filepath.Join(s.baseDir, auth.FileName)
	s.paths = append(s.paths, path)
	return path, nil
}

func (s *commandLineAuthStore) Delete(context.Context, string) error {
	return nil
}

func (s *commandLineAuthStore) SetBaseDir(dir string) {
	s.baseDir = dir
}
