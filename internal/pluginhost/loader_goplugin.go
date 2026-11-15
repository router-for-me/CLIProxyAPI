//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"fmt"
	"plugin"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// goPluginLoader opens Go-native plugins built with -buildmode=plugin.
//
// Why it exists: Go's c-shared buildmode forces Initial-Exec TLS, which
// musl's dlopen rejects (musl 5c2f46a2; golang/go#48596, golang/go#54805).
// On musl-based firmware the C ABI (c-shared) path therefore cannot load Go
// plugins, while Go's native plugin package has no such TLS restriction.
//
// The host and the plugin must be built with the same Go toolchain and an
// identical dependency graph (a go.work containing both is the supported
// layout); otherwise plugin.Open fails with a package-version mismatch.
type goPluginLoader struct {
	// open is indirected so tests can inject failures without a real .so.
	open   func(path string) (*plugin.Plugin, error)
	lookup func(p *plugin.Plugin, symbol string) (plugin.Symbol, error)
}

func newGoPluginLoader() goPluginLoader {
	return goPluginLoader{
		open:   plugin.Open,
		lookup: (*plugin.Plugin).Lookup,
	}
}

func (l goPluginLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	open := l.open
	if open == nil {
		open = plugin.Open
	}
	lookup := l.lookup
	if lookup == nil {
		lookup = (*plugin.Plugin).Lookup
	}

	p, err := open(file.Path)
	if err != nil {
		return nil, fmt.Errorf("go plugin open %s: %w (hint: host and plugin must share the same Go toolchain and dependency graph; build both within one go.work)", file.Path, err)
	}
	sym, err := lookup(p, "New")
	if err != nil {
		return nil, fmt.Errorf("go plugin %s has no New symbol: %w", file.Path, err)
	}

	// Assert the *unnamed* structural function type, not the named
	// pluginapi.GoPluginFactory: a named type and its underlying function
	// type are distinct in a cross-package type assertion, so asserting the
	// named type would reject every valid plugin entry point.
	factory, ok := sym.(func(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error))
	if !ok {
		return nil, fmt.Errorf("go plugin %s New symbol has type %T, want func(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error)", file.Path, sym)
	}

	instance := &hostCallbackInstance{}
	host.registerHostCallbackInstance(file.ID, instance)

	caller := goPluginCaller{host: host, pluginID: file.ID, instance: instance}
	inst, err := factory(caller)
	if err != nil {
		// Mark the callback instance closed and release per-plugin HTTP/stream
		// operations so a failed New() cannot leave a live callback owner that
		// later callback-ID resolution would treat as the current plugin.
		host.closeHostHTTPCallbackInstance(file.ID, instance)
		return nil, fmt.Errorf("go plugin %s New failed: %w", file.Path, err)
	}

	return &goPluginClient{inst: inst, pluginID: file.ID, instance: instance}, nil
}

// goPluginClient adapts a pluginapi.GoPlugin to the internal pluginClient
// interface used by the rest of the host (which is ABI-agnostic).
type goPluginClient struct {
	inst     pluginapi.GoPlugin
	pluginID string
	// instance backs the optional callbackInstance() hook discovered by
	// client_guard.go. Without it, model execution fails with
	// "host callback ID does not belong to the calling plugin instance".
	instance *hostCallbackInstance
}

func (c *goPluginClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if c == nil || c.inst == nil {
		return nil, fmt.Errorf("plugin client is closed")
	}
	return c.inst.Call(ctx, method, request)
}

func (c *goPluginClient) Shutdown() {
	if c == nil || c.inst == nil {
		return
	}
	c.inst.Shutdown()
	c.inst = nil
}

// callbackInstance satisfies the optional interface in client_guard.go so
// host callbacks issued during model execution resolve to this plugin.
func (c *goPluginClient) callbackInstance() *hostCallbackInstance {
	if c == nil {
		return nil
	}
	return c.instance
}

// goPluginCaller is the host-side object handed to the plugin's New. It
// forwards plugin-to-host calls to the same callFromPlugin dispatch used by
// the C ABI path, so every host callback (HTTP transport, auth store, logs,
// affinity) works identically regardless of loader.
type goPluginCaller struct {
	host     *Host
	pluginID string
	instance *hostCallbackInstance
}

func (c goPluginCaller) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if c.host == nil {
		return nil, fmt.Errorf("plugin host is unavailable")
	}
	ctx = withHostCallbackIdentity(ctx, c.pluginID, c.instance)
	return c.host.callFromPlugin(ctx, method, request)
}
