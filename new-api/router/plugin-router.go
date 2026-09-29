package router

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/gin-gonic/gin"
)

type pluginDispatchStateKey struct{}

type pluginDispatchState struct {
	generation *jsplugin.RoutingGeneration
	hit        atomic.Bool
	writer     *gatedResponseWriter
	requestID  string
	language   string
}

func (s *pluginDispatchState) markHit() {
	s.hit.Store(true)
	s.writer.activate()
}

type pluginRouteHandlers func(*jsplugin.RoutingGeneration, jsplugin.RouteBinding) []gin.HandlerFunc

type pluginGenerationBuilder struct {
	staticRoutes   []gin.RouteInfo
	trustedProxies []string
	routeHandlers  pluginRouteHandlers
	registerRoute  func(*gin.Engine, jsplugin.RouteBinding, []gin.HandlerFunc)
	configure      func(*gin.Engine) error
}

type pluginRouteDispatcher struct {
	registry *jsplugin.Registry
}

func SetPluginRouter(outer *gin.Engine) gin.HandlerFunc {
	trustedProxies, _, err := common.ResolveTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	dispatcher := &pluginRouteDispatcher{registry: jsplugin.DefaultRegistry}
	if err != nil {
		common.SysError("configure plugin router trusted proxies: " + err.Error())
		return dispatcher.dispatch
	}
	builder := newPluginGenerationBuilder(outer.Routes(), trustedProxies, productionPluginRouteHandlers)
	if err = jsplugin.DefaultRegistry.SetGenerationPreparer(builder.prepare); err != nil {
		common.SysError("build initial plugin router: " + err.Error())
	}
	return dispatcher.dispatch
}

func newPluginGenerationBuilder(staticRoutes []gin.RouteInfo, trustedProxies []string, handlers pluginRouteHandlers) *pluginGenerationBuilder {
	builder := &pluginGenerationBuilder{
		staticRoutes:   append([]gin.RouteInfo(nil), staticRoutes...),
		trustedProxies: append([]string(nil), trustedProxies...),
		routeHandlers:  handlers,
	}
	builder.registerRoute = func(engine *gin.Engine, binding jsplugin.RouteBinding, routeHandlers []gin.HandlerFunc) {
		engine.Handle(binding.Route.Method, binding.Route.Path, routeHandlers...)
	}
	builder.configure = func(engine *gin.Engine) error {
		return common.ConfigureTrustedProxies(engine, builder.trustedProxies)
	}
	return builder
}

func productionPluginRouteHandlers(generation *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) []gin.HandlerFunc {
	pinRoute := func(c *gin.Context) {
		pinnedGeneration := generation
		if state, _ := c.Request.Context().Value(pluginDispatchStateKey{}).(*pluginDispatchState); state != nil && state.generation != nil {
			pinnedGeneration = state.generation
		}
		c.Set(jsplugin.ContextKeyPinnedPlugin, jsplugin.PinnedPlugin{
			Generation: pinnedGeneration,
			Plugin:     binding.Plugin,
		})
		c.Set(jsplugin.ContextKeyPinnedRoute, jsplugin.PinnedRoute{
			Generation: pinnedGeneration,
			Plugin:     binding.Plugin,
			Route:      binding.Route,
		})
		logger.LogDebug(
			c,
			"task_plugin subsystem=router event=route_matched generation=%d plugin=%q version=%q method=%q route_type=%q",
			pinnedGeneration.Number,
			binding.Plugin.Meta.Key,
			binding.Plugin.Meta.Version,
			binding.Route.Method,
			binding.Route.Type,
		)
		c.Next()
		logger.LogDebug(
			c,
			"task_plugin subsystem=router event=route_complete generation=%d plugin=%q method=%q status=%d",
			pinnedGeneration.Number,
			binding.Plugin.Meta.Key,
			binding.Route.Method,
			c.Writer.Status(),
		)
	}
	return []gin.HandlerFunc{
		pinRoute,
		middleware.TokenAuth(),
		middleware.SystemPerformanceCheck(),
		middleware.ModelRequestRateLimit(),
		middleware.PrepareTaskPluginRoute(),
		middleware.Distribute(),
		controller.RelayTask,
	}
}

func (b *pluginGenerationBuilder) prepare(candidate, current *jsplugin.RoutingGeneration) (jsplugin.PreparedRoutingGeneration, error) {
	blocked := make(map[*jsplugin.LoadedPlugin]string)
	for {
		accepted, routingErrors := b.admitPlugins(candidate, current, blocked)
		plugins := sortedPlugins(accepted)
		filtered, err := candidate.RebuildWithPlugins(plugins)
		if err != nil {
			return jsplugin.PreparedRoutingGeneration{}, err
		}
		engine, offender, buildErr := b.buildInnerEngine(filtered)
		if buildErr == nil {
			return jsplugin.PreparedRoutingGeneration{
				Generation: filtered.WithRuntime(engine),
				Errors:     routingErrors,
			}, nil
		}
		if offender == "" {
			return jsplugin.PreparedRoutingGeneration{}, buildErr
		}

		failedPlugin := accepted[offender]
		if failedPlugin == nil {
			return jsplugin.PreparedRoutingGeneration{}, fmt.Errorf("public route rebuild attributed failure to absent plugin %q: %w", offender, buildErr)
		}
		blocked[failedPlugin] = fmt.Sprintf("plugin %s rejected while rebuilding public routes: %v", offender, buildErr)
	}
}

func (b *pluginGenerationBuilder) admitPlugins(
	candidate, current *jsplugin.RoutingGeneration,
	blocked map[*jsplugin.LoadedPlugin]string,
) (map[string]*jsplugin.LoadedPlugin, map[string]string) {
	accepted := make(map[string]*jsplugin.LoadedPlugin)
	currentByKey := make(map[string]*jsplugin.LoadedPlugin)
	if current != nil && current.RuntimeHandler() != nil {
		for _, plugin := range current.Plugins() {
			currentByKey[plugin.Meta.Key] = plugin
		}
	}

	unchangedKeys := make([]string, 0)
	changedKeys := make([]string, 0)
	newKeys := make([]string, 0)
	for _, plugin := range candidate.Plugins() {
		incumbent, existed := currentByKey[plugin.Meta.Key]
		switch {
		case existed && incumbent == plugin:
			unchangedKeys = append(unchangedKeys, plugin.Meta.Key)
		case existed:
			changedKeys = append(changedKeys, plugin.Meta.Key)
		default:
			newKeys = append(newKeys, plugin.Meta.Key)
		}
	}
	sort.Strings(unchangedKeys)
	sort.Strings(changedKeys)
	sort.Strings(newKeys)

	routingErrors := make(map[string]string)
	orderedKeys := append(unchangedKeys, changedKeys...)
	orderedKeys = append(orderedKeys, newKeys...)
	rejectedKeys := make([]string, 0)
	for _, key := range orderedKeys {
		plugin, _ := candidate.Get(key)
		if blockedError := blocked[plugin]; blockedError != "" {
			routingErrors[key] = blockedError
		} else if err := b.validatePlugin(plugin, accepted); err != nil {
			routingErrors[key] = fmt.Sprintf("plugin %s rejected from public routes: %v", key, err)
		} else {
			accepted[key] = plugin
			continue
		}
		rejectedKeys = append(rejectedKeys, key)
	}

	for _, key := range rejectedKeys {
		plugin, _ := candidate.Get(key)
		incumbent := currentByKey[key]
		if incumbent == nil || incumbent == plugin || !candidate.RetainsIncumbent(key) {
			continue
		}
		if blockedError := blocked[incumbent]; blockedError != "" {
			routingErrors[key] = blockedError
			continue
		}
		if err := b.validatePlugin(incumbent, accepted); err == nil {
			accepted[key] = incumbent
		}
	}
	return accepted, routingErrors
}

func (b *pluginGenerationBuilder) validatePlugin(plugin *jsplugin.LoadedPlugin, accepted map[string]*jsplugin.LoadedPlugin) error {
	for _, route := range plugin.Meta.Routes {
		for _, staticRoute := range b.staticRoutes {
			if routeIntersectsStaticRoute(route.Path, staticRoute.Path) {
				return fmt.Errorf("route %s %s intersects static route %s %s", route.Method, route.Path, staticRoute.Method, staticRoute.Path)
			}
		}
	}
	for index, left := range plugin.Meta.Routes {
		for _, right := range plugin.Meta.Routes[index+1:] {
			if left.Method != right.Method {
				continue
			}
			if routePatternsIntersect(left.Path, right.Path) {
				return fmt.Errorf("routes %s %s and %s overlap", left.Method, left.Path, right.Path)
			}
			if !routesGinCompatible(left.Path, right.Path) {
				return fmt.Errorf("routes %s %s and %s use incompatible wildcard names", left.Method, left.Path, right.Path)
			}
		}
	}
	for _, other := range accepted {
		if other.Meta.Key == plugin.Meta.Key {
			continue
		}
		for _, route := range plugin.Meta.Routes {
			for _, otherRoute := range other.Meta.Routes {
				if routePatternsIntersect(route.Path, otherRoute.Path) {
					return fmt.Errorf("route %s %s overlaps plugin %s route %s %s", route.Method, route.Path, other.Meta.Key, otherRoute.Method, otherRoute.Path)
				}
				if route.Method == otherRoute.Method && !routesGinCompatible(route.Path, otherRoute.Path) {
					return fmt.Errorf("route %s %s is structurally incompatible with plugin %s route %s", route.Method, route.Path, other.Meta.Key, otherRoute.Path)
				}
			}
		}
	}
	return nil
}

func (b *pluginGenerationBuilder) buildInnerEngine(generation *jsplugin.RoutingGeneration) (engine *gin.Engine, offender string, err error) {
	currentPlugin := ""
	defer func() {
		if recovered := recover(); recovered != nil {
			offender = currentPlugin
			err = fmt.Errorf("inner Gin registration panic: %v", recovered)
			engine = nil
		}
	}()

	engine = gin.New()
	engine.RedirectTrailingSlash = false
	engine.HandleMethodNotAllowed = true
	if err = b.configure(engine); err != nil {
		return nil, "", err
	}
	engine.Use(importPluginDispatchState())
	engine.Use(pluginRouteRecovery())
	engine.Use(middleware.BodyStorageCleanup())
	engine.NoMethod(func(c *gin.Context) {
		markPluginRouteHit(c)
		generation := uint64(0)
		if state, _ := c.Request.Context().Value(pluginDispatchStateKey{}).(*pluginDispatchState); state != nil && state.generation != nil {
			generation = state.generation.Number
		}
		logger.LogDebug(
			c,
			"task_plugin subsystem=router event=method_not_allowed generation=%d request_method=%q status=%d",
			generation,
			c.Request.Method,
			http.StatusMethodNotAllowed,
		)
		c.AbortWithStatus(http.StatusMethodNotAllowed)
	})

	for _, binding := range generation.Routes() {
		currentPlugin = binding.Plugin.Meta.Key
		b.registerRoute(engine, binding, b.routeHandlers(generation, binding))
	}
	currentPlugin = ""
	return engine, "", nil
}

func importPluginDispatchState() gin.HandlerFunc {
	return func(c *gin.Context) {
		state, _ := c.Request.Context().Value(pluginDispatchStateKey{}).(*pluginDispatchState)
		if state != nil {
			if c.FullPath() != "" {
				state.markHit()
			}
			if state.requestID != "" {
				c.Set(common.RequestIdKey, state.requestID)
			}
			if state.language != "" {
				c.Set(string(constant.ContextKeyLanguage), state.language)
			}
		}
		c.Set(middleware.RouteTagKey, "relay")
		c.Next()
	}
}

func markPluginRouteHit(c *gin.Context) {
	state, _ := c.Request.Context().Value(pluginDispatchStateKey{}).(*pluginDispatchState)
	if state != nil {
		state.markHit()
	}
}

func pluginRouteRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recover() == nil {
				return
			}
			common.SysError("panic recovered in plugin route")
			if pinnedValue, exists := c.Get(jsplugin.ContextKeyPinnedRoute); exists {
				if pinned, ok := pinnedValue.(jsplugin.PinnedRoute); ok && pinned.Plugin != nil && pinned.Generation != nil {
					logger.LogDebug(
						c,
						"task_plugin subsystem=router event=panic_recovered generation=%d plugin=%q method=%q",
						pinned.Generation.Number,
						pinned.Plugin.Meta.Key,
						pinned.Route.Method,
					)
				}
			}
			c.Abort()
			if !c.Writer.Written() {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": gin.H{
						"message": "internal plugin route error",
						"type":    "plugin_route_error",
					},
				})
			}
		}()
		c.Next()
	}
}

func (d *pluginRouteDispatcher) dispatch(c *gin.Context) {
	generation := d.registry.Generation()
	if generation == nil || generation.RuntimeHandler() == nil {
		c.Next()
		return
	}

	previousTag, hadPreviousTag := c.Get(middleware.RouteTagKey)
	c.Set(middleware.RouteTagKey, "relay")
	originalContext := c.Request.Context()
	state := &pluginDispatchState{
		generation: generation,
		requestID:  c.GetString(common.RequestIdKey),
		language:   c.GetString(string(constant.ContextKeyLanguage)),
	}
	gatedWriter := newGatedResponseWriter(c.Writer)
	state.writer = gatedWriter
	c.Request = c.Request.WithContext(context.WithValue(originalContext, pluginDispatchStateKey{}, state))

	generation.RuntimeHandler().ServeHTTP(gatedWriter, c.Request)
	if state.hit.Load() {
		if !c.Writer.Written() {
			c.Writer.WriteHeaderNow()
		}
		c.Abort()
		return
	}

	c.Request = c.Request.WithContext(originalContext)
	if hadPreviousTag {
		c.Set(middleware.RouteTagKey, previousTag)
	} else {
		delete(c.Keys, middleware.RouteTagKey)
	}
	c.Next()
}

type gatedResponseWriter struct {
	underlying    gin.ResponseWriter
	privateHeader http.Header
	active        atomic.Bool
	activateOnce  sync.Once
	pendingStatus int
}

func newGatedResponseWriter(underlying gin.ResponseWriter) *gatedResponseWriter {
	return &gatedResponseWriter{
		underlying:    underlying,
		privateHeader: underlying.Header().Clone(),
	}
}

func (w *gatedResponseWriter) activate() {
	w.activateOnce.Do(func() {
		target := w.underlying.Header()
		for key := range target {
			target.Del(key)
		}
		for key, values := range w.privateHeader {
			target[key] = append([]string(nil), values...)
		}
		w.active.Store(true)
		if w.pendingStatus != 0 {
			w.underlying.WriteHeader(w.pendingStatus)
		}
	})
}

func (w *gatedResponseWriter) Header() http.Header {
	if w.active.Load() {
		return w.underlying.Header()
	}
	return w.privateHeader
}

func (w *gatedResponseWriter) WriteHeader(statusCode int) {
	if w.active.Load() {
		w.underlying.WriteHeader(statusCode)
		return
	}
	if w.pendingStatus == 0 {
		w.pendingStatus = statusCode
	}
}

func (w *gatedResponseWriter) Write(data []byte) (int, error) {
	if !w.active.Load() {
		return len(data), nil
	}
	return w.underlying.Write(data)
}

func (w *gatedResponseWriter) Flush() {
	if w.active.Load() {
		w.underlying.Flush()
	}
}

func (w *gatedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !w.active.Load() {
		return nil, nil, errors.New("cannot hijack an unmatched plugin route")
	}
	return w.underlying.Hijack()
}

func (w *gatedResponseWriter) CloseNotify() <-chan bool {
	if w.active.Load() {
		return w.underlying.CloseNotify()
	}
	never := make(chan bool)
	return never
}

func (w *gatedResponseWriter) Push(target string, options *http.PushOptions) error {
	if !w.active.Load() {
		return http.ErrNotSupported
	}
	pusher := w.underlying.Pusher()
	if pusher == nil {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

func sortedPluginKeys(plugins map[string]*jsplugin.LoadedPlugin) []string {
	keys := make([]string, 0, len(plugins))
	for key := range plugins {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedPlugins(plugins map[string]*jsplugin.LoadedPlugin) []*jsplugin.LoadedPlugin {
	keys := sortedPluginKeys(plugins)
	sorted := make([]*jsplugin.LoadedPlugin, 0, len(keys))
	for _, key := range keys {
		sorted = append(sorted, plugins[key])
	}
	return sorted
}

type routePatternSegment struct {
	value    string
	dynamic  bool
	catchAll bool
}

func parseRoutePattern(routePath string) []routePatternSegment {
	parts := strings.Split(strings.TrimPrefix(routePath, "/"), "/")
	segments := make([]routePatternSegment, 0, len(parts))
	for _, part := range parts {
		segments = append(segments, routePatternSegment{
			value:    part,
			dynamic:  strings.HasPrefix(part, ":") || strings.HasPrefix(part, "*"),
			catchAll: strings.HasPrefix(part, "*"),
		})
	}
	return segments
}

func routePatternsIntersect(leftPath, rightPath string) bool {
	left := parseRoutePattern(leftPath)
	right := parseRoutePattern(rightPath)
	for index := 0; ; index++ {
		leftDone := index >= len(left)
		rightDone := index >= len(right)
		if leftDone || rightDone {
			return leftDone && rightDone
		}
		if left[index].catchAll || right[index].catchAll {
			return true
		}
		if !left[index].dynamic && !right[index].dynamic && left[index].value != right[index].value {
			return false
		}
		if (left[index].dynamic && right[index].value == "") || (right[index].dynamic && left[index].value == "") {
			return false
		}
	}
}

func routesGinCompatible(leftPath, rightPath string) bool {
	left := parseRoutePattern(leftPath)
	right := parseRoutePattern(rightPath)
	limit := min(len(right), len(left))
	for index := range limit {
		leftSegment := left[index]
		rightSegment := right[index]
		if !leftSegment.dynamic && !rightSegment.dynamic {
			if leftSegment.value != rightSegment.value {
				return true
			}
			continue
		}
		if leftSegment.dynamic && rightSegment.dynamic {
			if leftSegment.value != rightSegment.value {
				return false
			}
			continue
		}
		return true
	}
	return true
}

func routeIntersectsStaticRoute(pluginPath, staticPath string) bool {
	if routePatternsIntersect(pluginPath, staticPath) {
		return true
	}
	if catchAllIndex := strings.LastIndex(staticPath, "/*"); catchAllIndex >= 0 && catchAllIndex+2 < len(staticPath) {
		prefix := staticPath[:catchAllIndex]
		if routePatternsIntersect(pluginPath, prefix) || routePatternsIntersect(pluginPath, prefix+"/") {
			return true
		}
		return false
	}
	if staticPath == "/" {
		return false
	}
	alternate := strings.TrimSuffix(staticPath, "/")
	if alternate == staticPath {
		alternate += "/"
	}
	return routePatternsIntersect(pluginPath, alternate)
}
