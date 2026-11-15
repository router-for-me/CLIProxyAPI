package pluginhost

import (
	"context"
	"errors"
	"plugin"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// fakeGoPlugin is a deterministic stand-in for a real Go-native plugin,
// so loader selection and client behavior can be tested without plugin.Open.
type fakeGoPlugin struct {
	gotMethod string
	out       []byte
}

func (f *fakeGoPlugin) Call(_ context.Context, method string, _ []byte) ([]byte, error) {
	f.gotMethod = method
	return f.out, nil
}

func (f *fakeGoPlugin) Shutdown() {}

func TestGoPluginClientCallDelegates(t *testing.T) {
	fp := &fakeGoPlugin{out: []byte("ok")}
	c := &goPluginClient{inst: fp}

	got, err := c.Call(context.Background(), "plugin.echo", nil)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("Call = %q, want %q", got, "ok")
	}
	if fp.gotMethod != "plugin.echo" {
		t.Fatalf("delegated method = %q, want %q", fp.gotMethod, "plugin.echo")
	}
}

func TestGoPluginClientExposesCallbackInstance(t *testing.T) {
	// client_guard.go discovers this through an optional interface; omitting
	// the hook makes model execution fail with
	// "host callback ID does not belong to the calling plugin instance".
	inst := &hostCallbackInstance{}
	c := &goPluginClient{instance: inst}

	var hook interface{ callbackInstance() *hostCallbackInstance }
	if !implementsCallbackInstance(c) {
		t.Fatal("goPluginClient must implement the optional callbackInstance() hook")
	}
	_ = hook
	if got := c.callbackInstance(); got != inst {
		t.Fatalf("callbackInstance() = %p, want %p", got, inst)
	}
}

func implementsCallbackInstance(c *goPluginClient) bool {
	var _ interface{ callbackInstance() *hostCallbackInstance } = c
	return true
}

func TestGoPluginClientNilSafe(t *testing.T) {
	// A nil client must return an error rather than panic; the host calls
	// Shutdown/Call on retired clients during plugin replacement.
	var c *goPluginClient
	if _, err := c.Call(context.Background(), "m", nil); err == nil {
		t.Fatal("nil client Call must error")
	}
	c.Shutdown() // must not panic
}

func TestGoPluginClientShutdownClearsInstance(t *testing.T) {
	fp := &fakeGoPlugin{}
	c := &goPluginClient{inst: fp}
	c.Shutdown()
	if c.inst != nil {
		t.Fatal("Shutdown must drop the plugin reference")
	}
}

// --- loader selection (Task 4) ---

func newHostWithABI(abi string) *Host {
	h := &Host{}
	if abi != "" {
		h.runtimeConfig = &config.Config{}
		h.runtimeConfig.Plugins.ABI = abi
	}
	return h
}

func TestABISwitchDefaultIsDynamicLibrary(t *testing.T) {
	// Empty mode must resolve to the existing C ABI loader so current users
	// see zero behavior change.
	got := abiSwitchLoader{}.loaderFor(newHostWithABI(""))
	if _, ok := got.(dynamicLibraryLoader); !ok {
		t.Fatalf("default loader = %T, want dynamicLibraryLoader", got)
	}
}

func TestABISwitchGoSelectsGoLoader(t *testing.T) {
	got := abiSwitchLoader{}.loaderFor(newHostWithABI("go"))
	if _, ok := got.(goPluginLoader); !ok {
		t.Fatalf("go loader = %T, want goPluginLoader", got)
	}
}

func TestABISwitchUnknownValueFallsBack(t *testing.T) {
	// A typo ("golang") must not silently switch mechanisms; fall back to the
	// C ABI default rather than panicking or selecting the Go path.
	for _, v := range []string{"golang", "GO-", "c-abi", "c"} {
		got := abiSwitchLoader{}.loaderFor(newHostWithABI(v))
		if _, ok := got.(dynamicLibraryLoader); !ok {
			t.Fatalf("value %q resolved to %T, want dynamicLibraryLoader fallback", v, got)
		}
	}
}

func TestABISwitchCaseInsensitive(t *testing.T) {
	got := abiSwitchLoader{}.loaderFor(newHostWithABI("GO"))
	if _, ok := got.(goPluginLoader); !ok {
		t.Fatalf("uppercase GO resolved to %T, want goPluginLoader", got)
	}
}

// fakeGoPluginFactory returns a controllable result so the loader's
// open/lookup seams can be exercised without a real on-disk .so.
func TestGoPluginLoaderNewErrorCleansUp(t *testing.T) {
	// Review Focus: a plugin whose New factory errors must not leave a
	// callback instance that later callback-ID resolution treats as a live
	// owner. registerHostCallbackInstance only records into an existing
	// load request, so create one the same way the host's load path does.
	h := &Host{loading: map[string]*pluginLoadRequest{}}
	h.loading["p"] = &pluginLoadRequest{callbackInstances: map[*hostCallbackInstance]struct{}{}}

	failing := func(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error) {
		return nil, errFactoryBoom
	}
	loader := goPluginLoader{
		open: func(string) (*plugin.Plugin, error) { return &plugin.Plugin{}, nil },
		lookup: func(*plugin.Plugin, string) (plugin.Symbol, error) {
			return plugin.Symbol(failing), nil
		},
	}

	if _, err := loader.Open(pluginFile{ID: "p", Path: "/nonexistent.so"}, h); err == nil {
		t.Fatal("expected factory error to propagate")
	}
	// Cleanup semantics match the C ABI path: the instance stays in the load
	// request map but is marked closed (markPluginLoadClosedLocked), so later
	// callback-ID resolution rejects it rather than treating it as live.
	var marked *hostCallbackInstance
	for inst := range h.loading["p"].callbackInstances {
		marked = inst
	}
	if marked == nil {
		t.Fatal("expected the failed load's instance to be recorded")
	}
	if !marked.closed.Load() {
		t.Fatal("callback instance must be marked closed after New failure")
	}
}

var errFactoryBoom = errors.New("boom")
