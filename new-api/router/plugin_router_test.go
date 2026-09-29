package router

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPluginDispatcherMissFallsThroughWithoutLeakingInnerResponse(t *testing.T) {
	outer, registry := newPluginRouterTest(t, nil, nil)
	dispatcher := (&pluginRouteDispatcher{registry: registry}).dispatch
	outer.NoRoute(
		dispatcher,
		func(c *gin.Context) {
			assert.Equal(t, "before", c.GetString(middleware.RouteTagKey))
			c.Header("X-Fallback", "true")
			c.String(http.StatusOK, "spa")
		},
	)

	recorder := performPluginRequest(outer, http.MethodGet, "/not-owned")

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "spa", recorder.Body.String())
	assert.Equal(t, "true", recorder.Header().Get("X-Fallback"))
	assert.Empty(t, recorder.Header().Get("Location"))
}

func TestPluginDebugLogsOnlyOwnedRoutes(t *testing.T) {
	previousDebug := common.DebugEnabled
	common.DebugEnabled = true
	t.Cleanup(func() { common.DebugEnabled = previousDebug })

	var output bytes.Buffer
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &output
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousWriter
		common.LogWriterMu.Unlock()
	})

	plugin := compileRouterPlugin(t, "debug-owner", "1.0.0", `[
		{method: "GET", path: "/vendor/debug", type: "dynamic"}
	]`)
	handlers := func(generation *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) []gin.HandlerFunc {
		production := productionPluginRouteHandlers(generation, binding)
		return []gin.HandlerFunc{
			production[0],
			func(c *gin.Context) { c.Status(http.StatusNoContent) },
		}
	}
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, handlers)
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)
	output.Reset()

	owned := performPluginRequest(outer, http.MethodGet, "/vendor/debug")
	require.Equal(t, http.StatusNoContent, owned.Code)
	logOutput := output.String()
	assert.Contains(t, logOutput, "request-phase-two")
	assert.Contains(t, logOutput, "task_plugin subsystem=router event=route_matched")
	assert.Contains(t, logOutput, `plugin="debug-owner"`)
	assert.NotContains(t, logOutput, "/vendor/debug")
	assert.Contains(t, logOutput, "event=route_complete")

	output.Reset()
	miss := performPluginRequest(outer, http.MethodGet, "/not-owned")
	require.Equal(t, http.StatusOK, miss.Code)
	assert.Equal(t, "fallback", miss.Body.String())
	assert.NotContains(t, output.String(), "task_plugin")
}

func TestPluginAuthoredHeaderOnly404PassesThrough(t *testing.T) {
	plugin := compileRouterPlugin(t, "authored-404", "1.0.0", `[
		{method: "GET", path: "/vendor/missing", type: "dynamic"}
	]`)
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
		c.Header("X-Plugin", "authored")
		c.Status(http.StatusNotFound)
	})
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, handlers)
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)

	recorder := performPluginRequest(outer, http.MethodGet, "/vendor/missing")

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Empty(t, recorder.Body.String())
	assert.Equal(t, "authored", recorder.Header().Get("X-Plugin"))
}

func TestPluginOwnedPathMethodMismatchReturns405(t *testing.T) {
	plugin := compileRouterPlugin(t, "method-owner", "1.0.0", `[
		{method: "GET", path: "/vendor/jobs/:task_id", type: "query", render: "native"}
	]`)
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, testPluginRouteHandlers(
		func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
			c.String(http.StatusOK, "plugin")
		},
	))
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)

	recorder := performPluginRequest(outer, http.MethodPost, "/vendor/jobs/task-1")

	assert.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	assert.Empty(t, recorder.Body.String())
}

func TestPluginTrailingSlashMissDoesNotRedirect(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		routePath   string
		requestPath string
	}{
		{name: "declared without slash", routePath: "/vendor/job", requestPath: "/vendor/job/"},
		{name: "declared with slash", routePath: "/vendor/job/", requestPath: "/vendor/job"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plugin := compileRouterPlugin(t, "slash-owner", "1.0.0", fmt.Sprintf(`[
				{method: "GET", path: %q, type: "dynamic"}
			]`, testCase.routePath))
			outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, testPluginRouteHandlers(
				func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
					c.String(http.StatusOK, "plugin")
				},
			))
			outer.NoRoute(
				(&pluginRouteDispatcher{registry: registry}).dispatch,
				func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
			)

			recorder := performPluginRequest(outer, http.MethodGet, testCase.requestPath)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, "fallback", recorder.Body.String())
			assert.Empty(t, recorder.Header().Get("Location"))
		})
	}
}

func TestPluginSSEFlushesWithoutFallbackBuffering(t *testing.T) {
	plugin := compileRouterPlugin(t, "stream-owner", "1.0.0", `[
		{method: "GET", path: "/vendor/stream", type: "dynamic"}
	]`)
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString("data: ready\n\n")
		c.Writer.Flush()
	})
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, handlers)
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		middleware.RouteTag("web"),
		gzip.Gzip(gzip.DefaultCompression),
		middleware.Cache(),
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)

	request := httptest.NewRequest(http.MethodGet, "/vendor/stream", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	outer.ServeHTTP(recorder, request)

	assert.True(t, recorder.Flushed)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "data: ready\n\n", recorder.Body.String())
	assert.Empty(t, recorder.Header().Get("Content-Encoding"))
	assert.Empty(t, recorder.Header().Get("Cache-Control"))
	assert.Empty(t, recorder.Header().Get("Cache-Version"))
}

func TestWebCacheHeadersDoNotLeakOntoPluginRoutes(t *testing.T) {
	plugin := compileRouterPlugin(t, "cache-owner", "1.0.0", `[
		{method: "GET", path: "/vendor/status/:task_id", type: "query", render: "native"}
	]`)
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, testPluginRouteHandlers(
		func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
			c.JSON(http.StatusOK, gin.H{"status": "queued"})
		},
	))
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		middleware.Cache(),
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)

	pluginResponse := performPluginRequest(outer, http.MethodGet, "/vendor/status/task-1")
	assert.Empty(t, pluginResponse.Header().Get("Cache-Control"))
	assert.Empty(t, pluginResponse.Header().Get("Cache-Version"))

	fallbackResponse := performPluginRequest(outer, http.MethodGet, "/unknown")
	assert.Equal(t, "max-age=604800", fallbackResponse.Header().Get("Cache-Control"))
	assert.NotEmpty(t, fallbackResponse.Header().Get("Cache-Version"))
}

func TestPluginInnerContextImportsRequestMetadataAndTrustedProxyConfig(t *testing.T) {
	plugin := compileRouterPlugin(t, "context-owner", "1.0.0", `[
		{method: "GET", path: "/vendor/context", type: "dynamic"}
	]`)
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
		c.JSON(http.StatusOK, gin.H{
			"request_id": c.GetString(common.RequestIdKey),
			"language":   c.GetString(string(constant.ContextKeyLanguage)),
			"client_ip":  c.ClientIP(),
			"route_tag":  c.GetString(middleware.RouteTagKey),
		})
	})
	outer, registry := newPluginRouterTestWithProxies(t, []*jsplugin.LoadedPlugin{plugin}, handlers, []string{"127.0.0.0/8"})
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)

	request := httptest.NewRequest(http.MethodGet, "/vendor/context", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.20")
	recorder := httptest.NewRecorder()
	outer.ServeHTTP(recorder, request)

	assert.JSONEq(t, `{"request_id":"request-phase-two","language":"zh-CN","client_ip":"203.0.113.20","route_tag":"relay"}`, recorder.Body.String())
}

func TestPluginRequestPinsGenerationAcrossHotSwap(t *testing.T) {
	v1 := compileRouterPlugin(t, "hot-swap", "1.0.0", `[
		{method: "GET", path: "/vendor/version", type: "dynamic"}
	]`)
	v2 := compileRouterPlugin(t, "hot-swap", "2.0.0", `[
		{method: "GET", path: "/vendor/version", type: "dynamic"}
	]`)
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var registry *jsplugin.Registry
	handlers := testPluginRouteHandlers(func(c *gin.Context, generation *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
		if binding.Plugin.Meta.Version == "1.0.0" {
			startOnce.Do(func() { close(started) })
			<-release
		}
		pinnedValue, exists := c.Get(jsplugin.ContextKeyPinnedRoute)
		pinned, ok := pinnedValue.(jsplugin.PinnedRoute)
		if !exists || !ok || pinned.Plugin == nil || pinned.Generation == nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"version":            binding.Plugin.Meta.Version,
			"generation":         generation.Number,
			"pinned_version":     pinned.Plugin.Meta.Version,
			"pinned_generation":  pinned.Generation.Number,
			"current_generation": registry.Generation().Number,
		})
	})
	outer, activeRegistry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{v1}, handlers)
	registry = activeRegistry
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)
	firstGeneration := registry.Generation().Number

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- performPluginRequest(outer, http.MethodGet, "/vendor/version")
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "first generation request did not start")
	}
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{v2}))
	secondGeneration := registry.Generation().Number
	close(release)

	var first *httptest.ResponseRecorder
	select {
	case first = <-firstDone:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "pinned first generation request did not finish")
	}
	assert.JSONEq(t, fmt.Sprintf(`{
		"version": "1.0.0",
		"generation": %d,
		"pinned_version": "1.0.0",
		"pinned_generation": %d,
		"current_generation": %d
	}`, firstGeneration, firstGeneration, secondGeneration), first.Body.String())
	second := performPluginRequest(outer, http.MethodGet, "/vendor/version")
	assert.JSONEq(t, fmt.Sprintf(`{
		"version": "2.0.0",
		"generation": %d,
		"pinned_version": "2.0.0",
		"pinned_generation": %d,
		"current_generation": %d
	}`, secondGeneration, secondGeneration, secondGeneration), second.Body.String())
}

func TestPluginStaticRouteConflictsAreExcluded(t *testing.T) {
	tests := []struct {
		name       string
		staticPath string
		pluginPath string
	}{
		{name: "parameter intersection", staticPath: "/core/:id", pluginPath: "/core/fixed"},
		{name: "trailing slash redirect shadow", staticPath: "/fixed", pluginPath: "/fixed/"},
		{name: "catchall redirect shadow", staticPath: "/files/*filepath", pluginPath: "/files"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			outer := newOuterPluginTestEngine()
			outer.GET(testCase.staticPath, func(c *gin.Context) { c.String(http.StatusOK, "static") })
			registry := jsplugin.NewRegistry()
			plugin := compileRouterPlugin(t, "static-conflict", "1.0.0", fmt.Sprintf(`[
				{method: "POST", path: %q, type: "submit"}
			]`, testCase.pluginPath))
			require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{plugin}))
			builder := newPluginGenerationBuilder(outer.Routes(), nil, testPluginRouteHandlers(
				func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
					c.String(http.StatusOK, "plugin")
				},
			))
			require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
			outer.NoRoute(
				(&pluginRouteDispatcher{registry: registry}).dispatch,
				func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
			)

			recorder := performPluginRequest(outer, http.MethodPost, testCase.pluginPath)
			assert.Equal(t, "fallback", recorder.Body.String())
			assert.Contains(t, registry.RoutingErrors()["static-conflict"], "intersects static route")
		})
	}
}

func TestStaticConflictingUpdateRetainsIncumbentAndPublishesHealthyPeer(t *testing.T) {
	outer := newOuterPluginTestEngine()
	outer.GET("/core/:id", func(c *gin.Context) { c.String(http.StatusOK, "static") })
	registry := jsplugin.NewRegistry()
	incumbent := compileRouterPlugin(t, "static-update", "1.0.0", `[
		{method: "GET", path: "/safe/incumbent", type: "dynamic"}
	]`)
	conflictingUpdate := compileRouterPlugin(t, "static-update", "2.0.0", `[
		{method: "POST", path: "/core/fixed", type: "submit"}
	]`)
	healthyV1 := compileRouterPlugin(t, "healthy-peer", "1.0.0", `[
		{method: "GET", path: "/safe/healthy", type: "dynamic"}
	]`)
	healthyV2 := compileRouterPlugin(t, "healthy-peer", "2.0.0", `[
		{method: "GET", path: "/safe/healthy", type: "dynamic"}
	]`)
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{incumbent, healthyV1}))
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
		c.String(http.StatusOK, binding.Plugin.Meta.Version)
	})
	builder := newPluginGenerationBuilder(outer.Routes(), nil, handlers)
	require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)

	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{conflictingUpdate, healthyV2}))

	activeIncumbent, ok := registry.Get("static-update")
	require.True(t, ok)
	assert.Same(t, incumbent, activeIncumbent)
	activeHealthy, ok := registry.Get("healthy-peer")
	require.True(t, ok)
	assert.Same(t, healthyV2, activeHealthy)
	assert.Equal(t, "1.0.0", performPluginRequest(outer, http.MethodGet, "/safe/incumbent").Body.String())
	assert.Equal(t, "2.0.0", performPluginRequest(outer, http.MethodGet, "/safe/healthy").Body.String())
	assert.Contains(t, registry.RoutingErrors()["static-update"], "intersects static route")
}

func TestStaticConflictingNewOverrideRetainsFactoryRoute(t *testing.T) {
	outer := newOuterPluginTestEngine()
	outer.GET("/core/:id", func(c *gin.Context) { c.String(http.StatusOK, "static") })
	registry := jsplugin.NewRegistry()
	factory, err := registry.RegisterFactory(routerPluginSource("factory-route", "1.0.0", `[
		{method: "GET", path: "/factory-route/safe", type: "dynamic"}
	]`), jsplugin.Options{})
	require.NoError(t, err)
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
		c.String(http.StatusOK, binding.Plugin.Meta.Version)
	})
	builder := newPluginGenerationBuilder(outer.Routes(), nil, handlers)
	require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)
	conflictingOverride := compileRouterPlugin(t, "factory-route", "2.0.0", `[
		{method: "POST", path: "/core/fixed", type: "submit"}
	]`)

	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{conflictingOverride}))

	active, ok := registry.Get("factory-route")
	require.True(t, ok)
	assert.Same(t, factory, active)
	assert.Equal(t, "1.0.0", performPluginRequest(outer, http.MethodGet, "/factory-route/safe").Body.String())
	assert.Contains(t, registry.RoutingErrors()["factory-route"], "intersects static route")
}

func TestPluginRouteOwnershipCanSwapWithinOneGeneration(t *testing.T) {
	alphaV1 := compileRouterPlugin(t, "route-swap-alpha", "1.0.0", `[
		{method: "GET", path: "/route-swap/alpha", type: "dynamic"}
	]`)
	betaV1 := compileRouterPlugin(t, "route-swap-beta", "1.0.0", `[
		{method: "GET", path: "/route-swap/beta", type: "dynamic"}
	]`)
	alphaV2 := compileRouterPlugin(t, "route-swap-alpha", "2.0.0", `[
		{method: "GET", path: "/route-swap/beta", type: "dynamic"}
	]`)
	betaV2 := compileRouterPlugin(t, "route-swap-beta", "2.0.0", `[
		{method: "GET", path: "/route-swap/alpha", type: "dynamic"}
	]`)
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
		c.String(http.StatusOK, binding.Plugin.Meta.Key+"@"+binding.Plugin.Meta.Version)
	})
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{alphaV1, betaV1}, handlers)
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)

	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{alphaV2, betaV2}))

	assert.Equal(t, "route-swap-beta@2.0.0", performPluginRequest(outer, http.MethodGet, "/route-swap/alpha").Body.String())
	assert.Equal(t, "route-swap-alpha@2.0.0", performPluginRequest(outer, http.MethodGet, "/route-swap/beta").Body.String())
	assert.Empty(t, registry.RoutingErrors())
}

func TestRejectedRouteUpdateDoesNotFreezeHealthyPeer(t *testing.T) {
	alphaV1 := compileRouterPlugin(t, "route-fallback-alpha", "1.0.0", `[
		{method: "GET", path: "/route-fallback/alpha", type: "dynamic"}
	]`)
	betaV1 := compileRouterPlugin(t, "route-fallback-beta", "1.0.0", `[
		{method: "GET", path: "/route-fallback/beta", type: "dynamic"}
	]`)
	owner := compileRouterPlugin(t, "route-fallback-owner", "1.0.0", `[
		{method: "GET", path: "/route-fallback/owner", type: "dynamic"}
	]`)
	alphaV2 := compileRouterPlugin(t, "route-fallback-alpha", "2.0.0", `[
		{method: "POST", path: "/route-fallback/owner", type: "submit"}
	]`)
	betaV2 := compileRouterPlugin(t, "route-fallback-beta", "2.0.0", `[
		{method: "GET", path: "/route-fallback/alpha", type: "dynamic"}
	]`)
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
		c.String(http.StatusOK, binding.Plugin.Meta.Key+"@"+binding.Plugin.Meta.Version)
	})
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{alphaV1, betaV1, owner}, handlers)
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)

	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{alphaV2, betaV2, owner}))

	_, alphaActive := registry.Get("route-fallback-alpha")
	assert.False(t, alphaActive)
	activeBeta, ok := registry.Get("route-fallback-beta")
	require.True(t, ok)
	assert.Same(t, betaV2, activeBeta)
	assert.Equal(t, "route-fallback-beta@2.0.0", performPluginRequest(outer, http.MethodGet, "/route-fallback/alpha").Body.String())
	assert.Contains(t, registry.RoutingErrors()["route-fallback-alpha"], "overlaps plugin route-fallback-owner")
	assert.NotContains(t, registry.RoutingErrors(), "route-fallback-beta")
}

func TestPluginPathOwnershipIsExclusiveAcrossMethods(t *testing.T) {
	alpha := compileRouterPlugin(t, "alpha-owner", "1.0.0", `[
		{method: "GET", path: "/shared/jobs/:task_id", type: "query", render: "native"}
	]`)
	beta := compileRouterPlugin(t, "beta-owner", "1.0.0", `[
		{method: "POST", path: "/shared/jobs/:task_id", type: "submit"}
	]`)
	// Display priority must not change exclusive route ownership.
	alpha.Meta.SortPriority = -100
	beta.Meta.SortPriority = 100
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
		c.String(http.StatusOK, binding.Plugin.Meta.Key)
	})
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{beta, alpha}, handlers)
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)

	getResponse := performPluginRequest(outer, http.MethodGet, "/shared/jobs/task-1")
	assert.Equal(t, "alpha-owner", getResponse.Body.String())
	postResponse := performPluginRequest(outer, http.MethodPost, "/shared/jobs/task-1")
	assert.Equal(t, http.StatusMethodNotAllowed, postResponse.Code)
	assert.Contains(t, registry.RoutingErrors()["beta-owner"], "overlaps plugin alpha-owner")
}

func TestOnePluginMayOwnMultipleMethodsForSamePath(t *testing.T) {
	plugin := compileRouterPlugin(t, "multi-method", "1.0.0", `[
		{method: "GET", path: "/vendor/multi/:task_id", type: "query", render: "native"},
		{method: "POST", path: "/vendor/multi/:task_id", type: "submit"}
	]`)
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
		c.String(http.StatusOK, c.Request.Method)
	})
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, handlers)
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)

	assert.Equal(t, http.MethodGet, performPluginRequest(outer, http.MethodGet, "/vendor/multi/task-1").Body.String())
	assert.Equal(t, http.MethodPost, performPluginRequest(outer, http.MethodPost, "/vendor/multi/task-1").Body.String())
	assert.NotContains(t, registry.RoutingErrors(), "multi-method")
}

func TestGinWildcardNameConflictRejectsWholePlugin(t *testing.T) {
	plugin := compileRouterPlugin(t, "wildcard-conflict", "1.0.0", `[
		{method: "GET", path: "/vendor/:id/first", type: "query", render: "native", taskIdParam: "id"},
		{method: "GET", path: "/vendor/:name/second", type: "query", render: "native", taskIdParam: "name"}
	]`)
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, testPluginRouteHandlers(
		func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
			c.String(http.StatusOK, "plugin")
		},
	))
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)

	assert.Equal(t, "fallback", performPluginRequest(outer, http.MethodGet, "/vendor/1/first").Body.String())
	assert.Contains(t, registry.RoutingErrors()["wildcard-conflict"], "incompatible wildcard names")
}

func TestGinRegistrationPanicRebuildsWithoutOffender(t *testing.T) {
	alpha := compileRouterPlugin(t, "panic-alpha", "1.0.0", `[
		{method: "GET", path: "/panic/alpha", type: "dynamic"}
	]`)
	beta := compileRouterPlugin(t, "panic-beta", "1.0.0", `[
		{method: "GET", path: "/panic/beta", type: "dynamic"}
	]`)
	registry := jsplugin.NewRegistry()
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{alpha, beta}))
	outer := newOuterPluginTestEngine()
	builder := newPluginGenerationBuilder(outer.Routes(), nil, testPluginRouteHandlers(
		func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
			c.String(http.StatusOK, binding.Plugin.Meta.Key)
		},
	))
	normalRegister := builder.registerRoute
	builder.registerRoute = func(engine *gin.Engine, binding jsplugin.RouteBinding, handlers []gin.HandlerFunc) {
		if binding.Plugin.Meta.Key == "panic-beta" {
			panic("registration failed")
		}
		normalRegister(engine, binding, handlers)
	}
	require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
	outer.NoRoute(
		(&pluginRouteDispatcher{registry: registry}).dispatch,
		func(c *gin.Context) { c.String(http.StatusOK, "fallback") },
	)

	assert.Equal(t, "panic-alpha", performPluginRequest(outer, http.MethodGet, "/panic/alpha").Body.String())
	assert.Equal(t, "fallback", performPluginRequest(outer, http.MethodGet, "/panic/beta").Body.String())
	assert.Contains(t, registry.RoutingErrors()["panic-beta"], "registration panic")
}

func TestGinRegistrationPanicReadmitsPluginBlockedByOffender(t *testing.T) {
	alpha := compileRouterPlugin(t, "panic-owner-alpha", "1.0.0", `[
		{method: "GET", path: "/panic/reconsider", type: "dynamic"}
	]`)
	beta := compileRouterPlugin(t, "panic-owner-beta", "1.0.0", `[
		{method: "POST", path: "/panic/reconsider", type: "submit"}
	]`)
	registry := jsplugin.NewRegistry()
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{alpha, beta}))
	outer := newOuterPluginTestEngine()
	builder := newPluginGenerationBuilder(outer.Routes(), nil, testPluginRouteHandlers(
		func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
			c.String(http.StatusOK, binding.Plugin.Meta.Key)
		},
	))
	normalRegister := builder.registerRoute
	builder.registerRoute = func(engine *gin.Engine, binding jsplugin.RouteBinding, handlers []gin.HandlerFunc) {
		if binding.Plugin.Meta.Key == "panic-owner-alpha" {
			panic("registration failed")
		}
		normalRegister(engine, binding, handlers)
	}
	require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)

	_, alphaActive := registry.Get("panic-owner-alpha")
	assert.False(t, alphaActive)
	activeBeta, ok := registry.Get("panic-owner-beta")
	require.True(t, ok)
	assert.Same(t, beta, activeBeta)
	assert.Equal(t, "panic-owner-beta", performPluginRequest(outer, http.MethodPost, "/panic/reconsider").Body.String())
	assert.Contains(t, registry.RoutingErrors()["panic-owner-alpha"], "registration panic")
	assert.NotContains(t, registry.RoutingErrors(), "panic-owner-beta")
}

func TestUpdatedRegistrationPanicRestoresIncumbent(t *testing.T) {
	v1 := compileRouterPlugin(t, "panic-update", "1.0.0", `[
		{method: "GET", path: "/panic/stable", type: "dynamic"}
	]`)
	v2 := compileRouterPlugin(t, "panic-update", "2.0.0", `[
		{method: "GET", path: "/panic/stable", type: "dynamic"}
	]`)
	registry := jsplugin.NewRegistry()
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{v1}))
	outer := newOuterPluginTestEngine()
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
		c.String(http.StatusOK, binding.Plugin.Meta.Version)
	})
	builder := newPluginGenerationBuilder(outer.Routes(), nil, handlers)
	normalRegister := builder.registerRoute
	builder.registerRoute = func(engine *gin.Engine, binding jsplugin.RouteBinding, routeHandlers []gin.HandlerFunc) {
		if binding.Plugin.Meta.Version == "2.0.0" {
			panic("new version registration failed")
		}
		normalRegister(engine, binding, routeHandlers)
	}
	require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)

	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{v2}))

	active, ok := registry.Get("panic-update")
	require.True(t, ok)
	assert.Same(t, v1, active)
	assert.Equal(t, "1.0.0", performPluginRequest(outer, http.MethodGet, "/panic/stable").Body.String())
	assert.Contains(t, registry.RoutingErrors()["panic-update"], "registration panic")
}

func TestUnattributableRebuildFailureRetainsOldGeneration(t *testing.T) {
	v1 := compileRouterPlugin(t, "rebuild-stable", "1.0.0", `[
		{method: "GET", path: "/rebuild/version", type: "dynamic"}
	]`)
	v2 := compileRouterPlugin(t, "rebuild-stable", "2.0.0", `[
		{method: "GET", path: "/rebuild/version", type: "dynamic"}
	]`)
	handlers := testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
		c.String(http.StatusOK, binding.Plugin.Meta.Version)
	})
	outer := newOuterPluginTestEngine()
	registry := jsplugin.NewRegistry()
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{v1}))
	builder := newPluginGenerationBuilder(outer.Routes(), nil, handlers)
	require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)
	before := registry.Generation()

	normalConfigure := builder.configure
	builder.configure = func(*gin.Engine) error { return errors.New("engine configuration failed") }
	require.Error(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{v2}))
	assert.Same(t, before, registry.Generation())

	assert.Equal(t, "1.0.0", performPluginRequest(outer, http.MethodGet, "/rebuild/version").Body.String())
	assert.Contains(t, registry.LastRebuildError(), "engine configuration failed")

	builder.configure = normalConfigure
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{v2}))
	assert.Equal(t, "2.0.0", performPluginRequest(outer, http.MethodGet, "/rebuild/version").Body.String())
}

func TestPluginRouteRecoverySanitizesPanicResponse(t *testing.T) {
	plugin := compileRouterPlugin(t, "panic-route", "1.0.0", `[
		{method: "GET", path: "/vendor/panic", type: "dynamic"}
	]`)
	var calls atomic.Int32
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, testPluginRouteHandlers(
		func(c *gin.Context, _ *jsplugin.RoutingGeneration, _ jsplugin.RouteBinding) {
			if calls.Add(1) == 1 {
				panic("https://secret.example/internal?token=credential")
			}
			c.String(http.StatusOK, "recovered")
		},
	))
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)

	recorder := performPluginRequest(outer, http.MethodGet, "/vendor/panic")

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "internal plugin route error")
	assert.NotContains(t, recorder.Body.String(), "secret.example")
	assert.NotContains(t, recorder.Body.String(), "credential")

	second := performPluginRequest(outer, http.MethodGet, "/vendor/panic")
	assert.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, "recovered", second.Body.String())
}

func TestProductionPluginRoutePipelineRequiresTokenAuth(t *testing.T) {
	plugin := compileRouterPlugin(t, "forced-auth", "1.0.0", `[
		{method: "GET", path: "/vendor/protected/:task_id", type: "query", render: "native"}
	]`)
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, productionPluginRouteHandlers)
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)

	recorder := performPluginRequest(outer, http.MethodGet, "/vendor/protected/task-1")

	assert.NotEqual(t, http.StatusNotImplemented, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "error")
}

func TestProductionPluginNativeQueryTraversesInnerRouter(t *testing.T) {
	previousDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Task{}))
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, database.Create(&model.Task{
		TaskID:    "task_native_router",
		Platform:  constant.TaskPlatform("kling"),
		UserId:    91,
		Status:    model.TaskStatusSuccess,
		Progress:  "100%",
		ChannelId: 17,
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "private_upstream_id",
			ResultURL:      "https://secret.example/video.mp4",
		},
	}).Error)

	kling, found := jsplugin.DefaultRegistry.Get("kling")
	require.True(t, found)
	authenticatedProductionHandlers := func(
		generation *jsplugin.RoutingGeneration,
		binding jsplugin.RouteBinding,
	) []gin.HandlerFunc {
		production := productionPluginRouteHandlers(generation, binding)
		return []gin.HandlerFunc{
			production[0],
			func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyUserId, 91)
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				c.Next()
			},
			production[2],
			production[3],
			production[4],
			production[5],
			production[6],
		}
	}
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{kling}, authenticatedProductionHandlers)
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)

	request := httptest.NewRequest(http.MethodGet, "/kling/v1/videos/text2video/task_native_router", nil)
	recorder := httptest.NewRecorder()
	outer.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"task_id":"task_native_router"`)
	assert.Contains(t, recorder.Body.String(), `"task_status":"succeed"`)
	assert.NotContains(t, recorder.Body.String(), "private_upstream_id")
	assert.NotContains(t, recorder.Body.String(), "secret.example")
}

func newPluginRouterTest(
	t *testing.T,
	plugins []*jsplugin.LoadedPlugin,
	handlers pluginRouteHandlers,
) (*gin.Engine, *jsplugin.Registry) {
	t.Helper()
	return newPluginRouterTestWithProxies(t, plugins, handlers, nil)
}

func newPluginRouterTestWithProxies(
	t *testing.T,
	plugins []*jsplugin.LoadedPlugin,
	handlers pluginRouteHandlers,
	trustedProxies []string,
) (*gin.Engine, *jsplugin.Registry) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	outer := newOuterPluginTestEngine()
	registry := jsplugin.NewRegistry()
	if plugins != nil {
		require.NoError(t, registry.ReplaceOverrides(plugins))
	}
	if handlers == nil {
		handlers = testPluginRouteHandlers(func(c *gin.Context, _ *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) {
			c.String(http.StatusOK, binding.Plugin.Meta.Key)
		})
	}
	builder := newPluginGenerationBuilder(outer.Routes(), trustedProxies, handlers)
	require.NoError(t, registry.SetGenerationPreparer(builder.prepare))
	return outer, registry
}

func newOuterPluginTestEngine() *gin.Engine {
	outer := gin.New()
	outer.Use(func(c *gin.Context) {
		c.Set(common.RequestIdKey, "request-phase-two")
		c.Set(string(constant.ContextKeyLanguage), "zh-CN")
		c.Set(middleware.RouteTagKey, "before")
		c.Next()
	})
	return outer
}

func testPluginRouteHandlers(
	handler func(*gin.Context, *jsplugin.RoutingGeneration, jsplugin.RouteBinding),
) pluginRouteHandlers {
	return func(generation *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) []gin.HandlerFunc {
		return []gin.HandlerFunc{func(c *gin.Context) {
			pinnedGeneration := generation
			if state, _ := c.Request.Context().Value(pluginDispatchStateKey{}).(*pluginDispatchState); state != nil && state.generation != nil {
				pinnedGeneration = state.generation
			}
			c.Set(jsplugin.ContextKeyPinnedRoute, jsplugin.PinnedRoute{
				Generation: pinnedGeneration,
				Plugin:     binding.Plugin,
				Route:      binding.Route,
			})
			handler(c, pinnedGeneration, binding)
		}}
	}
}

func compileRouterPlugin(t *testing.T, key, version, routes string) *jsplugin.LoadedPlugin {
	t.Helper()
	plugin, err := jsplugin.CompilePlugin(routerPluginSource(key, version, routes), jsplugin.Options{Key: key, Version: version})
	require.NoError(t, err)
	return plugin
}

func routerPluginSource(key, version, routes string) string {
	return fmt.Sprintf(`
export const meta = {
	apiVersion: 1,
	key: %q,
	name: %q,
	version: %q,
	author: {name: "Test"},
	models: ["model"],
	fetchMode: "per_task",
	routes: (%s).map(function(route) {
		const migrated = Object.assign({}, route);
		delete migrated.renderer;
		migrated.render = route.render || route.renderer || "render";
		if (route.type !== "query") migrated.decode = route.decode || "decode";
		return migrated;
	}),
};
export const native = {
	decode: function(ctx) { return {kind: "submit", model: "model", requestBody: ctx.body.value}; },
	render: function(ctx, task) { return task; },
	native: function(ctx, task) { return task; },
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`, key, key, version, routes)
}

func performPluginRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(""))
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestWebFallbackDoesNotCacheMissingAPIOrAssets(t *testing.T) {
	outer := gin.New()
	SetWebRouter(outer, WebAssets{IndexPage: []byte("dashboard")}, func(c *gin.Context) { c.Next() })
	for _, path := range []string{"/api/user/token/status", "/api/audit/self?p=1", "/v1/missing", "/assets/missing.js"} {
		t.Run(path, func(t *testing.T) {
			response := performPluginRequest(outer, http.MethodGet, path)
			assert.Equal(t, http.StatusNotFound, response.Code)
			assert.Contains(t, response.Body.String(), "Invalid URL")
			assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
			assert.NotContains(t, response.Header().Get("Cache-Control"), "604800")
		})
	}
	page := performPluginRequest(outer, http.MethodGet, "/security")
	assert.Equal(t, http.StatusOK, page.Code)
	assert.Equal(t, "dashboard", page.Body.String())
	assert.Equal(t, "no-cache", page.Header().Get("Cache-Control"))
}

func TestSecurityRoutesDisableCachingBeforeAuthentication(t *testing.T) {
	outer := gin.New()
	SetApiRouter(outer)
	for _, path := range []string{"/api/user/token/status", "/api/user/token", "/api/audit/self", "/api/audit"} {
		t.Run(path, func(t *testing.T) {
			response := performPluginRequest(outer, http.MethodGet, path)
			assert.Equal(t, http.StatusUnauthorized, response.Code)
			assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
		})
	}
}
