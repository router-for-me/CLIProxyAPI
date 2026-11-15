//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"fmt"
	"plugin"

	log "github.com/sirupsen/logrus"

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

// openGoPluginGuarded runs plugin.Open behind a recovery boundary.
//
// plugin.Open executes the plugin package initializer inside the host
// process. A panicking initializer would otherwise tear down the whole
// server, because the loader runs in the goroutine started by
// Host.startPluginLoad. The C ABI path has no equivalent risk because dlopen
// never runs plugin Go code.
func openGoPluginGuarded(open func(path string) (*plugin.Plugin, error), path string) (p *plugin.Plugin, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			p = nil
			err = fmt.Errorf("panic during plugin initialization: %v", recovered)
		}
	}()
	return open(path)
}

// callGoPluginFactoryGuarded runs the plugin factory behind a recovery
// boundary so a panicking New() is reported as a load error instead of
// terminating the host process.
func callGoPluginFactoryGuarded(factory func(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error), caller pluginapi.GoPluginCaller) (inst pluginapi.GoPlugin, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			inst = nil
			err = fmt.Errorf("panic in plugin factory: %v", recovered)
		}
	}()
	return factory(caller)
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

	p, err := openGoPluginGuarded(open, file.Path)
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
	inst, err := callGoPluginFactoryGuarded(factory, caller)
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

// Shutdown releases the plugin-side resources and detaches the client.
//
// Like the C ABI client, the host must not issue calls concurrently with
// (or after) retirement; the plugin host serializes plugin lifecycle, so
// the instance field is not guarded by a mutex here either.
//
// The plugin call is isolated: a plugin that panics while shutting down must
// not take the host process down with it.
func (c *goPluginClient) Shutdown() {
	if c == nil || c.inst == nil {
		return
	}
	inst := c.inst
	c.inst = nil
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("pluginhost: plugin %s panicked during shutdown: %v", c.pluginID, recovered)
		}
	}()
	inst.Shutdown()
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
