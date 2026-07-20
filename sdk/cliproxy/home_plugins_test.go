package cliproxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
	"gopkg.in/yaml.v3"
)

func TestSyncHomePluginsSkipsUnchangedSignature(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}

	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		return sdkpluginstore.PluginSyncResponse{
			SchemaVersion: sdkpluginstore.PluginSyncSchemaVersion,
			ExpiresAt:     time.Now().UTC().Add(time.Minute),
			Items:         []sdkpluginstore.PluginSyncItem{},
		}, nil
	}}
	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins() error = %v", errSync)
	}
	if !didSync || key == "" || !report.OK {
		t.Fatalf("syncHomePlugins() didSync=%v key=%q report=%+v, want reportable empty plan", didSync, key, report)
	}
	service.markHomePluginsSynced(key)

	_, gotKey, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins(second) error = %v", errSync)
	}
	if didSync || gotKey != key {
		t.Fatalf("syncHomePlugins(second) didSync=%v key=%q, want skipped same key %q", didSync, gotKey, key)
	}
}

func TestSyncHomePluginsFetchFailureReturnsFailureReport(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	wantErr := errors.New("plugin sync unavailable")
	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		return sdkpluginstore.PluginSyncResponse{}, wantErr
	}}

	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if !errors.Is(errSync, wantErr) {
		t.Fatalf("syncHomePlugins() error = %v, want %v", errSync, wantErr)
	}
	if didSync {
		t.Fatalf("syncHomePlugins() didSync = true, want false before a plan is available")
	}
	if key == "" {
		t.Fatal("syncHomePlugins() key is empty")
	}
	if report.SchemaVersion != 1 || report.Task != "plugin-sync" || report.OK || report.Error != wantErr.Error() {
		t.Fatalf("syncHomePlugins() report = %#v, want reportable fetch failure", report)
	}
}

func TestSyncHomePluginsFallsBackForUnsupportedHomeProtocol(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Dir = t.TempDir()
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		return sdkpluginstore.PluginSyncResponse{}, home.ErrPluginSyncUnsupported
	}}

	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins() error = %v", errSync)
	}
	if !didSync || key == "" {
		t.Fatalf("syncHomePlugins() didSync=%v key=%q, want legacy fallback", didSync, key)
	}
	if !report.OK || report.Task != "plugin-sync" {
		t.Fatalf("syncHomePlugins() report = %#v, want successful legacy sync", report)
	}
}

func TestSyncHomePluginsSkipsFetchWhenPluginsDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	fetchCalls := 0
	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		fetchCalls++
		return sdkpluginstore.PluginSyncResponse{}, errors.New("fetch should not be called")
	}}

	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins() error = %v", errSync)
	}
	if didSync || fetchCalls != 0 {
		t.Fatalf("syncHomePlugins() didSync=%v fetchCalls=%d, want disabled skip", didSync, fetchCalls)
	}
	if key == "" || report.Task != "plugin-sync" || !report.OK {
		t.Fatalf("disabled sync key/report = %q/%#v, want reportable disabled status", key, report)
	}
	if service.homePluginSyncKey != "" {
		t.Fatalf("homePluginSyncKey = %q, want caller to mark after reporting", service.homePluginSyncKey)
	}
}

func TestSyncHomePluginsSkipsDisabledReportWhenUnchanged(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		return sdkpluginstore.PluginSyncResponse{}, errors.New("fetch should not be called")
	}}

	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins() error = %v", errSync)
	}
	if didSync || key == "" || report.Task != "plugin-sync" || !report.OK {
		t.Fatalf("syncHomePlugins() didSync=%v key=%q report=%#v, want reportable disabled status", didSync, key, report)
	}
	service.markHomePluginsSynced(key)

	report, gotKey, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins(second) error = %v", errSync)
	}
	if didSync || gotKey != key || report.Task != "" {
		t.Fatalf("syncHomePlugins(second) didSync=%v key=%q report=%#v, want skipped unchanged disabled status", didSync, gotKey, report)
	}
}

func TestApplyHomeOverlayWarnsOnRuntimePluginSyncFailure(t *testing.T) {
	base := &config.Config{}
	base.Home.Enabled = true
	base.Plugins.Enabled = true
	service := &Service{cfg: base}

	enabled := true
	remote := &config.Config{}
	remote.Plugins.Enabled = true
	remote.Plugins.Configs = map[string]config.PluginInstanceConfig{
		"broken": {
			Enabled: &enabled,
			Raw: yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "store"},
					{
						Kind: yaml.MappingNode,
						Tag:  "!!map",
						Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "id"},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "broken"},
						},
					},
				},
			},
		},
	}

	if errApply := service.applyHomeOverlayContext(context.Background(), remote); errApply != nil {
		t.Fatalf("applyHomeOverlayContext() error = %v, want warning-only plugin sync failure", errApply)
	}
	if service.cfg == nil || !service.cfg.Home.Enabled || !service.cfg.Plugins.Enabled {
		t.Fatalf("service cfg = %+v, want applied home config despite plugin sync failure", service.cfg)
	}
	if service.homePluginSyncKey != "" {
		t.Fatalf("homePluginSyncKey = %q, want empty after plugin sync failure", service.homePluginSyncKey)
	}
}

func TestStartHomeSubscriberDoesNotPreMarkPluginSync(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.Host = "127.0.0.1"
	cfg.Home.Port = 1
	cfg.Plugins.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	service := &Service{cfg: cfg}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.startHomeSubscriber(ctx)
	defer func() {
		home.ClearCurrent()
		if service.homeCancel != nil {
			service.homeCancel()
		}
		if service.homeClient != nil {
			service.homeClient.Close()
		}
	}()

	if service.homePluginSyncKey != "" {
		t.Fatalf("homePluginSyncKey = %q, want empty before a successful plugin sync", service.homePluginSyncKey)
	}
}

func TestHomePluginSyncKeyIncludesCredentialRevision(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	first := homePluginSyncKey(cfg)
	cfg.Plugins.AuthRevision = 2
	second := homePluginSyncKey(cfg)
	if first == second {
		t.Fatalf("homePluginSyncKey() unchanged after sync revision update: %q", first)
	}
}

func TestForceHomeRuntimeConfigClearsStoreAuth(t *testing.T) {
	cfg := &config.Config{}
	cfg.Plugins.StoreAuth = []sdkpluginstore.AuthConfig{{
		Match: "https://downloads.example/", Type: sdkpluginstore.AuthTypeBearer, TokenEnv: "PLUGIN_TOKEN",
	}}
	forceHomeRuntimeConfig(cfg)
	if cfg.Plugins.StoreAuth != nil {
		t.Fatalf("Plugins.StoreAuth = %#v, want nil in Home mode", cfg.Plugins.StoreAuth)
	}
}
