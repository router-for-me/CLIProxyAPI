//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"plugin"
	"sync"

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
//
// Unlike the C ABI loader, a Go-native plugin is loaded into the host
// process: plugin.Open runs the plugin package initializer in-process, so a
// plugin that crashes the Go runtime (for example an illegal instruction)
// takes the host down with it. Panics raised by the initializer, by the New
// factory and by Shutdown are recovered and reported as ordinary errors.
type goPluginLoader struct {
	// open is indirected so tests can inject failures without a real .so.
	open   func(path string) (*plugin.Plugin, error)
	lookup func(p *plugin.Plugin, symbol string) (plugin.Symbol, error)
	// shadow resolves the content-addressed path a plugin is opened from.
	// It is indirected for the same reason as open.
	shadow func(file pluginFile) (string, func(), error)
}

func newGoPluginLoader() goPluginLoader {
	return goPluginLoader{
		open:   plugin.Open,
		lookup: (*plugin.Plugin).Lookup,
		shadow: acquireShadowPluginLoad,
	}
}

// goPluginFailedPaths remembers the load paths of plugins that failed to
// initialize.
//
// Go's plugin package stores a per-path placeholder before it runs the
// plugin initializer and only closes that placeholder's loaded channel after
// the initialization succeeded. A failed or panicking initialization leaves
// a placeholder whose channel is never closed, so a second plugin.Open on the
// same path blocks forever. Remembering the failed path keeps exactly that
// broken build from being retried.
//
// The load path is content-addressed, so a plugin that is fixed gets a
// different digest and therefore a different path: only the unchanged broken
// build stays blocked, and a host restart is enough to pick up the fix.
var goPluginFailedPaths sync.Map

// goPluginOpenAttempts holds the in-flight plugin.Open attempts per load
// path. Go's plugin package blocks a concurrent open of the same path until
// the first attempt finishes, and a panicking initializer never finishes, so
// concurrent callers have to share the first attempt instead of opening the
// path again.
var goPluginOpenAttempts sync.Map

// goPluginFallbackPaths remembers the load paths whose local shadow copy
// could not be opened but that were loaded from the executable fallback.
// Such a path stays usable: later loads go straight to the fallback instead
// of being rejected as a failed path.
var goPluginFallbackPaths sync.Map

// goPluginOpenAttempt is a single plugin.Open attempt and its outcome.
type goPluginOpenAttempt struct {
	done   chan struct{}
	plugin *plugin.Plugin
	err    error
}

// openWithFallback opens the plugin from the local shadow copy and falls back
// to the executable copy in the cache directory when the local copy cannot be
// opened, for example because the plugin directory is mounted noexec.
//
// A panic raised by the plugin initializer is fatal: Go's plugin cache keeps
// the path poisoned, so the path is recorded as failed and never opened
// again. Any other failure may be caused by the shadow copy itself, so the
// executable fallback is tried before the path is given up on.
func (l goPluginLoader) openWithFallback(file pluginFile, open func(path string) (*plugin.Plugin, error), loadPath string, releaseGuard func()) (*plugin.Plugin, error) {
	if _, needsFallback := goPluginFallbackPaths.Load(loadPath); needsFallback {
		// The local copy was already found to be unusable, and the plugin was
		// loaded successfully from the executable fallback before.
		p, errFallback := l.openFallbackShadow(file, open, loadPath)
		if errFallback != nil {
			goPluginFailedPaths.Store(loadPath, struct{}{})
			return nil, fmt.Errorf("the executable fallback failed: %w", errFallback)
		}
		return p, nil
	}

	p, err := openGoPluginSerialized(open, loadPath)
	releaseGuard()
	if err == nil {
		return p, nil
	}

	var panicErr *goPluginPanicError
	if errors.As(err, &panicErr) {
		// openGoPluginSerialized already recorded the path as failed.
		return nil, err
	}

	// The local shadow copy may live on a noexec mount, in which case the
	// plugin cannot be loaded from it. The C ABI loader falls back to an
	// executable copy in the cache directory; do the same here.
	goPluginFallbackPaths.Store(loadPath, struct{}{})
	fallbackPlugin, errFallback := l.openFallbackShadow(file, open, loadPath)
	if errFallback != nil {
		goPluginFailedPaths.Store(loadPath, struct{}{})
		return nil, fmt.Errorf("%w (the executable fallback failed as well: %v)", err, errFallback)
	}
	return fallbackPlugin, nil
}

// openFallbackShadow loads the plugin from an executable copy in the cache
// directory. It is the Go-native counterpart of the C ABI loader's fallback
// for plugin directories that are mounted noexec.
func (l goPluginLoader) openFallbackShadow(file pluginFile, open func(path string) (*plugin.Plugin, error), failedLoadPath string) (*plugin.Plugin, error) {
	fallbackPath, releaseFallbackGuard, errFallback := acquireShadowPluginFallbackLoad(file, failedLoadPath)
	if errFallback != nil {
		return nil, errFallback
	}
	fallbackPath = canonicalizePluginPath(fallbackPath)
	p, errOpen := openGoPluginSerialized(open, fallbackPath)
	releaseFallbackGuard()
	return p, errOpen
}

// openGoPluginSerialized opens the plugin at loadPath, making sure that only
// one attempt per path is in flight. Concurrent callers wait for the first
// attempt and observe its result, so a plugin whose initializer panics can
// never leave another caller waiting on Go's plugin cache forever.
//
// A failed attempt is recorded before the shared entry is released, so a
// caller that arrives in between observes the failure instead of opening the
// poisoned path again.
func openGoPluginSerialized(open func(path string) (*plugin.Plugin, error), loadPath string) (*plugin.Plugin, error) {
	attempt := &goPluginOpenAttempt{done: make(chan struct{})}
	actual, loaded := goPluginOpenAttempts.LoadOrStore(loadPath, attempt)
	att := actual.(*goPluginOpenAttempt)
	if loaded {
		<-att.done
		return att.plugin, att.err
	}
	att.plugin, att.err = openGoPluginGuarded(open, loadPath)
	if att.err != nil {
		var panicErr *goPluginPanicError
		if errors.As(att.err, &panicErr) {
			// A panicking initializer leaves Go's plugin cache poisoned for
			// this path, so the path must never be opened again.
			goPluginFailedPaths.Store(loadPath, struct{}{})
		}
	}
	close(att.done)
	goPluginOpenAttempts.Delete(loadPath)
	return att.plugin, att.err
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
			err = &goPluginPanicError{value: recovered}
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

// canonicalizePluginPath resolves a plugin path the way Go's plugin package
// does before it caches a plugin: the path is made absolute and its symlinks
// are resolved. The failure and attempt records are keyed by this value, so
// they match Go's own plugin cache and a path reached through different
// spellings refers to the same cache entry.
func canonicalizePluginPath(path string) string {
	if path == "" {
		return ""
	}
	if absolute, errAbs := filepath.Abs(path); errAbs == nil {
		path = absolute
	}
	if resolved, errResolve := filepath.EvalSymlinks(path); errResolve == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

// goPluginPanicError reports a panic that was recovered from plugin code.
type goPluginPanicError struct {
	value any
}

func (e *goPluginPanicError) Error() string {
	return fmt.Sprintf("panic during plugin initialization: %v", e.value)
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
	shadow := l.shadow
	if shadow == nil {
		shadow = acquireShadowPluginLoad
	}

	// Go's plugin package caches by path, so the plugin is opened from the
	// same content-addressed shadow copy the C ABI loader uses. Every distinct
	// plugin build therefore gets its own path and its own plugin image, and
	// the original artifact stays replaceable.
	loadPath, releaseGuard, errShadow := shadow(file)
	if errShadow != nil {
		return nil, errShadow
	}
	loadPath = canonicalizePluginPath(loadPath)
	if _, failed := goPluginFailedPaths.Load(loadPath); failed {
		releaseGuard()
		return nil, fmt.Errorf("go plugin %s previously failed to initialize and cannot be retried in this process; fix the plugin and restart the host", file.Path)
	}

	p, err := l.openWithFallback(file, open, loadPath, releaseGuard)
	if err != nil {
		return nil, fmt.Errorf("go plugin open %s: %w (hint: build the plugin with -buildmode=plugin, and make sure the host and the plugin share the same Go toolchain, GOOS/GOARCH, build tags and dependency graph; building both inside one go.work is the supported layout)", file.Path, err)
	}
	if p == nil {
		goPluginFailedPaths.Store(loadPath, struct{}{})
		return nil, fmt.Errorf("go plugin open %s returned no plugin handle", file.Path)
	}
	sym, err := lookup(p, "New")
	if err != nil {
		goPluginFailedPaths.Store(loadPath, struct{}{})
		return nil, fmt.Errorf("go plugin %s has no New symbol: %w (hint: the plugin must export New(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error))", file.Path, err)
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
	if inst == nil {
		host.closeHostHTTPCallbackInstance(file.ID, instance)
		return nil, fmt.Errorf("go plugin %s New returned no plugin instance", file.Path)
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
