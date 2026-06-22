package homeplugins

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
	"golang.org/x/sys/cpu"
	"gopkg.in/yaml.v3"
)

type Platform struct {
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
	Variant string `json:"variant,omitempty"`
}

type PluginRuntime interface {
	PluginBusy(id string) bool
	UnloadPlugin(id string) bool
}

// CurrentPlatform reports the platform used by pluginhost discovery.
func CurrentPlatform() Platform {
	return Platform{
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Variant: cpuVariant(),
	}
}

func NormalizePlatform(platform Platform) Platform {
	goos := strings.ToLower(strings.TrimSpace(platform.GOOS))
	switch goos {
	case "mac", "macos", "osx":
		goos = "darwin"
	}
	goarch := strings.ToLower(strings.TrimSpace(platform.GOARCH))
	switch goarch {
	case "x64", "x86_64":
		goarch = "amd64"
	case "aarch64":
		goarch = "arm64"
	}
	variant := strings.ToLower(strings.TrimSpace(platform.Variant))
	return Platform{GOOS: goos, GOARCH: goarch, Variant: variant}
}

func Sync(ctx context.Context, cfg *config.Config, pluginRuntime PluginRuntime) error {
	return SyncPlatform(ctx, cfg, pluginRuntime, CurrentPlatform())
}

func SyncPlatform(ctx context.Context, cfg *config.Config, pluginRuntime PluginRuntime, platform Platform) error {
	if cfg == nil || !cfg.Home.Enabled || !cfg.Plugins.Enabled {
		return nil
	}
	platform = NormalizePlatform(platform)
	if platform.GOOS == "" {
		return fmt.Errorf("home plugins: goos is required")
	}
	if platform.GOARCH == "" {
		return fmt.Errorf("home plugins: goarch is required")
	}
	root := strings.TrimSpace(cfg.Plugins.Dir)
	if root == "" {
		root = "plugins"
	}
	client := newPluginStoreClient(cfg)
	for id, item := range cfg.Plugins.Configs {
		if !pluginConfigEnabled(item) {
			continue
		}
		manifest, okManifest, errManifest := storeManifestFromPluginConfig(id, item)
		if errManifest != nil {
			return errManifest
		}
		if !okManifest {
			continue
		}
		if errSync := installManifest(ctx, client, manifest, root, platform, pluginRuntime); errSync != nil {
			return errSync
		}
	}
	return nil
}

func installManifest(ctx context.Context, client sdkpluginstore.Client, manifest sdkpluginstore.Manifest, root string, platform Platform, pluginRuntime PluginRuntime) error {
	id := strings.TrimSpace(manifest.ID)
	if id == "" {
		return fmt.Errorf("home plugins: manifest plugin id is empty")
	}
	pluginIsBusy := func() bool {
		return pluginRuntime != nil && pluginRuntime.PluginBusy(id)
	}
	_, errInstall := client.InstallManifest(ctx, manifest, sdkpluginstore.InstallOptions{
		PluginsDir:   root,
		GOOS:         platform.GOOS,
		GOARCH:       platform.GOARCH,
		PluginLoaded: pluginIsBusy,
		BeforeWrite: func() error {
			if !pluginIsBusy() {
				return nil
			}
			if pluginRuntime == nil || !pluginRuntime.UnloadPlugin(id) && pluginIsBusy() {
				return sdkpluginstore.ErrLoadedPluginLocked
			}
			return nil
		},
	})
	if errInstall != nil {
		return fmt.Errorf("home plugins: install %s: %w", id, errInstall)
	}
	return nil
}

func storeManifestFromPluginConfig(id string, item config.PluginInstanceConfig) (sdkpluginstore.Manifest, bool, error) {
	if item.Raw.Kind == 0 {
		return sdkpluginstore.Manifest{}, false, nil
	}
	storeNode := yamlMappingValue(&item.Raw, "store")
	if storeNode == nil || storeNode.Kind == 0 {
		return sdkpluginstore.Manifest{}, false, nil
	}
	var manifest sdkpluginstore.Manifest
	if errDecode := storeNode.Decode(&manifest); errDecode != nil {
		return sdkpluginstore.Manifest{}, false, fmt.Errorf("home plugins: decode store manifest for %s: %w", id, errDecode)
	}
	if strings.TrimSpace(manifest.ID) == "" {
		manifest.ID = strings.TrimSpace(id)
	}
	if errValidate := manifest.Validate(); errValidate != nil {
		return sdkpluginstore.Manifest{}, false, fmt.Errorf("home plugins: invalid store manifest for %s: %w", id, errValidate)
	}
	return manifest, true, nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		if keyNode == nil || keyNode.Value != key {
			continue
		}
		return node.Content[i+1]
	}
	return nil
}

var newPluginStoreClient = func(cfg *config.Config) sdkpluginstore.Client {
	client := &http.Client{}
	if cfg != nil && strings.TrimSpace(cfg.ProxyURL) != "" {
		util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(cfg.ProxyURL)}, client)
	}
	return sdkpluginstore.NewClient(client, "")
}

func pluginConfigEnabled(item config.PluginInstanceConfig) bool {
	return item.Enabled != nil && *item.Enabled
}

func cpuVariant() string {
	if runtime.GOARCH != "amd64" {
		return ""
	}
	if cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512CD && cpu.X86.HasAVX512DQ && cpu.X86.HasAVX512VL {
		return "v4"
	}
	if cpu.X86.HasAVX && cpu.X86.HasAVX2 && cpu.X86.HasBMI1 && cpu.X86.HasBMI2 && cpu.X86.HasFMA {
		return "v3"
	}
	if cpu.X86.HasSSE3 && cpu.X86.HasSSSE3 && cpu.X86.HasSSE41 && cpu.X86.HasSSE42 && cpu.X86.HasPOPCNT {
		return "v2"
	}
	return "v1"
}
