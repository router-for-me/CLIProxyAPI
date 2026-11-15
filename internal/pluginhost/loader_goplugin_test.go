//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"errors"
	"plugin"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

	// Compile-time proof that the optional hook is implemented.
	var hook interface{ callbackInstance() *hostCallbackInstance } = c
	if hook == nil {
		t.Fatal("goPluginClient must implement the optional callbackInstance() hook")
	}
	if got := c.callbackInstance(); got != inst {
		t.Fatalf("callbackInstance() = %p, want %p", got, inst)
	}
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

// TestGoPluginLoaderNewErrorCleansUp exercises the loader's open/lookup seams
// without a real on-disk .so.
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
		shadow: passthroughShadow,
		open:   func(string) (*plugin.Plugin, error) { return &plugin.Plugin{}, nil },
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

// TestGoPluginLoaderRecoversInitPanic verifies that a plugin whose package
// initializer panics is reported as a load error instead of terminating the
// host process.
func TestGoPluginLoaderRecoversInitPanic(t *testing.T) {
	resetGoPluginFailures(t)
	h := &Host{loading: map[string]*pluginLoadRequest{}}
	h.loading["p"] = &pluginLoadRequest{callbackInstances: map[*hostCallbackInstance]struct{}{}}

	loader := goPluginLoader{
		shadow: passthroughShadow,
		open: func(string) (*plugin.Plugin, error) {
			panic("init exploded")
		},
		lookup: func(*plugin.Plugin, string) (plugin.Symbol, error) {
			t.Fatal("lookup must not run after a failed open")
			return nil, nil
		},
	}

	client, err := loader.Open(pluginFile{ID: "p", Path: "/nonexistent-init-panic.so"}, h)
	if err == nil {
		t.Fatal("expected the initializer panic to surface as an error")
	}
	if client != nil {
		t.Fatal("expected no client when the plugin initializer panics")
	}
	if !strings.Contains(err.Error(), "panic during plugin initialization") {
		t.Fatalf("error %q does not mention the recovered panic", err)
	}
}

// TestGoPluginLoaderRecoversFactoryPanic verifies that a panicking New()
// factory is reported as a load error and does not leave a live callback
// instance behind.
func TestGoPluginLoaderRecoversFactoryPanic(t *testing.T) {
	resetGoPluginFailures(t)
	h := &Host{loading: map[string]*pluginLoadRequest{}}
	h.loading["p"] = &pluginLoadRequest{callbackInstances: map[*hostCallbackInstance]struct{}{}}

	panicking := func(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error) {
		panic("factory exploded")
	}
	loader := goPluginLoader{
		shadow: passthroughShadow,
		open:   func(string) (*plugin.Plugin, error) { return &plugin.Plugin{}, nil },
		lookup: func(*plugin.Plugin, string) (plugin.Symbol, error) {
			return plugin.Symbol(panicking), nil
		},
	}

	client, err := loader.Open(pluginFile{ID: "p", Path: "/nonexistent-factory-panic.so"}, h)
	if err == nil {
		t.Fatal("expected the factory panic to surface as an error")
	}
	if client != nil {
		t.Fatal("expected no client when the plugin factory panics")
	}
	if !strings.Contains(err.Error(), "panic in plugin factory") {
		t.Fatalf("error %q does not mention the recovered panic", err)
	}
	for instance := range h.loading["p"].callbackInstances {
		if !instance.closed.Load() {
			t.Fatal("callback instance must be marked closed after a factory panic")
		}
	}
}

// TestGoPluginClientShutdownRecoversPanic verifies that a plugin panicking
// during shutdown cannot take the host process down, and that the client is
// detached even when the panic happens.
func TestGoPluginClientShutdownRecoversPanic(t *testing.T) {
	c := &goPluginClient{
		pluginID: "p",
		inst:     panickingPlugin{},
	}
	c.Shutdown()
	if c.inst != nil {
		t.Fatal("client must be detached after shutdown")
	}
}

// panickingPlugin panics when shut down.
type panickingPlugin struct{}

func (p panickingPlugin) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	return nil, nil
}

func (p panickingPlugin) Shutdown() {
	panic("shutdown exploded")
}

// passthroughShadow keeps the loader tests free of real shadow copies: the
// path is passed through unchanged and the release guard is a no-op.
func passthroughShadow(file pluginFile) (string, func(), error) {
	return file.Path, func() {}, nil
}

// TestGoPluginLoaderOpensFromShadowPath verifies that the plugin is opened
// from the content-addressed shadow copy instead of the original artifact.
// Go's plugin package caches by path, so opening the original file would
// make a rebuilt plugin silently keep running the previous build.
func TestGoPluginLoaderOpensFromShadowPath(t *testing.T) {
	h := &Host{loading: map[string]*pluginLoadRequest{}}
	h.loading["p"] = &pluginLoadRequest{callbackInstances: map[*hostCallbackInstance]struct{}{}}

	const shadowPath = "/tmp/cliproxy-plugin-deadbeef.so"
	openedPath := ""
	released := false
	loader := goPluginLoader{
		shadow: func(file pluginFile) (string, func(), error) {
			return shadowPath, func() { released = true }, nil
		},
		open: func(path string) (*plugin.Plugin, error) {
			openedPath = path
			return &plugin.Plugin{}, nil
		},
		lookup: func(*plugin.Plugin, string) (plugin.Symbol, error) {
			return plugin.Symbol(func(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error) {
				return &fakeGoPlugin{}, nil
			}), nil
		},
	}

	client, err := loader.Open(pluginFile{ID: "p", Path: "/data/cpa/plugins/linux/arm64/probe.so"}, h)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
	if openedPath != shadowPath {
		t.Fatalf("plugin opened from %q, want the shadow path %q", openedPath, shadowPath)
	}
	if !released {
		t.Fatal("the shadow release guard must be released after the load attempt")
	}
}

// TestGoPluginLoaderRejectsNilInstance verifies that a factory returning a
// nil plugin without an error is reported as a load failure instead of
// producing a client that fails every later call.
func TestGoPluginLoaderRejectsNilInstance(t *testing.T) {
	resetGoPluginFailures(t)
	h := &Host{loading: map[string]*pluginLoadRequest{}}
	h.loading["p"] = &pluginLoadRequest{callbackInstances: map[*hostCallbackInstance]struct{}{}}

	loader := goPluginLoader{
		shadow: passthroughShadow,
		open:   func(string) (*plugin.Plugin, error) { return &plugin.Plugin{}, nil },
		lookup: func(*plugin.Plugin, string) (plugin.Symbol, error) {
			return plugin.Symbol(func(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error) {
				return nil, nil
			}), nil
		},
	}

	client, err := loader.Open(pluginFile{ID: "p", Path: "/nonexistent-nil-instance.so"}, h)
	if err == nil {
		t.Fatal("expected a nil plugin instance to be rejected")
	}
	if client != nil {
		t.Fatal("expected no client when the factory returns no instance")
	}
	if !strings.Contains(err.Error(), "no plugin instance") {
		t.Fatalf("error %q does not explain the nil instance", err)
	}
}

// TestGoPluginLoaderDoesNotRetryFailedPath verifies that a plugin whose
// initialization failed is not opened again. Go's plugin package leaves a
// per-path placeholder behind whose channel is never closed when the
// initialization fails, so a second plugin.Open on the same path blocks
// forever.
func TestGoPluginLoaderDoesNotRetryFailedPath(t *testing.T) {
	resetGoPluginFailures(t)
	h := &Host{loading: map[string]*pluginLoadRequest{}}
	h.loading["p"] = &pluginLoadRequest{callbackInstances: map[*hostCallbackInstance]struct{}{}}

	const failedPath = "/nonexistent-retry.so"
	openCalls := 0
	loader := goPluginLoader{
		shadow: passthroughShadow,
		open: func(string) (*plugin.Plugin, error) {
			openCalls++
			panic("init exploded")
		},
		lookup: func(*plugin.Plugin, string) (plugin.Symbol, error) {
			t.Fatal("lookup must not run after a failed open")
			return nil, nil
		},
	}

	if _, err := loader.Open(pluginFile{ID: "p", Path: failedPath}, h); err == nil {
		t.Fatal("expected the initializer panic to surface as an error")
	}
	if openCalls != 1 {
		t.Fatalf("open was called %d times, want 1", openCalls)
	}

	// A second attempt must fail immediately instead of hanging in
	// plugin.Open, and it must not touch the plugin again.
	_, err := loader.Open(pluginFile{ID: "p", Path: failedPath}, h)
	if err == nil {
		t.Fatal("expected the retry of a failed plugin path to fail")
	}
	if !strings.Contains(err.Error(), "cannot be retried") {
		t.Fatalf("error %q does not explain that the path cannot be retried", err)
	}
	if openCalls != 1 {
		t.Fatalf("open was called %d times after the retry, want 1", openCalls)
	}
}

// TestGoPluginLoaderRetriesDifferentBuild verifies that a fixed plugin is
// picked up: the load path is content-addressed, so a different build has a
// different path and is not blocked by the failure of the previous one.
func TestGoPluginLoaderRetriesDifferentBuild(t *testing.T) {
	resetGoPluginFailures(t)
	h := &Host{loading: map[string]*pluginLoadRequest{}}
	h.loading["p"] = &pluginLoadRequest{callbackInstances: map[*hostCallbackInstance]struct{}{}}

	paths := []string{"/shadow/build-one.so", "/shadow/build-two.so"}
	index := 0
	loader := goPluginLoader{
		shadow: func(file pluginFile) (string, func(), error) {
			path := paths[index]
			index++
			return path, func() {}, nil
		},
		open: func(path string) (*plugin.Plugin, error) {
			if path == paths[0] {
				panic("init exploded")
			}
			return &plugin.Plugin{}, nil
		},
		lookup: func(*plugin.Plugin, string) (plugin.Symbol, error) {
			return plugin.Symbol(func(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error) {
				return &fakeGoPlugin{}, nil
			}), nil
		},
	}

	if _, err := loader.Open(pluginFile{ID: "p", Path: "/data/plugins/probe.so"}, h); err == nil {
		t.Fatal("expected the initializer panic to surface as an error")
	}
	client, err := loader.Open(pluginFile{ID: "p", Path: "/data/plugins/probe.so"}, h)
	if err != nil {
		t.Fatalf("the fixed build must load again: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client for the fixed build")
	}
}

// resetGoPluginFailures clears the process-wide record of failed plugin load
// paths. The record mirrors Go's own per-path plugin cache, so it survives
// across tests unless it is reset explicitly.
func resetGoPluginFailures(t *testing.T) {
	t.Helper()
	goPluginFailedPaths.Clear()
	t.Cleanup(func() { goPluginFailedPaths.Clear() })
}

// TestGoPluginLoaderConcurrentOpenDoesNotHang verifies that two concurrent
// loads of the same plugin path do not leave one of them waiting forever.
// Go's plugin cache blocks a concurrent open of the same path until the first
// attempt finishes, and a panicking initializer never finishes, so the
// second attempt has to share the first attempt instead of opening again.
func TestGoPluginLoaderConcurrentOpenDoesNotHang(t *testing.T) {
	resetGoPluginFailures(t)

	const sharedPath = "/nonexistent-concurrent.so"
	entered := make(chan struct{})
	var openCalls int32
	loader := goPluginLoader{
		shadow: func(file pluginFile) (string, func(), error) {
			return sharedPath, func() {}, nil
		},
		open: func(string) (*plugin.Plugin, error) {
			if atomic.AddInt32(&openCalls, 1) == 1 {
				// Keep the first attempt in flight long enough for the second
				// one to overlap with it. The sleep only creates the overlap;
				// the assertions rely on the reported error, not on timing.
				close(entered)
				time.Sleep(50 * time.Millisecond)
			}
			panic("init exploded")
		},
		lookup: func(*plugin.Plugin, string) (plugin.Symbol, error) {
			t.Fatal("lookup must not run after a failed open")
			return nil, nil
		},
	}

	results := make(chan error, 2)
	open := func() {
		_, err := loader.Open(pluginFile{ID: "p", Path: sharedPath}, &Host{})
		results <- err
	}

	go open()
	<-entered
	go open()

	deadline := time.After(20 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("expected every concurrent open to fail")
			}
		case <-deadline:
			t.Fatal("a concurrent plugin open blocked instead of reporting the failure")
		}
	}
}
