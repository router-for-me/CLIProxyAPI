package jsplugin

import (
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/constant"
)

type RouteType string

const (
	RouteTypeSubmit  RouteType = "submit"
	RouteTypeQuery   RouteType = "query"
	RouteTypeDynamic RouteType = "dynamic"
)

type Route struct {
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Type        RouteType `json:"type"`
	Action      string    `json:"action,omitempty"`
	Decode      string    `json:"decode,omitempty"`
	Render      string    `json:"render,omitempty"`
	TaskIDParam string    `json:"taskIdParam,omitempty"`
	// RetainResult, when explicitly false on a submit or dynamic route, tells
	// the host not to persist the upstream snapshot of an immediate terminal
	// result and to treat the task as not found on every retrieval surface
	// afterwards. nil means the route did not declare it (retain).
	RetainResult *bool `json:"retainResult,omitempty"`
	// Models restricts this route to the listed models. The host matches the
	// canonical top-level "model" body field before any JS hook runs; empty
	// means unrestricted. Must be a subset of meta.models.
	Models []string `json:"models,omitempty"`
}

// ProtocolClaim is one entry of meta.protocols. Models narrows the protocol's
// endpoint bindings to a subset of meta.models; empty binds every model.
// Supports names the request forms a mode-bearing protocol accepts; decode
// and normalize rewrite it into host-table order.
type ProtocolClaim struct {
	Name       string   `json:"name"`
	Models     []string `json:"models,omitempty"`
	Supports   []string `json:"supports,omitempty"`
	objectForm bool
}

// ProtocolMode is one client request form a host protocol operation accepts
// and the plugin hook that implements it.
type ProtocolMode struct {
	Name string
	Hook string
}

type BodyKind string

const (
	BodyNone      BodyKind = "none"
	BodyJSON      BodyKind = "json"
	BodyForm      BodyKind = "form"
	BodyMultipart BodyKind = "multipart"
)

type HostProtocolOperation struct {
	Name                    string
	Methods                 []string
	Path                    string
	BodyKinds               []BodyKind
	ModelField              string
	RequiredProtocolMembers []string
	Modes                   []ProtocolMode
	RequiredDriverHooks     []string
}

type HostProtocolDefinition struct {
	Name       string
	Operations []HostProtocolOperation
}

var hostProtocols = []HostProtocolDefinition{
	{Name: "openai_responses", Operations: []HostProtocolOperation{
		{Name: "create", Methods: []string{http.MethodPost}, Path: "/v1/responses", BodyKinds: []BodyKind{BodyJSON}, ModelField: "model", RequiredProtocolMembers: []string{"decodeRequest"}, Modes: []ProtocolMode{{Name: "stream", Hook: "renderEvents"}, {Name: "sync", Hook: "renderFinal"}, {Name: "background", Hook: "renderFinal"}}},
		{Name: "retrieve", Methods: []string{http.MethodGet}, Path: "/v1/responses/:response_id", BodyKinds: []BodyKind{BodyNone}},
	}},
	{Name: "openai_video", Operations: []HostProtocolOperation{
		{Name: "create", Methods: []string{http.MethodPost}, Path: "/v1/videos", BodyKinds: []BodyKind{BodyJSON, BodyMultipart}, ModelField: "model", RequiredProtocolMembers: []string{"decodeRequest"}},
		{Name: "retrieve", Methods: []string{http.MethodGet}, Path: "/v1/videos/:task_id", BodyKinds: []BodyKind{BodyNone}, RequiredProtocolMembers: []string{"render"}},
		{Name: "content", Methods: []string{http.MethodGet, http.MethodHead}, Path: "/v1/videos/:task_id/content", BodyKinds: []BodyKind{BodyNone}, RequiredDriverHooks: []string{"listArtifacts", "buildContentRequest"}},
	}},
	// The OpenAI Images API is synchronous: both operations create a task and
	// the host answers with the rendered image response once the task is
	// terminal, so there is no retrieve operation and no request modes.
	{Name: ProtocolOpenAIImage, Operations: []HostProtocolOperation{
		{Name: "generate", Methods: []string{http.MethodPost}, Path: "/v1/images/generations", BodyKinds: []BodyKind{BodyJSON}, ModelField: "model", RequiredProtocolMembers: []string{"decodeRequest", "render"}},
		{Name: "edit", Methods: []string{http.MethodPost}, Path: "/v1/images/edits", BodyKinds: []BodyKind{BodyJSON, BodyMultipart}, ModelField: "model", RequiredProtocolMembers: []string{"decodeRequest", "render"}},
	}},
}

// ProtocolOpenAIImage is the host protocol that serves the OpenAI Images API
// (`POST /v1/images/generations` and `POST /v1/images/edits`) from a plugin.
const ProtocolOpenAIImage = "openai_image"

func HostProtocol(name string) (HostProtocolDefinition, bool) {
	for _, definition := range hostProtocols {
		if definition.Name == name {
			return definition, true
		}
	}
	return HostProtocolDefinition{}, false
}

func HostProtocols() []HostProtocolDefinition {
	definitions := make([]HostProtocolDefinition, len(hostProtocols))
	for index, definition := range hostProtocols {
		definitions[index] = definition
		definitions[index].Operations = append([]HostProtocolOperation(nil), definition.Operations...)
		for operationIndex := range definitions[index].Operations {
			operation := &definitions[index].Operations[operationIndex]
			operation.Methods = append([]string(nil), operation.Methods...)
			operation.BodyKinds = append([]BodyKind(nil), operation.BodyKinds...)
			operation.RequiredProtocolMembers = append([]string(nil), operation.RequiredProtocolMembers...)
			operation.Modes = append([]ProtocolMode(nil), operation.Modes...)
			operation.RequiredDriverHooks = append([]string(nil), operation.RequiredDriverHooks...)
		}
	}
	return definitions
}

// DefinedModes returns each distinct mode on the protocol in host-table order.
func (d HostProtocolDefinition) DefinedModes() []ProtocolMode {
	seen := make(map[string]struct{})
	modes := make([]ProtocolMode, 0)
	for _, operation := range d.Operations {
		for _, mode := range operation.Modes {
			if _, exists := seen[mode.Name]; exists {
				continue
			}
			seen[mode.Name] = struct{}{}
			modes = append(modes, mode)
		}
	}
	return modes
}

func orderProtocolSupports(protocol string, supports []string) []string {
	if len(supports) == 0 {
		return supports
	}
	definition, ok := HostProtocol(protocol)
	if !ok {
		return supports
	}
	rank := make(map[string]int)
	for index, mode := range definition.DefinedModes() {
		rank[mode.Name] = index
	}
	ordered := append([]string(nil), supports...)
	slices.SortStableFunc(ordered, func(left, right string) int {
		leftRank, leftKnown := rank[left]
		rightRank, rightKnown := rank[right]
		switch {
		case leftKnown && rightKnown:
			if leftRank < rightRank {
				return -1
			}
			if leftRank > rightRank {
				return 1
			}
			return 0
		case leftKnown:
			return -1
		case rightKnown:
			return 1
		default:
			return 0
		}
	})
	return ordered
}

func LookupHostProtocolOperation(method, path string) (string, HostProtocolOperation, bool) {
	method = strings.ToUpper(strings.TrimSpace(method))
	for _, definition := range hostProtocols {
		for _, operation := range definition.Operations {
			if operation.Path != path || operation.ModelField == "" {
				continue
			}
			if slices.Contains(operation.Methods, method) {
				return definition.Name, operation, true
			}
		}
	}
	return "", HostProtocolOperation{}, false
}

type RouteBinding struct {
	Plugin *LoadedPlugin
	Route  Route
}

type ProtocolBinding struct {
	Plugin    *LoadedPlugin
	Protocol  string
	Operation HostProtocolOperation
	Model     string
}

const (
	ContextKeyPinnedPlugin    = "task_plugin_pinned_plugin"
	ContextKeyPinnedRoute     = "task_plugin_pinned_route"
	ContextKeyPinnedEndpoint  = "task_plugin_pinned_endpoint"
	ContextKeyRouteRequest    = "task_plugin_route_request"
	ContextKeyProtocolRequest = "task_plugin_protocol_request"
)

type PinnedPlugin struct {
	Generation *RoutingGeneration
	Plugin     *LoadedPlugin
}

type PinnedRoute struct {
	Generation *RoutingGeneration
	Plugin     *LoadedPlugin
	Route      Route
}

// PinnedEndpoint carries the exact generation and endpoint candidates selected
// before distribution. Plugin initially names the deterministic request parser;
// distribution may rebind it to another candidate from the same generation
// when multiple legacy providers expose the same model.
type PinnedEndpoint struct {
	Generation  *RoutingGeneration
	Plugin      *LoadedPlugin
	Protocol    string
	Operation   HostProtocolOperation
	Model       string
	MappedModel string
	Candidates  []ProtocolBinding
}

// FileReference returns the opaque ref of the index-th uploaded file in a
// multipart field. The first file keeps the historical `request_file:<field>`
// spelling; later files in the same repeated field (`image[]`, `image[]`)
// append `#<index>` so a plugin can address each of them.
func FileReference(field string, index int) string {
	if index <= 0 {
		return "request_file:" + field
	}
	return "request_file:" + field + "#" + strconv.Itoa(index)
}

// ParseFileReference resolves a ref produced by FileReference back to the
// multipart field and the zero-based file index within that field.
func ParseFileReference(ref string) (field string, index int, ok bool) {
	rest, found := strings.CutPrefix(ref, "request_file:")
	if !found || rest == "" {
		return "", 0, false
	}
	field, suffix, hasIndex := strings.Cut(rest, "#")
	if field == "" {
		return "", 0, false
	}
	if !hasIndex {
		return field, 0, true
	}
	parsed, err := strconv.Atoi(suffix)
	if err != nil || parsed < 0 || strconv.Itoa(parsed) != suffix {
		return "", 0, false
	}
	return field, parsed, true
}

// RouteRequestContext is the canonical request view exposed to declarative
// routing hooks. RequestBody contains decoded JSON or multipart text fields;
// raw binary and multipart file bytes remain host-owned.
type RouteRequestContext struct {
	Path        string              `json:"path"`
	Method      string              `json:"method"`
	Params      map[string]string   `json:"params"`
	Query       map[string][]string `json:"query"`
	Body        any                 `json:"body"`
	Files       []map[string]any    `json:"-"`
	RequestBody any                 `json:"-"`
}

func (r RouteRequestContext) JSValue() map[string]any {
	params := make(map[string]string, len(r.Params))
	maps.Copy(params, r.Params)
	query := make(map[string][]string, len(r.Query))
	for key, values := range r.Query {
		query[key] = append([]string(nil), values...)
	}
	return map[string]any{
		"path":   r.Path,
		"method": r.Method,
		"params": params,
		"query":  query,
		"body":   clonePluginRequestValue(r.Body),
	}
}

func clonePluginRequestValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = clonePluginRequestValue(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = clonePluginRequestValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case map[string][]string:
		cloned := make(map[string][]string, len(typed))
		for key, values := range typed {
			cloned[key] = append([]string(nil), values...)
		}
		return cloned
	case []map[string]any:
		cloned := make([]map[string]any, len(typed))
		for index, item := range typed {
			cloned[index] = clonePluginRequestValue(item).(map[string]any)
		}
		return cloned
	default:
		return value
	}
}

type ProtocolRequestContext struct {
	RouteRequestContext
	Protocol  string `json:"protocol"`
	Operation string `json:"operation"`
	Model     string `json:"model"`
	// UpstreamModel is the declared machine identity when Model is a
	// channel-mapping alias; empty otherwise. Decode hooks that key rate
	// tables or request shaping by model must use it over Model.
	UpstreamModel string `json:"upstreamModel,omitempty"`
	Stream        bool   `json:"stream"`
}

func (p ProtocolRequestContext) JSValue() map[string]any {
	value := p.RouteRequestContext.JSValue()
	value["protocol"] = p.Protocol
	value["operation"] = p.Operation
	value["model"] = p.Model
	if p.UpstreamModel != "" {
		value["upstreamModel"] = p.UpstreamModel
	}
	value["stream"] = p.Stream
	return value
}

// SupportsHostProtocol reports whether the current host release has a
// concrete wire/state machine for an otherwise valid manifest endpoint.
func SupportsHostProtocol(protocol string) bool { _, ok := HostProtocol(protocol); return ok }

// RoutingGeneration is an immutable, request-pinnable view of all effective
// plugins and their deterministic routing indexes.
type RoutingGeneration struct {
	Number      uint64
	PublishedAt time.Time

	byKey                map[string]*LoadedPlugin
	byModel              map[string]*LoadedPlugin
	modelPlugins         map[string][]*LoadedPlugin
	canonicalModelByFold map[string]string
	byChannelType        map[int]*LoadedPlugin
	routeIndex           map[string]RouteBinding
	protocolIndex        map[string][]ProtocolBinding
	plugins              []*LoadedPlugin
	routes               []RouteBinding
	runtime              http.Handler
	retainCurrent        map[string]struct{}
}

var (
	routeMethodPattern = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE)$`)
	pathNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	staticSegment      = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	memberNamePattern  = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
)

var reservedRouteNamespaces = []string{
	"/api",
	"/assets",
	"/setup",
	"/v1/tasks",
	"/console",
	"/login",
	"/forbidden",
	"/sign-in",
	"/sign-up",
	"/forgot-password",
	"/oauth",
	"/otp",
	"/register",
	"/reset",
	"/privacy-policy",
	"/user-agreement",
	"/about",
	"/pricing",
	"/rankings",
	"/user",
	"/401",
	"/403",
	"/404",
	"/500",
	"/503",
	"/chat2link",
	"/system-settings",
	"/channels",
	"/chat",
	"/dashboard",
	"/errors",
	"/keys",
	"/models",
	"/playground",
	"/profile",
	"/redemption-codes",
	"/subscriptions",
	"/system-info",
	"/task-plugins",
	"/usage-logs",
	"/users",
	"/wallet",
}

func (g *RoutingGeneration) Get(key string) (*LoadedPlugin, bool) {
	if g == nil {
		return nil, false
	}
	plugin, ok := g.byKey[key]
	return plugin, ok
}

func (g *RoutingGeneration) GetByChannelType(channelType int) (*LoadedPlugin, bool) {
	if g == nil || channelType == 0 || channelType == constant.ChannelTypeTaskPlugin {
		return nil, false
	}
	plugin, ok := g.byChannelType[channelType]
	return plugin, ok
}

// GetByModel returns the deterministic effective plugin metadata used for a
// model-level host concern such as billing. When multiple providers expose the
// same model name, the first plugin in generation order owns that shared
// metadata view.
func (g *RoutingGeneration) GetByModel(model string) (*LoadedPlugin, bool) {
	if g == nil || model == "" {
		return nil, false
	}
	plugin, ok := g.byModel[model]
	return plugin, ok
}

// PluginsByModel returns all plugins declaring model, in ascending key order.
func (g *RoutingGeneration) PluginsByModel(model string) []*LoadedPlugin {
	if g == nil {
		return nil
	}
	return slices.Clone(g.modelPlugins[model])
}

// SharedModel reports whether multiple plugins declare a model without copying
// its provider list on the relay hot path.
func (g *RoutingGeneration) SharedModel(model string) bool {
	return g != nil && len(g.modelPlugins[model]) >= 2
}

// CanonicalModel returns the declared spelling for model. An exact byModel
// hit wins and returns the input unchanged; otherwise the ASCII-folded
// index is consulted. Miss and nil-receiver return ("", false).
func (g *RoutingGeneration) CanonicalModel(model string) (string, bool) {
	if g == nil || model == "" {
		return "", false
	}
	if _, ok := g.byModel[model]; ok {
		return model, true
	}
	declared, ok := g.canonicalModelByFold[asciiFold(model)]
	return declared, ok
}

// LookupDeclaredRoute resolves a manifest path declaration. It does not match
// an incoming concrete URL; runtime matching is delegated to Gin.
func (g *RoutingGeneration) LookupDeclaredRoute(method, path string) (RouteBinding, bool) {
	if g == nil {
		return RouteBinding{}, false
	}
	normalizedMethod, err := normalizeRouteMethod(method)
	if err != nil {
		return RouteBinding{}, false
	}
	shape, err := routePathShape(path)
	if err != nil {
		return RouteBinding{}, false
	}
	binding, ok := g.routeIndex[normalizedMethod+" "+shape]
	return binding, ok
}

func (g *RoutingGeneration) LookupEndpoint(method, path, model string) (ProtocolBinding, bool) {
	if g == nil {
		return ProtocolBinding{}, false
	}
	normalizedMethod, err := normalizeRouteMethod(method)
	if err != nil {
		return ProtocolBinding{}, false
	}
	bindings := g.protocolIndex[endpointIndexKey(normalizedMethod, path, model)]
	if len(bindings) == 0 {
		return ProtocolBinding{}, false
	}
	return bindings[0], true
}

// LookupEndpointCandidates returns every plugin that can serve a shared model
// endpoint, in ascending key order. Each candidate decodes before distribution.
func (g *RoutingGeneration) LookupEndpointCandidates(method, path, model string) []ProtocolBinding {
	if g == nil {
		return nil
	}
	normalizedMethod, err := normalizeRouteMethod(method)
	if err != nil {
		return nil
	}
	bindings := g.protocolIndex[endpointIndexKey(normalizedMethod, path, model)]
	return append([]ProtocolBinding(nil), bindings...)
}

func (g *RoutingGeneration) Plugins() []*LoadedPlugin {
	if g == nil {
		return nil
	}
	return append([]*LoadedPlugin(nil), g.plugins...)
}

func (g *RoutingGeneration) Routes() []RouteBinding {
	if g == nil {
		return nil
	}
	return append([]RouteBinding(nil), g.routes...)
}

// RuntimeHandler is the inner router built for this exact generation. It is
// published in the same atomic pointer as the routing indexes.
func (g *RoutingGeneration) RuntimeHandler() http.Handler {
	if g == nil {
		return nil
	}
	return g.runtime
}

func (g *RoutingGeneration) RetainsIncumbent(key string) bool {
	if g == nil {
		return false
	}
	_, retained := g.retainCurrent[key]
	return retained
}

// RebuildWithPlugins creates a generation from the supplied exact plugin
// pointers. It is used when a rejected hot update must retain the incumbent
// runtime object for that key.
func (g *RoutingGeneration) RebuildWithPlugins(plugins []*LoadedPlugin) (*RoutingGeneration, error) {
	if g == nil {
		return nil, fmt.Errorf("cannot rebuild a nil routing generation")
	}
	byKey := make(map[string]*LoadedPlugin, len(plugins))
	for _, plugin := range plugins {
		if plugin == nil {
			return nil, fmt.Errorf("cannot rebuild routing generation with a nil plugin")
		}
		if _, exists := g.byKey[plugin.Meta.Key]; !exists {
			return nil, fmt.Errorf("plugin %q is not present in routing generation %d", plugin.Meta.Key, g.Number)
		}
		if _, duplicate := byKey[plugin.Meta.Key]; duplicate {
			return nil, fmt.Errorf("plugin %q appears more than once in routing generation rebuild", plugin.Meta.Key)
		}
		byKey[plugin.Meta.Key] = plugin
	}
	rebuilt, err := buildRoutingGeneration(byKey, nil, g.Number)
	if err != nil {
		return nil, err
	}
	rebuilt.PublishedAt = g.PublishedAt
	rebuilt.retainCurrent = cloneStringSet(g.retainCurrent)
	return rebuilt, nil
}

// WithRuntime returns a shallow immutable copy carrying the prepared inner
// handler. Callers use it before publication; published generations must not
// be mutated.
func (g *RoutingGeneration) WithRuntime(handler http.Handler) *RoutingGeneration {
	if g == nil {
		return nil
	}
	prepared := *g
	prepared.runtime = handler
	return &prepared
}

func cloneStringSet(source map[string]struct{}) map[string]struct{} {
	if len(source) == 0 {
		return nil
	}
	clone := make(map[string]struct{}, len(source))
	for key := range source {
		clone[key] = struct{}{}
	}
	return clone
}

func normalizeRouteMethod(method string) (string, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if !routeMethodPattern.MatchString(method) {
		return "", fmt.Errorf("plugin route method %q is not supported", method)
	}
	return method, nil
}

// NormalizeRoutePath validates the canonical path syntax used by plugin route
// declarations. A trailing slash is allowed and remains significant.
func NormalizeRoutePath(routePath string) (string, error) {
	if routePath == "" || routePath[0] != '/' {
		return "", fmt.Errorf("plugin route path must start with /")
	}
	if routePath == "/" {
		return "", fmt.Errorf("plugin route path / is reserved")
	}
	if strings.ContainsAny(routePath, "?#%") {
		return "", fmt.Errorf("plugin route path %q must not contain a query, fragment, or percent-encoding", routePath)
	}
	if strings.Contains(routePath, "//") {
		return "", fmt.Errorf("plugin route path %q must not contain empty segments", routePath)
	}

	segments := strings.Split(strings.TrimPrefix(routePath, "/"), "/")
	seenNames := make(map[string]struct{})
	for index, segment := range segments {
		if segment == "" && index == len(segments)-1 {
			continue
		}
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("plugin route path %q must not contain dot segments", routePath)
		}
		if after, ok := strings.CutPrefix(segment, ":"); ok {
			name := after
			if !pathNamePattern.MatchString(name) {
				return "", fmt.Errorf("plugin route path %q has invalid parameter %q", routePath, segment)
			}
			if _, exists := seenNames[name]; exists {
				return "", fmt.Errorf("plugin route path %q repeats parameter %q", routePath, name)
			}
			seenNames[name] = struct{}{}
			continue
		}
		if after, ok := strings.CutPrefix(segment, "*"); ok {
			name := after
			if index != len(segments)-1 || !pathNamePattern.MatchString(name) {
				return "", fmt.Errorf("plugin route path %q has an invalid catch-all segment", routePath)
			}
			if _, exists := seenNames[name]; exists {
				return "", fmt.Errorf("plugin route path %q repeats parameter %q", routePath, name)
			}
			seenNames[name] = struct{}{}
			continue
		}
		if !staticSegment.MatchString(segment) {
			return "", fmt.Errorf("plugin route path %q has invalid segment %q", routePath, segment)
		}
	}
	return routePath, nil
}

func routePathShape(routePath string) (string, error) {
	normalized, err := NormalizeRoutePath(routePath)
	if err != nil {
		return "", err
	}
	segments := strings.Split(strings.TrimPrefix(normalized, "/"), "/")
	for index, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			segments[index] = ":"
		} else if strings.HasPrefix(segment, "*") {
			segments[index] = "*"
		}
	}
	return "/" + strings.Join(segments, "/"), nil
}

func endpointIndexKey(method, path, model string) string {
	return method + "\x00" + path + "\x00" + model
}

func intersectingReservedNamespace(routePath string) (string, bool) {
	for _, namespace := range reservedRouteNamespaces {
		if routePatternIntersectsNamespace(routePath, namespace) {
			return namespace, true
		}
	}
	return "", false
}

func routePatternIntersectsNamespace(routePath, namespace string) bool {
	routeSegments := strings.Split(strings.Trim(strings.TrimPrefix(routePath, "/"), "/"), "/")
	namespaceSegments := strings.Split(strings.TrimPrefix(namespace, "/"), "/")
	for index, namespaceSegment := range namespaceSegments {
		if index >= len(routeSegments) {
			return false
		}
		routeSegment := routeSegments[index]
		if strings.HasPrefix(routeSegment, "*") {
			return true
		}
		if !strings.HasPrefix(routeSegment, ":") && routeSegment != namespaceSegment {
			return false
		}
	}
	return true
}

func validateRoute(route *Route) error {
	if route.Method != strings.ToUpper(strings.TrimSpace(route.Method)) {
		return fmt.Errorf("plugin route method %q must use canonical uppercase spelling", route.Method)
	}
	method, err := normalizeRouteMethod(route.Method)
	if err != nil {
		return err
	}
	route.Method = method
	route.Path, err = NormalizeRoutePath(route.Path)
	if err != nil {
		return err
	}
	if namespace, reserved := intersectingReservedNamespace(route.Path); reserved {
		return fmt.Errorf("plugin route path %q intersects reserved namespace %s", route.Path, namespace)
	}
	switch route.Type {
	case RouteTypeSubmit:
		if route.Decode == "" || route.Render == "" || route.TaskIDParam != "" {
			return fmt.Errorf("submit route %s %s must declare decode and render and must not declare taskIdParam", route.Method, route.Path)
		}
	case RouteTypeQuery:
		if route.Decode != "" || strings.TrimSpace(route.Render) == "" {
			return fmt.Errorf("query route %s %s must declare render and must not declare decode", route.Method, route.Path)
		}
		if route.Action != "" {
			return fmt.Errorf("query route %s %s must not declare action", route.Method, route.Path)
		}
		if route.RetainResult != nil {
			return fmt.Errorf("query route %s %s must not declare retainResult", route.Method, route.Path)
		}
		if route.TaskIDParam == "" {
			route.TaskIDParam = "task_id"
		}
		if !pathNamePattern.MatchString(route.TaskIDParam) {
			return fmt.Errorf("query route %s %s has invalid taskIdParam %q", route.Method, route.Path, route.TaskIDParam)
		}
		if !pathHasParameter(route.Path, route.TaskIDParam) {
			return fmt.Errorf("query route %s %s must contain :%s", route.Method, route.Path, route.TaskIDParam)
		}
	case RouteTypeDynamic:
		if route.Decode == "" || route.Render == "" || route.TaskIDParam != "" {
			return fmt.Errorf("dynamic route %s %s must declare decode and render and must not declare taskIdParam", route.Method, route.Path)
		}
	default:
		return fmt.Errorf("plugin route %s %s has unsupported type %q", route.Method, route.Path, route.Type)
	}
	if route.Decode != "" && !memberNamePattern.MatchString(route.Decode) {
		return fmt.Errorf("plugin route %s %s has invalid decode %q", route.Method, route.Path, route.Decode)
	}
	if route.Render != "" && !memberNamePattern.MatchString(route.Render) {
		return fmt.Errorf("plugin route %s %s has invalid render %q", route.Method, route.Path, route.Render)
	}
	if strings.TrimSpace(route.Action) != route.Action {
		return fmt.Errorf("plugin route %s %s action must not have surrounding whitespace", route.Method, route.Path)
	}
	if len(route.Models) > 0 {
		if route.Type == RouteTypeQuery {
			return fmt.Errorf("query route %s %s must not declare models", route.Method, route.Path)
		}
		if err := validateModelScope(route.Models, fmt.Sprintf("route %s %s", route.Method, route.Path)); err != nil {
			return err
		}
	}
	return nil
}

// validateModelScope enforces the shared rules for route.models and
// per-protocol models entries: non-empty canonical names, no duplicates.
func validateModelScope(models []string, subject string) error {
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if strings.TrimSpace(model) == "" || strings.TrimSpace(model) != model {
			return fmt.Errorf("plugin %s models must contain non-empty canonical names", subject)
		}
		folded := asciiFold(model)
		if _, duplicate := seen[folded]; duplicate {
			return fmt.Errorf("plugin %s models must be unique case-insensitively", subject)
		}
		seen[folded] = struct{}{}
	}
	return nil
}

func pathHasParameter(routePath, name string) bool {
	return slices.Contains(strings.Split(routePath, "/"), ":"+name)
}

func ResolveRouteAction(route Route, resolvedAction string) string {
	if strings.TrimSpace(resolvedAction) != "" {
		return resolvedAction
	}
	return route.Action
}

func buildRoutingGeneration(factory, override map[string]*LoadedPlugin, number uint64) (*RoutingGeneration, error) {
	effective := effectivePlugins(factory, override)
	return buildRoutingGenerationFromPlugins(effective, number)
}

func buildRoutingGenerationAdmitting(
	factory, override map[string]*LoadedPlugin,
	number uint64,
	current *RoutingGeneration,
	retainCurrent map[string]struct{},
) (*RoutingGeneration, map[string]string, error) {
	candidates := effectivePlugins(factory, override)
	accepted := make(map[string]*LoadedPlugin, len(candidates))
	currentByKey := make(map[string]*LoadedPlugin)
	if current != nil {
		for _, plugin := range current.plugins {
			currentByKey[plugin.Meta.Key] = plugin
		}
	}
	generation, err := buildRoutingGenerationFromPlugins(accepted, number)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize routing generation: %w", err)
	}

	unchangedKeys := make([]string, 0)
	changedKeys := make([]string, 0)
	newKeys := make([]string, 0)
	for key, candidate := range candidates {
		incumbent, exists := currentByKey[key]
		switch {
		case exists && candidate == incumbent:
			unchangedKeys = append(unchangedKeys, key)
		case exists:
			changedKeys = append(changedKeys, key)
		default:
			newKeys = append(newKeys, key)
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
		candidate := candidates[key]
		accepted[key] = candidate
		trial, trialErr := buildRoutingGenerationFromPlugins(accepted, number)
		if trialErr == nil {
			generation = trial
			continue
		}
		delete(accepted, key)
		routingErrors[key] = fmt.Sprintf("plugin %s rejected from routing generation: %v", key, trialErr)
		rejectedKeys = append(rejectedKeys, key)
	}

	for _, key := range rejectedKeys {
		candidate := candidates[key]
		incumbent, hasIncumbent := currentByKey[key]
		_, mayRetain := retainCurrent[key]
		if !hasIncumbent || !mayRetain || incumbent == candidate {
			continue
		}
		accepted[key] = incumbent
		fallback, fallbackErr := buildRoutingGenerationFromPlugins(accepted, number)
		if fallbackErr == nil {
			generation = fallback
			continue
		}
		delete(accepted, key)
	}
	generation.retainCurrent = cloneStringSet(retainCurrent)
	return generation, routingErrors, nil
}

func effectivePlugins(factory, override map[string]*LoadedPlugin) map[string]*LoadedPlugin {
	effective := make(map[string]*LoadedPlugin, len(factory)+len(override))
	maps.Copy(effective, factory)
	maps.Copy(effective, override)
	return effective
}

func buildRoutingGenerationFromPlugins(effective map[string]*LoadedPlugin, number uint64) (*RoutingGeneration, error) {
	keys := make([]string, 0, len(effective))
	for key := range effective {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	generation := &RoutingGeneration{
		Number:               number,
		PublishedAt:          time.Now(),
		byKey:                make(map[string]*LoadedPlugin, len(effective)),
		byModel:              make(map[string]*LoadedPlugin),
		modelPlugins:         make(map[string][]*LoadedPlugin),
		canonicalModelByFold: make(map[string]string),
		byChannelType:        make(map[int]*LoadedPlugin),
		routeIndex:           make(map[string]RouteBinding),
		protocolIndex:        make(map[string][]ProtocolBinding),
		plugins:              make([]*LoadedPlugin, 0, len(effective)),
	}
	for _, key := range keys {
		plugin := effective[key]
		generation.byKey[key] = plugin
		generation.plugins = append(generation.plugins, plugin)
		for _, model := range plugin.Meta.Models {
			generation.modelPlugins[model] = append(generation.modelPlugins[model], plugin)
			if _, exists := generation.byModel[model]; !exists {
				generation.byModel[model] = plugin
			}
			folded := asciiFold(model)
			if existing, exists := generation.canonicalModelByFold[folded]; exists {
				if existing != model {
					otherKey := plugin.Meta.Key
					if other, ok := generation.byModel[existing]; ok {
						otherKey = other.Meta.Key
					}
					return nil, fmt.Errorf("plugin %s model %q conflicts with plugin %s model %q", plugin.Meta.Key, model, otherKey, existing)
				}
				continue
			}
			generation.canonicalModelByFold[folded] = model
		}

		for _, channelType := range plugin.Meta.ChannelTypes {
			if channelType == 0 || channelType == constant.ChannelTypeTaskPlugin {
				continue
			}
			if other, exists := generation.byChannelType[channelType]; exists {
				return nil, fmt.Errorf("plugin %s channelType %d conflicts with plugin %s", plugin.Meta.Key, channelType, other.Meta.Key)
			}
			generation.byChannelType[channelType] = plugin
		}

		for _, route := range plugin.Meta.Routes {
			shape, err := routePathShape(route.Path)
			if err != nil {
				return nil, err
			}
			indexKey := route.Method + " " + shape
			if other, exists := generation.routeIndex[indexKey]; exists {
				return nil, fmt.Errorf("plugin %s route %s %s conflicts with plugin %s route %s", plugin.Meta.Key, route.Method, route.Path, other.Plugin.Meta.Key, other.Route.Path)
			}
			binding := RouteBinding{Plugin: plugin, Route: route}
			generation.routeIndex[indexKey] = binding
			generation.routes = append(generation.routes, binding)
		}

		for _, claim := range plugin.Meta.Protocols {
			definition, _ := HostProtocol(claim.Name)
			boundModels := plugin.Meta.Models
			if len(claim.Models) > 0 {
				boundModels = claim.Models
			}
			for _, operation := range definition.Operations {
				if operation.ModelField == "" {
					continue
				}
				for _, method := range operation.Methods {
					for _, model := range boundModels {
						indexKey := endpointIndexKey(method, operation.Path, model)
						bindings := generation.protocolIndex[indexKey]
						if len(bindings) > 0 {
							other := bindings[0]
							if claim.Name != other.Protocol {
								return nil, fmt.Errorf("plugin %s protocol %s %s model %q conflicts with plugin %s", plugin.Meta.Key, method, operation.Path, model, other.Plugin.Meta.Key)
							}
						}
						generation.protocolIndex[indexKey] = append(bindings, ProtocolBinding{Plugin: plugin, Protocol: claim.Name, Operation: operation, Model: model})
					}
				}
			}
		}
	}
	return generation, nil
}

// PreflightRoutingConflict reports whether admitting candidate into the
// current generation would collide on a channel type, native route, or
// protocol-model binding. A same-key entry is replaced first so re-uploading
// a plugin (or overriding a factory built-in) does not self-conflict.
func PreflightRoutingConflict(current *RoutingGeneration, candidate *LoadedPlugin) error {
	if candidate == nil {
		return fmt.Errorf("cannot preflight a nil plugin")
	}
	effective := make(map[string]*LoadedPlugin)
	number := uint64(0)
	if current != nil {
		for _, plugin := range current.Plugins() {
			effective[plugin.Meta.Key] = plugin
		}
		number = current.Number
	}
	effective[candidate.Meta.Key] = candidate
	_, err := buildRoutingGenerationFromPlugins(effective, number)
	return err
}
