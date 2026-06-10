package cliproxy

import (
	"context"
	"reflect"
	"testing"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestBuilderBuildInjectsPluginHostScheduler(t *testing.T) {
	host := pluginhost.New()
	service, errBuild := NewBuilder().
		WithConfig(&config.Config{AuthDir: t.TempDir()}).
		WithConfigPath(t.TempDir() + "/config.yaml").
		WithPluginHost(host).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}

	got := pluginSchedulerFromManager(t, service.coreManager)
	if got != host {
		t.Fatalf("plugin scheduler = %p, want host %p", got, host)
	}
	gotServerToolHandler := pluginServerToolHandlerFromManager(t, service.coreManager)
	if gotServerToolHandler != host {
		t.Fatalf("plugin server tool handler = %p, want host %p", gotServerToolHandler, host)
	}
}

func TestServiceSyncPluginRuntimeConfigInjectsPluginHostScheduler(t *testing.T) {
	host := pluginhost.New()
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  host,
	}

	if ok := service.syncPluginRuntimeConfig(context.Background()); !ok {
		t.Fatal("syncPluginRuntimeConfig() = false, want true")
	}

	got := pluginSchedulerFromManager(t, service.coreManager)
	if got != host {
		t.Fatalf("plugin scheduler = %p, want host %p", got, host)
	}
	gotServerToolHandler := pluginServerToolHandlerFromManager(t, service.coreManager)
	if gotServerToolHandler != host {
		t.Fatalf("plugin server tool handler = %p, want host %p", gotServerToolHandler, host)
	}
}

func TestServiceSyncPluginRuntimeConfigClearsPluginSchedulerWithoutHost(t *testing.T) {
	host := pluginhost.New()
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  host,
	}
	service.coreManager.SetPluginScheduler(host)
	service.pluginHost = nil

	if ok := service.syncPluginRuntimeConfig(context.Background()); ok {
		t.Fatal("syncPluginRuntimeConfig() = true, want false")
	}

	got := pluginSchedulerFromManager(t, service.coreManager)
	if got != nil {
		t.Fatalf("plugin scheduler = %p, want nil", got)
	}
	gotServerToolHandler := pluginServerToolHandlerFromManager(t, service.coreManager)
	if gotServerToolHandler != nil {
		t.Fatalf("plugin server tool handler = %p, want nil", gotServerToolHandler)
	}
}

func pluginSchedulerFromManager(t *testing.T, manager *coreauth.Manager) *pluginhost.Host {
	t.Helper()
	if manager == nil {
		t.Fatal("manager = nil")
	}
	value := reflect.ValueOf(manager).Elem().FieldByName("pluginScheduler")
	if !value.IsValid() {
		t.Fatal("pluginScheduler field not found")
	}
	scheduler := reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem().Interface()
	if scheduler == nil {
		return nil
	}
	host, ok := scheduler.(*pluginhost.Host)
	if !ok {
		t.Fatalf("pluginScheduler type = %T, want *pluginhost.Host", scheduler)
	}
	return host
}

func pluginServerToolHandlerFromManager(t *testing.T, manager *coreauth.Manager) *pluginhost.Host {
	t.Helper()
	if manager == nil {
		t.Fatal("manager = nil")
	}
	value := reflect.ValueOf(manager).Elem().FieldByName("pluginServerToolHandler")
	if !value.IsValid() {
		t.Fatal("pluginServerToolHandler field not found")
	}
	handler := reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem().Interface()
	if handler == nil {
		return nil
	}
	host, ok := handler.(*pluginhost.Host)
	if !ok {
		t.Fatalf("pluginServerToolHandler type = %T, want *pluginhost.Host", handler)
	}
	return host
}
