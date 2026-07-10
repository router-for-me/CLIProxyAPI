package pluginhost

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRegisterManagementRoutesSkipsReservedAndUsesPriority(t *testing.T) {
	high := &managementPluginDouble{
		routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: "/config", Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
				return pluginapi.ManagementResponse{Body: []byte("reserved")}, nil
			})},
			{Method: http.MethodGet, Path: "/plugins/shared/status", Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
				return pluginapi.ManagementResponse{Body: []byte("high")}, nil
			})},
		},
	}
	low := &managementPluginDouble{
		routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: "/plugins/shared/status", Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
				return pluginapi.ManagementResponse{Body: []byte("low")}, nil
			})},
			{Method: http.MethodPost, Path: "plugins/low/run", Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
				return pluginapi.ManagementResponse{StatusCode: http.StatusAccepted, Body: []byte("low-only")}, nil
			})},
		},
	}
	host := newHostWithRecords(
		capabilityRecord{id: "low", priority: 1, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ManagementAPI: low}}},
		capabilityRecord{id: "high", priority: 10, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ManagementAPI: high}}},
	)
	host.RegisterManagementRoutes(context.Background(), map[string]struct{}{
		"GET /v0/management/config": {},
	})

	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins/shared/status", nil)
	rec := httptest.NewRecorder()
	if !host.ServeManagementHTTP(rec, req) {
		t.Fatal("ServeManagementHTTP() = false, want true")
	}
	if rec.Body.String() != "high" {
		t.Fatalf("Body = %q, want high", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v0/management/plugins/low/run", nil)
	rec = httptest.NewRecorder()
	if !host.ServeManagementHTTP(rec, req) {
		t.Fatal("ServeManagementHTTP() for low route = false, want true")
	}
	if rec.Code != http.StatusAccepted || rec.Body.String() != "low-only" {
		t.Fatalf("response = %d %q, want 202 low-only", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
	rec = httptest.NewRecorder()
	if host.ServeManagementHTTP(rec, req) {
		t.Fatal("reserved route was served by plugin")
	}
}

func TestServeManagementHTMLEscapesJSONResponseStrings(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "json",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{routes: []pluginapi.ManagementRoute{{
				Method: http.MethodGet,
				Path:   "/plugins/json/status",
				Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					return pluginapi.ManagementResponse{
						Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
						Body: []byte(`{
							"title": "<script>alert(1)</script>",
							"items": ["<b>first</b>", {"description": "safe & sound"}],
							"count": 1
						}`),
					}, nil
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins/json/status", nil)
	rec := httptest.NewRecorder()
	if !host.ServeManagementHTTP(rec, req) {
		t.Fatal("ServeManagementHTTP() = false, want true")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if body["title"] != html.EscapeString("<script>alert(1)</script>") {
		t.Fatalf("title = %q, want escaped", body["title"])
	}
	items, okItems := body["items"].([]any)
	if !okItems || len(items) != 2 {
		t.Fatalf("items = %#v, want two items", body["items"])
	}
	if items[0] != html.EscapeString("<b>first</b>") {
		t.Fatalf("items[0] = %q, want escaped", items[0])
	}
	nested, okNested := items[1].(map[string]any)
	if !okNested {
		t.Fatalf("items[1] = %#v, want object", items[1])
	}
	if nested["description"] != html.EscapeString("safe & sound") {
		t.Fatalf("nested description = %q, want escaped", nested["description"])
	}
	if body["count"] != float64(1) {
		t.Fatalf("count = %#v, want unchanged number", body["count"])
	}
}

func TestServeManagementHTTPForwardsResolvedLocale(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "locale",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{routes: []pluginapi.ManagementRoute{{
				Method: http.MethodPost,
				Path:   "/plugins/locale/run",
				Handler: managementHandlerFunc(func(_ context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					if req.Path != "/v0/management/plugins/locale/run" {
						t.Fatalf("management request path = %q, want normalized path", req.Path)
					}
					if locale := req.Headers.Get("X-Locale"); locale != "zh-CN" {
						t.Fatalf("management request X-Locale = %q, want zh-CN", locale)
					}
					if mode := req.Query.Get("mode"); mode != "full" {
						t.Fatalf("management request mode query = %q, want full", mode)
					}
					req.Headers.Set("X-Original", "mutated")
					req.Query.Set("mutated", "1")
					return pluginapi.ManagementResponse{StatusCode: http.StatusCreated, Body: []byte("created")}, nil
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	req := httptest.NewRequest(http.MethodPost, "/v0/management/plugins/locale/run?mode=full", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Accept-Language", "zh-CN, en;q=0.8")
	req.Header.Set("X-Original", "source")
	rec := httptest.NewRecorder()
	if !host.ServeManagementHTTP(rec, req) {
		t.Fatal("ServeManagementHTTP() = false, want true")
	}
	if rec.Code != http.StatusCreated || rec.Body.String() != "created" {
		t.Fatalf("response = %d %q, want 201 created", rec.Code, rec.Body.String())
	}
	if got := req.Header.Get("X-Locale"); got != "" {
		t.Fatalf("original request X-Locale = %q, want unchanged empty", got)
	}
	if got := req.Header.Get("X-Original"); got != "source" {
		t.Fatalf("original request X-Original = %q, want source", got)
	}
	if got := req.URL.Query().Get("mutated"); got != "" {
		t.Fatalf("original request mutated query = %q, want unchanged empty", got)
	}
}

func TestServeManagementHTTPForwardsQueryLocaleWithoutHeaders(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "query-locale",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{routes: []pluginapi.ManagementRoute{{
				Method: http.MethodGet,
				Path:   "/plugins/query-locale/status",
				Handler: managementHandlerFunc(func(_ context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					if locale := req.Headers.Get("X-Locale"); locale != "zh-CN" {
						t.Fatalf("management request X-Locale = %q, want zh-CN", locale)
					}
					return pluginapi.ManagementResponse{Body: []byte("ok")}, nil
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins/query-locale/status?locale=zh-CN", nil)
	rec := httptest.NewRecorder()
	if !host.ServeManagementHTTP(rec, req) {
		t.Fatal("ServeManagementHTTP() = false, want true")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("response = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestServeManagementHTTPProvidesWritableHeadersWithoutLocale(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "empty-headers",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{routes: []pluginapi.ManagementRoute{{
				Method: http.MethodGet,
				Path:   "/plugins/empty-headers/status",
				Handler: managementHandlerFunc(func(_ context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					req.Headers.Set("X-Test", "ok")
					if req.Headers.Get("X-Test") != "ok" {
						t.Fatalf("management request headers = %#v, want writable header", req.Headers)
					}
					return pluginapi.ManagementResponse{Body: []byte("ok")}, nil
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins/empty-headers/status", nil)
	rec := httptest.NewRecorder()
	if !host.ServeManagementHTTP(rec, req) {
		t.Fatal("ServeManagementHTTP() = false, want true")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("response = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestManagementHandlerPanicFusesPlugin(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "panic",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{routes: []pluginapi.ManagementRoute{{
				Method: http.MethodGet,
				Path:   "/plugins/panic",
				Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					panic("boom")
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins/panic", nil)
	rec := httptest.NewRecorder()
	if !host.ServeManagementHTTP(rec, req) {
		t.Fatal("ServeManagementHTTP() = false, want true")
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !host.isPluginFused("panic") {
		t.Fatal("plugin was not fused after panic")
	}
}

func TestServeResourceHTTPDispatchesPluginResource(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "resource",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{resources: []pluginapi.ResourceRoute{{
				Path:        "/status",
				Menu:        "Status",
				Description: "Shows plugin status.",
				Handler: managementHandlerFunc(func(_ context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					if req.Path != "/v0/resource/plugins/resource/status" {
						t.Fatalf("resource request path = %q, want normalized resource path", req.Path)
					}
					if locale := req.Headers.Get("X-Locale"); locale != "zh-CN" {
						t.Fatalf("resource request X-Locale = %q, want zh-CN", locale)
					}
					return pluginapi.ManagementResponse{
						Headers: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
						Body:    []byte("<!doctype html><title>resource</title>"),
					}, nil
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v0/resource/plugins/resource/status?locale=zh-CN", nil)
	rec := httptest.NewRecorder()
	if !host.ServeResourceHTTP(rec, req) {
		t.Fatal("ServeResourceHTTP() = false, want true")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "<!doctype html><title>resource</title>" {
		t.Fatalf("response = %d %q, want 200 html", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
}

func TestServeResourceHTTPDispatchesPluginResourceWithDefaultQuery(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "resource-query",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{resources: []pluginapi.ResourceRoute{{
				Path:        "/status?mode=full",
				Menu:        "Status",
				Description: "Shows plugin status.",
				Handler: managementHandlerFunc(func(_ context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					if req.Path != "/v0/resource/plugins/resource-query/status" {
						t.Fatalf("resource request path = %q, want normalized resource path", req.Path)
					}
					if mode := req.Query.Get("mode"); mode != "full" {
						t.Fatalf("resource request mode query = %q, want full", mode)
					}
					if locale := req.Headers.Get("X-Locale"); locale != "zh-CN" {
						t.Fatalf("resource request X-Locale = %q, want zh-CN", locale)
					}
					return pluginapi.ManagementResponse{Body: []byte("ok")}, nil
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 || len(plugins[0].Menus) != 1 {
		t.Fatalf("RegisteredPlugins() = %#v, want one plugin with one menu", plugins)
	}
	if got := plugins[0].Menus[0].Path; got != "/v0/resource/plugins/resource-query/status?mode=full" {
		t.Fatalf("menu path = %q, want launch path with default query", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/resource/plugins/resource-query/status?mode=full&locale=zh-CN", nil)
	rec := httptest.NewRecorder()
	if !host.ServeResourceHTTP(rec, req) {
		t.Fatal("ServeResourceHTTP() = false, want true")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("response = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestServeResourceHTTPForwardsAcceptLanguageLocale(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "resource-accept-language",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{resources: []pluginapi.ResourceRoute{{
				Path: "/status",
				Handler: managementHandlerFunc(func(_ context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					if locale := req.Headers.Get("X-Locale"); locale != "zh-CN" {
						t.Fatalf("resource request X-Locale = %q, want zh-CN", locale)
					}
					return pluginapi.ManagementResponse{Body: []byte("ok")}, nil
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v0/resource/plugins/resource-accept-language/status", nil)
	req.Header.Set("Accept-Language", "zh-CN, en;q=0.8")
	rec := httptest.NewRecorder()
	if !host.ServeResourceHTTP(rec, req) {
		t.Fatal("ServeResourceHTTP() = false, want true")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("response = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestLegacyGETManagementMenuRegistersAsResource(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "legacy",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{routes: []pluginapi.ManagementRoute{{
				Method:      http.MethodGet,
				Path:        "/plugins/legacy/status",
				Menu:        "Legacy Status",
				Description: "Shows legacy plugin status.",
				Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					return pluginapi.ManagementResponse{Body: []byte("legacy")}, nil
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	managementReq := httptest.NewRequest(http.MethodGet, "/v0/management/plugins/legacy/status", nil)
	managementRec := httptest.NewRecorder()
	if host.ServeManagementHTTP(managementRec, managementReq) {
		t.Fatal("legacy menu route was served as Management API route")
	}

	resourceReq := httptest.NewRequest(http.MethodGet, "/v0/resource/plugins/legacy/status", nil)
	resourceRec := httptest.NewRecorder()
	if !host.ServeResourceHTTP(resourceRec, resourceReq) {
		t.Fatal("legacy menu route was not served as resource route")
	}
	if resourceRec.Body.String() != "legacy" {
		t.Fatalf("resource body = %q, want legacy", resourceRec.Body.String())
	}
}

func TestRegisteredPluginsIncludesResourceMenus(t *testing.T) {
	plugin := &managementPluginDouble{
		routes: []pluginapi.ManagementRoute{
			{
				Method: http.MethodGet,
				Path:   "/plugins/menu/hidden",
				Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					return pluginapi.ManagementResponse{}, nil
				}),
			},
		},
		resources: []pluginapi.ResourceRoute{
			{
				Path:        "/status",
				Menu:        "Status",
				Description: "Shows plugin status.",
				Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					return pluginapi.ManagementResponse{}, nil
				}),
			},
		},
	}
	host := newHostWithRecords(capabilityRecord{
		id:     "menu",
		meta:   pluginapi.Metadata{Name: "menu", Version: "1.0.0", Author: "test", GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI"},
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ManagementAPI: plugin}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 {
		t.Fatalf("RegisteredPlugins() len = %d, want 1", len(plugins))
	}
	if len(plugins[0].Menus) != 1 {
		t.Fatalf("RegisteredPlugins()[0].Menus = %#v, want one visible GET menu", plugins[0].Menus)
	}
	menu := plugins[0].Menus[0]
	if menu.Path != "/v0/resource/plugins/menu/status" || menu.Menu != "Status" || menu.Description != "Shows plugin status." {
		t.Fatalf("menu = %#v, want normalized status menu", menu)
	}
}

func TestRegisteredPluginsClonesLocalizedMetadataAndMenus(t *testing.T) {
	plugin := &managementPluginDouble{
		resources: []pluginapi.ResourceRoute{{
			Path:        "/status",
			Menu:        "Status",
			Description: "Shows plugin status.",
			Locales: map[string]pluginapi.RouteLocale{
				"zh-CN": {Menu: "状态", Description: "显示插件状态。"},
			},
			Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
				return pluginapi.ManagementResponse{}, nil
			}),
		}},
	}
	host := newHostWithRecords(capabilityRecord{
		id: "localized",
		meta: pluginapi.Metadata{
			Name:             "localized",
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Locales: map[string]pluginapi.MetadataLocale{
				"zh-CN": {Name: "本地化插件", Author: "作者"},
			},
			ConfigFields: []pluginapi.ConfigField{{
				Name:        "mode",
				Type:        pluginapi.ConfigFieldTypeEnum,
				EnumValues:  []string{"safe", "fast"},
				Description: "Execution mode.",
				Locales: map[string]pluginapi.ConfigFieldLocale{
					"zh-CN": {Description: "执行模式。"},
				},
			}},
		},
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ManagementAPI: plugin}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)

	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 || len(plugins[0].Menus) != 1 {
		t.Fatalf("RegisteredPlugins() = %#v, want one plugin with one menu", plugins)
	}
	plugins[0].Metadata.Locales["zh-cn"] = pluginapi.MetadataLocale{Name: "mutated"}
	plugins[0].Metadata.ConfigFields[0].EnumValues[0] = "mutated"
	plugins[0].Metadata.ConfigFields[0].Locales["zh-cn"] = pluginapi.ConfigFieldLocale{Description: "mutated"}
	plugins[0].Menus[0].Locales["zh-cn"] = pluginapi.RouteLocale{Menu: "mutated"}

	again := host.RegisteredPlugins()
	field := again[0].Metadata.ConfigFields[0]
	if again[0].Metadata.Locales["zh-cn"].Name != "本地化插件" ||
		field.EnumValues[0] != "safe" ||
		field.Locales["zh-cn"].Description != "执行模式。" ||
		again[0].Menus[0].Locales["zh-cn"].Menu != "状态" {
		t.Fatalf("RegisteredPlugins() returned shared nested data: %#v", again[0])
	}
}

func TestRegisteredPluginsNormalizesAndClonesLocaleKeys(t *testing.T) {
	routeLocales := map[string]pluginapi.RouteLocale{
		" zh_CN ": {Menu: "状态", Description: "显示插件状态。"},
	}
	metaLocales := map[string]pluginapi.MetadataLocale{
		" zh_CN ": {Name: "本地化插件", Author: "作者"},
	}
	fieldLocales := map[string]pluginapi.ConfigFieldLocale{
		" zh_CN ": {Description: "执行模式。"},
	}
	host := newHostWithRecords(capabilityRecord{
		id: "normalized-locales",
		meta: pluginapi.Metadata{
			Name:             "normalized-locales",
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Locales:          metaLocales,
			ConfigFields: []pluginapi.ConfigField{{
				Name:        "mode",
				Description: "Execution mode.",
				Locales:     fieldLocales,
			}},
		},
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ManagementAPI: &managementPluginDouble{resources: []pluginapi.ResourceRoute{{
				Path:    "/status",
				Menu:    "Status",
				Locales: routeLocales,
				Handler: managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
					return pluginapi.ManagementResponse{}, nil
				}),
			}}},
		}},
	})
	host.RegisterManagementRoutes(context.Background(), nil)
	routeLocales[" zh_CN "] = pluginapi.RouteLocale{Menu: "mutated"}

	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 || len(plugins[0].Menus) != 1 || len(plugins[0].Metadata.ConfigFields) != 1 {
		t.Fatalf("RegisteredPlugins() = %#v, want one plugin/menu/field", plugins)
	}
	if plugins[0].Metadata.Locales["zh-cn"].Name != "本地化插件" ||
		plugins[0].Metadata.ConfigFields[0].Locales["zh-cn"].Description != "执行模式。" ||
		plugins[0].Menus[0].Locales["zh-cn"].Menu != "状态" {
		t.Fatalf("RegisteredPlugins() locale data = %#v, want normalized cloned locale keys", plugins[0])
	}
}

func TestRegisteredPluginsPreservesEmptyConfigFields(t *testing.T) {
	emptyFields := []pluginapi.ConfigField{}
	host := newHostWithRecords(capabilityRecord{
		id: "empty-fields",
		meta: pluginapi.Metadata{
			Name:             "empty-fields",
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields:     emptyFields,
		},
	})

	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 {
		t.Fatalf("RegisteredPlugins() = %#v, want one plugin", plugins)
	}
	if plugins[0].Metadata.ConfigFields == nil || len(plugins[0].Metadata.ConfigFields) != 0 {
		t.Fatalf("ConfigFields = %#v, want preserved empty slice", plugins[0].Metadata.ConfigFields)
	}
}

func TestRegisteredPluginsClonesMetadataLocalesWithoutConfigFields(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "metadata-only",
		meta: pluginapi.Metadata{
			Name:             "metadata-only",
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Locales: map[string]pluginapi.MetadataLocale{
				"zh-CN": {Name: "仅元数据", Author: "作者"},
			},
		},
	})

	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 {
		t.Fatalf("RegisteredPlugins() = %#v, want one plugin", plugins)
	}
	plugins[0].Metadata.Locales["zh-cn"] = pluginapi.MetadataLocale{Name: "mutated"}

	again := host.RegisteredPlugins()
	if again[0].Metadata.Locales["zh-cn"].Name != "仅元数据" {
		t.Fatalf("RegisteredPlugins() metadata locales shared map: %#v", again[0].Metadata.Locales)
	}
}

type managementPluginDouble struct {
	routes    []pluginapi.ManagementRoute
	resources []pluginapi.ResourceRoute
}

func (p *managementPluginDouble) RegisterManagement(context.Context, pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	return pluginapi.ManagementRegistrationResponse{Routes: p.routes, Resources: p.resources}, nil
}

type managementHandlerFunc func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error)

func (f managementHandlerFunc) HandleManagement(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	return f(ctx, req)
}
