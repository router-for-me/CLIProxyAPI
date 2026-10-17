package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type nativeHandlerFunc func(context.Context, pluginapi.NativeRequest) (pluginapi.NativeResponse, error)

func (f nativeHandlerFunc) HandleNative(ctx context.Context, req pluginapi.NativeRequest) (pluginapi.NativeResponse, error) {
	return f(ctx, req)
}

type nativePluginDouble struct {
	routes     []pluginapi.NativeRoute
	request    pluginapi.NativeRequest
	response   pluginapi.NativeResponse
	err        error
	panicValue any
}

func (p *nativePluginDouble) RegisterNative(_ context.Context, _ pluginapi.NativeRegistrationRequest) (pluginapi.NativeRegistrationResponse, error) {
	return pluginapi.NativeRegistrationResponse{Routes: p.routes}, nil
}

func (p *nativePluginDouble) HandleNative(_ context.Context, req pluginapi.NativeRequest) (pluginapi.NativeResponse, error) {
	p.request = req
	if p.panicValue != nil {
		panic(p.panicValue)
	}
	return p.response, p.err
}

func TestRegisterNativeRoutesValidatesExactV1PathsAndPriority(t *testing.T) {
	mk := func(body string) pluginapi.NativeRoute {
		return pluginapi.NativeRoute{Method: http.MethodPost, Path: "/v1/plugin/run", Handler: nativeHandlerFunc(func(context.Context, pluginapi.NativeRequest) (pluginapi.NativeResponse, error) {
			return pluginapi.NativeResponse{Body: []byte(body)}, nil
		})}
	}
	high := &nativePluginDouble{routes: []pluginapi.NativeRoute{
		mk("high"),
		{Method: http.MethodPost, Path: "/v1/models/*model", Handler: nativeHandlerFunc(func(context.Context, pluginapi.NativeRequest) (pluginapi.NativeResponse, error) {
			return pluginapi.NativeResponse{}, nil
		})},
		{Method: http.MethodPost, Path: "/v1/../escape", Handler: nativeHandlerFunc(func(context.Context, pluginapi.NativeRequest) (pluginapi.NativeResponse, error) {
			return pluginapi.NativeResponse{}, nil
		})},
		{Method: "BAD METHOD", Path: "/v1/bad", Handler: nativeHandlerFunc(func(context.Context, pluginapi.NativeRequest) (pluginapi.NativeResponse, error) {
			return pluginapi.NativeResponse{}, nil
		})},
	}}
	low := &nativePluginDouble{routes: []pluginapi.NativeRoute{mk("low")}}
	host := newHostWithRecords(
		capabilityRecord{id: "low", priority: 1, plugin: pluginapi.Plugin{SchemaVersion: 7, Capabilities: pluginapi.Capabilities{NativeAPI: low}}},
		capabilityRecord{id: "high", priority: 10, plugin: pluginapi.Plugin{SchemaVersion: 7, Capabilities: pluginapi.Capabilities{NativeAPI: high}}},
	)
	host.RegisterNativeRoutes(context.Background(), nil)
	recorder := httptest.NewRecorder()
	if !host.ServeNativeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/plugin/run", nil)) {
		t.Fatal("route not served")
	}
	if recorder.Body.String() != "high" {
		t.Fatalf("body = %q, want high", recorder.Body.String())
	}
}

func TestRegisterNativeRoutesAcceptsOnlyCORSAllowedMethods(t *testing.T) {
	// Browsers only send methods the CORS response allows, so other methods could never be called cross-origin.
	for _, method := range []string{http.MethodOptions, "PROPFIND"} {
		t.Run(method, func(t *testing.T) {
			plugin := &nativePluginDouble{routes: []pluginapi.NativeRoute{{Method: method, Path: "/v1/plugin/run"}}}
			plugin.routes[0].Handler = plugin
			host := newHostWithRecords(capabilityRecord{id: "native", plugin: pluginapi.Plugin{SchemaVersion: 7, Capabilities: pluginapi.Capabilities{NativeAPI: plugin}}})
			host.RegisterNativeRoutes(context.Background(), nil)
			if host.HasNativeRoute("/v1/plugin/run") {
				t.Fatalf("%s native route was registered", method)
			}
		})
	}
}

func TestServeNativeHTTPPreservesResponseAndStripsCredentials(t *testing.T) {
	plugin := &nativePluginDouble{
		routes:   []pluginapi.NativeRoute{{Method: http.MethodPost, Path: "/v1/plugin/run", Handler: nil}},
		response: pluginapi.NativeResponse{StatusCode: http.StatusTeapot, Headers: http.Header{"Retry-After": []string{"7"}, "Content-Type": []string{"application/octet-stream"}}, Body: []byte{0, 1, 255}},
	}
	plugin.routes[0].Handler = plugin
	host := newHostWithRecords(capabilityRecord{id: "native", path: "/plugin.so", version: "1", plugin: pluginapi.Plugin{SchemaVersion: 7, Capabilities: pluginapi.Capabilities{NativeAPI: plugin}}})
	host.RegisterNativeRoutes(context.Background(), nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/plugin/run?q=ok&key=secret&auth_token=secret", strings.NewReader("payload"))
	req.Header = http.Header{"Authorization": []string{"Bearer client"}, "X-Api-Key": []string{"client"}, "X-Goog-Api-Key": []string{"google"}, "Api-Key": []string{"client"}, "Proxy-Authorization": []string{"proxy"}, "Cookie": []string{"session=secret"}, "Content-Type": []string{"application/json"}, "X-Trace": []string{"safe"}}
	recorder := httptest.NewRecorder()
	if !host.ServeNativeHTTP(recorder, req) {
		t.Fatal("route not served")
	}
	if recorder.Code != http.StatusTeapot || string(recorder.Body.Bytes()) != string([]byte{0, 1, 255}) || recorder.Header().Get("Retry-After") != "7" {
		t.Fatalf("response = %d %v %v", recorder.Code, recorder.Header(), recorder.Body.Bytes())
	}
	for _, key := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key", "Proxy-Authorization", "Cookie"} {
		if plugin.request.Headers.Get(key) != "" {
			t.Errorf("credential header %s leaked", key)
		}
	}
	if plugin.request.Headers.Get("X-Trace") != "safe" || string(plugin.request.Body) != "payload" || plugin.request.Query.Get("q") != "ok" || plugin.request.Query.Has("key") || plugin.request.Query.Has("auth_token") {
		t.Fatalf("request = %#v", plugin.request)
	}
}
func TestServeNativeHTTPDropsPluginCORSHeaders(t *testing.T) {
	// The host owns CORS; a plugin copy would duplicate Access-Control-Allow-Origin and browsers reject that.
	plugin := &nativePluginDouble{
		routes:   []pluginapi.NativeRoute{{Method: http.MethodPost, Path: "/v1/plugin/run"}},
		response: pluginapi.NativeResponse{StatusCode: http.StatusOK, Headers: http.Header{"Access-Control-Allow-Origin": []string{"*"}, "Access-Control-Expose-Headers": []string{"X-Upstream"}, "Retry-After": []string{"3"}}, Body: []byte("ok")},
	}
	plugin.routes[0].Handler = plugin
	host := newHostWithRecords(capabilityRecord{id: "native", path: "/plugin.so", version: "1", plugin: pluginapi.Plugin{SchemaVersion: 7, Capabilities: pluginapi.Capabilities{NativeAPI: plugin}}})
	host.RegisterNativeRoutes(context.Background(), nil)
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Access-Control-Allow-Origin", "*")
	if !host.ServeNativeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/plugin/run", nil)) {
		t.Fatal("route not served")
	}
	if got := recorder.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 {
		t.Fatalf("Access-Control-Allow-Origin = %q, want only the host value", got)
	}
	if recorder.Header().Get("Access-Control-Expose-Headers") != "" || recorder.Header().Get("Retry-After") != "3" {
		t.Fatalf("headers = %#v, want plugin CORS dropped and other headers kept", recorder.Header())
	}
}

func TestServeNativeHTTPRejectsInvalidStatusCodesWithoutFusingPlugin(t *testing.T) {
	for _, status := range []int{1000, -1, 100, 101, 199, 600, 999} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			plugin := &nativePluginDouble{
				routes:   []pluginapi.NativeRoute{{Method: http.MethodPost, Path: "/v1/plugin/run"}},
				response: pluginapi.NativeResponse{StatusCode: status, Headers: http.Header{"Content-Encoding": []string{"gzip"}}, Body: []byte("bad")},
			}
			plugin.routes[0].Handler = plugin
			host := newHostWithRecords(capabilityRecord{id: "native", path: "/plugin.so", version: "1", plugin: pluginapi.Plugin{SchemaVersion: 7, Capabilities: pluginapi.Capabilities{NativeAPI: plugin}}})
			host.RegisterNativeRoutes(context.Background(), nil)

			recorder := httptest.NewRecorder()
			if !host.ServeNativeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/plugin/run", nil)) {
				t.Fatal("route not served")
			}
			if recorder.Code != http.StatusBadGateway || recorder.Body.String() != "native plugin handler failed\n" {
				t.Fatalf("response = %d %q, want 502 plugin handler failure", recorder.Code, recorder.Body.String())
			}
			// The plain-text fallback must not inherit plugin transport headers.
			if recorder.Header().Get("Content-Encoding") != "" {
				t.Fatalf("fallback 502 kept plugin Content-Encoding %q", recorder.Header().Get("Content-Encoding"))
			}
			if !host.HasNativeRoute("/v1/plugin/run") {
				t.Fatal("invalid status fused plugin")
			}
		})
	}
}

func TestShutdownAllClearsNativeRoutes(t *testing.T) {
	host := New()
	host.mu.Lock()
	host.nativeRoutes[managementRouteKey(http.MethodPost, "/v1/native-test")] = nativeRouteRecord{pluginID: "native", path: "test", version: "1", route: pluginapi.NativeRoute{Method: http.MethodPost, Path: "/v1/native-test"}}
	host.mu.Unlock()
	host.ShutdownAllContext(context.Background())
	if host.HasNativeRoute("/v1/native-test") {
		t.Fatal("native route remained after shutdown")
	}
}

func TestHasNativeRouteHidesFusedPlugins(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{id: "native", path: "test", version: "1", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{NativeAPI: &nativePluginDouble{}}}})
	host.mu.Lock()
	host.nativeRoutes[managementRouteKey(http.MethodPost, "/v1/native-test")] = nativeRouteRecord{pluginID: "native", path: host.activePluginPaths["native"], version: host.activePluginVersions["native"], route: pluginapi.NativeRoute{Method: http.MethodPost, Path: "/v1/native-test"}}
	host.fused["native"] = "panic"
	host.mu.Unlock()
	if host.HasNativeRoute("/v1/native-test") {
		t.Fatal("fused route remained available")
	}
}
