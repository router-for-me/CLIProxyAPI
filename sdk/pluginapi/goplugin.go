package pluginapi

import "context"

// GoPlugin is the interface implemented by plugins loaded through Go's native
// plugin package (-buildmode=plugin), as an alternative to the C ABI
// (-buildmode=c-shared) path.
//
// The wire shape deliberately mirrors the C ABI: each call carries a method
// name and a JSON-encoded request, and returns a JSON-encoded response. Keeping
// the two ABIs byte-compatible means the rest of the plugin host (adapters,
// protocol translation, model registry) does not need to know which loader
// produced the client.
//
// Why this exists: Go's c-shared buildmode forces Initial-Exec TLS, which
// musl's dlopen rejects (musl 5c2f46a2; golang/go#48596, golang/go#54805).
// On musl-based firmware the C ABI therefore cannot load Go plugins, while
// Go's native plugin mechanism has no such restriction.
type GoPlugin interface {
	// Call invokes a plugin RPC method. request and response are JSON bytes
	// whose schema is defined per method, matching the C ABI envelopes.
	Call(ctx context.Context, method string, request []byte) ([]byte, error)

	// Shutdown releases plugin-side resources.
	//
	// A Go plugin cannot be unloaded from a running process, so replacing a
	// plugin does not require a host restart: the host opens the replacement
	// from a content-addressed copy of the new build, which Go's plugin
	// package loads as a separate image, and retires the previous instance
	// once the replacement is in place. The retired instance is released
	// through this method.
	Shutdown()
}

// GoPluginCaller is the host-side surface exposed to a plugin for calling
// back into the host (for example HTTP transport, auth storage, logging).
// It is the Go-native counterpart of the C ABI's host_api.call function
// pointer and routes through the same host callback dispatch.
//
// The error contract differs from the C ABI: a failing host callback is
// reported as a Go error, not as an RPC error envelope. Plugins must check
// the returned error instead of decoding an error envelope from the response
// bytes.
//
// Callbacks are dispatched on the goroutine of the caller. The host does not
// serialize them per plugin instance, so a plugin that calls back from
// several goroutines has to synchronize its own access to host state.
type GoPluginCaller interface {
	Call(ctx context.Context, method string, request []byte) ([]byte, error)
}

// GoPluginFactory is the signature of the "New" symbol a Go plugin exports.
//
// The host looks up a plain function named "New" and asserts against the
// unnamed structural type func(GoPluginCaller) (GoPlugin, error), not against
// this named type: a named type and its underlying function type are distinct
// types in a cross-package type assertion. GoPluginFactory is therefore
// documentation of the required signature rather than something to assert on.
type GoPluginFactory func(host GoPluginCaller) (GoPlugin, error)
