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

// goPluginFailedPaths remembers the load paths whose plugin.Open failed.
//
// Go's plugin package stores a per-path placeholder before it runs the
// plugin initializer and closes that placeholder's loaded channel only after
// the plugin was fully loaded. Both a panicking initializer and a symbol
// that cannot be resolved return before that channel is closed, so a second
// plugin.Open on the same path blocks forever. The judgement is therefore
// not "did the initializer panic" but "did plugin.Open fail": every failure
// of plugin.Open marks the path as unusable for the rest of the process.
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
// could not be opened but that are loaded from the executable fallback.
// Such a path stays usable: later loads go straight to the fallback instead
// of being rejected as a failed path.
var goPluginFallbackPaths sync.Map

// goPluginFallbackPrepareError reports a failure of the executable fallback
// that happened before plugin.Open was reached, for example because the
// cache directory cannot be written.
//
// Go's plugin cache was not touched in that case, so the path stays
// retryable: a later load tries the fallback again instead of failing
// immediately, even after the filesystem problem is fixed.
type goPluginFallbackPrepareError struct {
	err error
}

func (e *goPluginFallbackPrepareError) Error() string {
	if e == nil || e.err == nil {
		return "the executable fallback could not be prepared"
	}
	return e.err.Error()
}

func (e *goPluginFallbackPrepareError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

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
// This function owns the shadow coordination guard: the guard is released
// before the fallback lock is taken and on every return path. The primary
// shadow lock and the executable fallback lock are both derived from the
// digest of the plugin's directory and therefore live in the same lock file.
// Taking the fallback lock while the primary lock is still held keeps the
// lock file descriptor open for the rest of the process and lets the
// exclusive lock of the shadow pruning on the same file keep failing.
//
// A failure of plugin.Open leaves Go's plugin cache poisoned for that path,
// so the path is recorded as failed and never opened again. A failure other
// than a panicking initializer may be caused by the shadow copy itself, so
// the executable fallback is tried before the path is given up on. A failure
// of the fallback that happened before plugin.Open was reached does not
// poison anything: the path stays retryable.
func (l goPluginLoader) openWithFallback(file pluginFile, open func(path string) (*plugin.Plugin, error), loadPath string, releaseGuard func()) (*plugin.Plugin, error) {
	// The release closure is idempotent, but the guard is released exactly
	// once here: the explicit releases below and the deferred backstop share
	// a single sync.Once.
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(releaseGuard) }
	defer release()

	if _, needsFallback := goPluginFallbackPaths.Load(loadPath); needsFallback {
		// The local copy was already found to be unusable and the plugin is
		// loaded from the executable fallback instead. Release the primary
		// guard first: the fallback lock lives in the same lock file.
		//
		// The local path is not checked against the failed paths here: it
		// may have failed to open, which is exactly why the fallback is
		// used, and it is never opened again.
		release()
		p, errFallback := l.openFallbackShadow(file, open, loadPath)
		if errFallback != nil {
			return nil, fmt.Errorf("%s: %w", goPluginFallbackFailurePhrase(errFallback), errFallback)
		}
		return p, nil
	}

	if _, failed := goPluginFailedPaths.Load(loadPath); failed {
		return nil, &goPluginPreviouslyFailedError{path: file.Path}
	}

	p, err := openGoPluginSerialized(open, loadPath)
	// The primary attempt is over. Release the primary guard before the
	// fallback lock can be taken.
	release()
	if err == nil {
		return p, nil
	}

	var failedErr *goPluginPreviouslyFailedError
	if errors.As(err, &failedErr) {
		// The path is known to be unusable, so the fallback is not tried
		// either: it carries the same broken build.
		return nil, err
	}

	var panicErr *goPluginPanicError
	if errors.As(err, &panicErr) {
		// A panicking initializer leaves Go's plugin cache poisoned for
		// this path, so the path must never be opened again. The executable
		// copy carries the same initializer, so it is not tried either.
		return nil, err
	}

	// The local shadow copy may live on a noexec mount, in which case the
	// plugin cannot be loaded from it. The C ABI loader falls back to an
	// executable copy in the cache directory; do the same here.
	//
	// openGoPluginSerialized recorded the local path as failed: its
	// plugin.Open failed, so a retry of it would block in Go's plugin cache.
	// The fallback marker makes the next load go straight to the fallback,
	// so the poisoned local path is never opened again.
	goPluginFallbackPaths.Store(loadPath, struct{}{})
	fallbackPlugin, errFallback := l.openFallbackShadow(file, open, loadPath)
	if errFallback != nil {
		return nil, fmt.Errorf("%w (%s as well: %v)", err, goPluginFallbackFailurePhrase(errFallback), errFallback)
	}
	return fallbackPlugin, nil
}

// goPluginFallbackFailurePhrase describes a failed executable fallback.
//
// A preparation failure is reported with its own wording: it happened
// before plugin.Open was reached, so Go's plugin cache was not touched and
// the path stays retryable.
func goPluginFallbackFailurePhrase(err error) string {
	var prepareErr *goPluginFallbackPrepareError
	if errors.As(err, &prepareErr) {
		return "the executable fallback preparation failed"
	}
	return "the executable fallback failed"
}

// openFallbackShadow loads the plugin from an executable copy in the cache
// directory. It is the Go-native counterpart of the C ABI loader's fallback
// for plugin directories that are mounted noexec.
//
// A failure of the preparation is reported as a *goPluginFallbackPrepareError:
// plugin.Open was not reached, so Go's plugin cache was not touched and the
// path stays retryable. A failure of the open itself is reported as an
// ordinary error; the failed path is recorded by openGoPluginSerialized.
func (l goPluginLoader) openFallbackShadow(file pluginFile, open func(path string) (*plugin.Plugin, error), failedLoadPath string) (*plugin.Plugin, error) {
	fallbackPath, releaseFallbackGuard, errFallback := acquireShadowPluginFallbackLoad(file, failedLoadPath)
	if errFallback != nil {
		return nil, &goPluginFallbackPrepareError{err: errFallback}
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
// A path whose plugin.Open already failed is never opened again: Go's plugin
// cache keeps a placeholder whose channel is never closed for it, so a second
// open of the same path blocks forever.
//
// A failed attempt is recorded before the shared entry is released, so a
// caller that arrives in between observes the failure instead of opening the
// poisoned path again.
func openGoPluginSerialized(open func(path string) (*plugin.Plugin, error), loadPath string) (*plugin.Plugin, error) {
	if _, failed := goPluginFailedPaths.Load(loadPath); failed {
		return nil, &goPluginPreviouslyFailedError{path: loadPath}
	}
	attempt := &goPluginOpenAttempt{done: make(chan struct{})}
	actual, loaded := goPluginOpenAttempts.LoadOrStore(loadPath, attempt)
	att := actual.(*goPluginOpenAttempt)
	if loaded {
		<-att.done
		return att.plugin, att.err
	}
	att.plugin, att.err = openGoPluginGuarded(open, loadPath)
	if att.err != nil {
		// Every failure of plugin.Open poisons the path in Go's plugin
		// cache: the placeholder's channel is closed only after a fully
		// loaded plugin. It does not matter whether the initializer panicked
		// or whether a symbol could not be resolved, both return before the
		// channel is closed.
		goPluginFailedPaths.Store(loadPath, struct{}{})
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

// goPluginPreviouslyFailedError reports a load path whose plugin.Open
// already failed in this process. The error is reported as it is, without the
// buildmode hint of a regular load failure: rebuilding the plugin does not
// unblock the poisoned path, only a host restart does.
type goPluginPreviouslyFailedError struct {
	path string
}

func (e *goPluginPreviouslyFailedError) Error() string {
	return fmt.Sprintf("go plugin %s previously failed to initialize and cannot be retried in this process; fix the plugin and restart the host", e.path)
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

	// openWithFallback owns the shadow coordination guard: it releases the
	// guard before the fallback lock is taken, because both locks live in the
	// same lock file.
	p, err := l.openWithFallback(file, open, loadPath, releaseGuard)
	if err != nil {
		var failedErr *goPluginPreviouslyFailedError
		if errors.As(err, &failedErr) {
			return nil, failedErr
		}
		return nil, fmt.Errorf("go plugin open %s: %w (hint: build the plugin with -buildmode=plugin, and make sure the host and the plugin share the same Go toolchain, GOOS/GOARCH, build tags and dependency graph; building both inside one go.work is the supported layout)", file.Path, err)
	}
	if p == nil {
		// The open succeeded, so Go's plugin cache holds a valid entry for
		// the load path and a later load can reuse it. Only a failure of
		// plugin.Open itself makes a path unusable.
		return nil, fmt.Errorf("go plugin open %s returned no plugin handle", file.Path)
	}
	sym, err := lookup(p, "New")
	if err != nil {
		// The plugin was opened successfully, so the load path stays usable:
		// the failure is in the plugin, not in the open.
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
