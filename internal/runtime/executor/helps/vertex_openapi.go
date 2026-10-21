package helps

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// IsVertexOpenAPIModel reports whether the given model identifier targets a
// non-Google Vertex partner or open model (MaaS) served by endpoints/openapi.
func IsVertexOpenAPIModel(model string) bool {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if baseModel == "" {
		return false
	}
	if publisher, modelID, ok := cutVertexPublisherResource(baseModel); ok {
		return !strings.EqualFold(publisher, "google") && !strings.Contains(modelID, "/")
	}
	publisher, modelID, ok := strings.Cut(baseModel, "/")
	publisher = strings.TrimSpace(publisher)
	modelID = strings.TrimSpace(modelID)
	if !ok || publisher == "" || modelID == "" || strings.Contains(modelID, "/") {
		return false
	}
	if strings.EqualFold(publisher, "google") || strings.EqualFold(publisher, "models") || strings.EqualFold(publisher, "publishers") {
		return false
	}
	return true
}

// NormalizeVertexOpenAPIModel normalizes a Vertex model identifier into the
// "<publisher>/<model>" form expected by Vertex's endpoints/openapi route while
// preserving any thinking suffix.
func NormalizeVertexOpenAPIModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}
	parsed := thinking.ParseSuffix(trimmed)
	baseModel := strings.TrimSpace(parsed.ModelName)
	if baseModel == "" {
		baseModel = trimmed
	}
	normalizedBase := normalizeVertexOpenAPIBaseModel(baseModel)
	if parsed.HasSuffix {
		return fmt.Sprintf("%s(%s)", normalizedBase, parsed.RawSuffix)
	}
	return normalizedBase
}

func normalizeVertexOpenAPIBaseModel(baseModel string) string {
	if publisher, modelID, ok := cutVertexPublisherResource(baseModel); ok {
		return strings.ToLower(publisher) + "/" + modelID
	}
	lower := strings.ToLower(baseModel)
	if strings.HasPrefix(lower, "models/") {
		modelID := strings.TrimSpace(baseModel[len("models/"):])
		if modelID != "" {
			return "google/" + modelID
		}
	}
	if publisher, modelID, ok := strings.Cut(baseModel, "/"); ok {
		publisher = strings.TrimSpace(publisher)
		modelID = strings.TrimSpace(modelID)
		if publisher != "" && modelID != "" {
			return publisher + "/" + modelID
		}
	}
	return "google/" + baseModel
}

func cutVertexPublisherResource(baseModel string) (publisher, modelID string, ok bool) {
	lower := strings.ToLower(baseModel)
	if !strings.HasPrefix(lower, "publishers/") {
		return "", "", false
	}
	rest := strings.TrimSpace(baseModel[len("publishers/"):])
	pub, tail, found := strings.Cut(rest, "/")
	pub = strings.TrimSpace(pub)
	tail = strings.TrimSpace(tail)
	if !found || pub == "" || tail == "" {
		return "", "", false
	}
	lowerTail := strings.ToLower(tail)
	if !strings.HasPrefix(lowerTail, "models/") {
		return "", "", false
	}
	id := strings.TrimSpace(tail[len("models/"):])
	if id == "" {
		return "", "", false
	}
	return pub, id, true
}

// ShouldExecuteVertexOpenAPI reports whether the request should be routed
// through Vertex AI's OpenAI-compatible endpoints/openapi route.
func ShouldExecuteVertexOpenAPI(auth *cliproxyauth.Auth, model string) bool {
	return ShouldExecuteVertexOpenAPIForRequest(nil, auth, model, cliproxyexecutor.Options{})
}

// ShouldExecuteVertexOpenAPIForRequest resolves model, credential, and automatic
// endpoint selection in that order, preserving alias-specific overrides.
func ShouldExecuteVertexOpenAPIForRequest(cfg *config.Config, auth *cliproxyauth.Auth, model string, opts cliproxyexecutor.Options) bool {
	model = strings.TrimSpace(model)
	baseModel := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if strings.Contains(strings.ToLower(baseModel), "imagen") {
		return false
	}
	requestedModel := PayloadRequestedModel(opts, "")
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(gjson.GetBytes(opts.OriginalRequest, "model").String())
	}
	if requestedModel == "" && len(opts.Metadata) > 0 {
		if data, errMarshal := json.Marshal(opts.Metadata); errMarshal == nil {
			requestedModel = strings.TrimSpace(gjson.GetBytes(data, "model").String())
		}
	}
	requestedModel = stripVertexAuthPrefix(requestedModel, auth)
	entry := ResolveVertexCompatConfig(cfg, auth)
	if entry != nil {
		rules := make([]config.OAuthModelAlias, 0, len(entry.Models))
		for _, configured := range entry.Models {
			rules = append(rules, config.OAuthModelAlias{Name: configured.Name, Alias: configured.Alias, OpenAPI: configured.OpenAPI})
		}
		if setting := matchVertexModelOpenAPI(rules, model, requestedModel); setting != nil {
			return *setting
		}
	}
	isOAuth := isVertexOAuthCredential(auth)
	var oauthAndAuthRules []config.OAuthModelAlias
	if auth != nil {
		for _, key := range []string{"model_aliases", "model-aliases", "models"} {
			if data, errMarshal := json.Marshal(auth.Metadata[key]); errMarshal == nil {
				oauthAndAuthRules = append(oauthAndAuthRules, vertexModelOpenAPIRules(data)...)
			}
		}
		oauthAndAuthRules = append(oauthAndAuthRules, vertexModelOpenAPIRules([]byte(auth.Attributes["vertex_model_openapi"]))...)
		if isOAuth {
			oauthAndAuthRules = append(oauthAndAuthRules, cliproxyauth.OAuthModelAliasesFromAttributes(auth.Attributes)...)
		}
	}
	if cfg != nil && isOAuth {
		oauthAndAuthRules = append(oauthAndAuthRules, cfg.OAuthModelAlias["vertex"]...)
	}
	if setting := matchVertexModelOpenAPI(deduplicateVertexModelOpenAPIRules(oauthAndAuthRules), model, requestedModel); setting != nil {
		return *setting
	}
	if cfg != nil && isOAuth {
		settingRules := make([]config.OAuthModelAlias, 0, len(cfg.OAuthSettings["vertex"]))
		for _, configured := range cfg.OAuthSettings["vertex"] {
			settingRules = append(settingRules, config.OAuthModelAlias{Name: configured.Name, Alias: configured.Alias, OpenAPI: configured.OpenAPI})
		}
		if setting := matchVertexModelOpenAPI(settingRules, model, requestedModel); setting != nil {
			return *setting
		}
	}
	if setting, hasExplicit := vertexOpenAPIExplicitSetting(auth); hasExplicit {
		return setting
	}
	if entry != nil && entry.OpenAPI != nil {
		return *entry.OpenAPI
	}
	return IsVertexOpenAPIModel(model)
}

func stripVertexAuthPrefix(model string, auth *cliproxyauth.Auth) string {
	model = strings.TrimSpace(model)
	if auth == nil || model == "" {
		return model
	}
	prefix := strings.Trim(strings.TrimSpace(auth.Prefix), "/")
	if prefix == "" && auth.Attributes != nil {
		prefix = strings.Trim(strings.TrimSpace(auth.Attributes["prefix"]), "/")
	}
	if prefix == "" {
		return model
	}
	needle := prefix + "/"
	if len(model) > len(needle) && strings.EqualFold(model[:len(needle)], needle) {
		return strings.TrimSpace(model[len(needle):])
	}
	return model
}

func isVertexOAuthCredential(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return true
	}
	if auth.Metadata != nil {
		if sa, ok := auth.Metadata["service_account"].(map[string]any); ok && len(sa) > 0 {
			return true
		}
	}
	return cliproxyauth.OAuthModelAliasChannel("vertex", auth.AuthKind()) != ""
}

func vertexModelOpenAPIRules(data []byte) []config.OAuthModelAlias {
	var rules []config.OAuthModelAlias
	for _, item := range gjson.ParseBytes(data).Array() {
		var rule config.OAuthModelAlias
		if errUnmarshal := json.Unmarshal([]byte(item.Raw), &rule); errUnmarshal == nil && (strings.TrimSpace(rule.Name) != "" || strings.TrimSpace(rule.Alias) != "") {
			rules = append(rules, rule)
		}
	}
	return rules
}

func deduplicateVertexModelOpenAPIRules(rules []config.OAuthModelAlias) []config.OAuthModelAlias {
	if len(rules) == 0 {
		return nil
	}
	out := make([]config.OAuthModelAlias, 0, len(rules))
	seenAlias := make(map[string]struct{}, len(rules))
	seenNameOnly := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		name := strings.TrimSpace(rule.Name)
		alias := strings.TrimSpace(rule.Alias)
		if name == "" && alias == "" {
			continue
		}
		if alias != "" && !strings.EqualFold(name, alias) {
			aliasKey := strings.ToLower(alias)
			if _, exists := seenAlias[aliasKey]; exists {
				continue
			}
			seenAlias[aliasKey] = struct{}{}
		} else {
			if rule.OpenAPI == nil {
				continue
			}
			nameKey := strings.ToLower(name)
			if _, exists := seenNameOnly[nameKey]; exists {
				continue
			}
			seenNameOnly[nameKey] = struct{}{}
		}
		out = append(out, rule)
	}
	return out
}

func matchVertexModelOpenAPI(rules []config.OAuthModelAlias, model, requestedModel string) *bool {
	base := func(value string) string {
		return strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(value)).ModelName)
	}
	exactMatch := func(value, rule string) bool {
		value, rule = strings.TrimSpace(value), strings.TrimSpace(rule)
		return value != "" && rule != "" && strings.EqualFold(value, rule)
	}
	baseMatch := func(value, rule string) bool {
		value, rule = strings.TrimSpace(value), strings.TrimSpace(rule)
		if value == "" || rule == "" || thinking.ParseSuffix(rule).HasSuffix {
			return false
		}
		return strings.EqualFold(base(value), base(rule))
	}
	anyMatch := func(value, rule string) bool {
		return exactMatch(value, rule) || baseMatch(value, rule)
	}
	aliasTarget := requestedModel
	if aliasTarget == "" {
		aliasTarget = model
	}
	aliasConsistentWithModel := func(rule config.OAuthModelAlias) bool {
		if strings.TrimSpace(rule.Name) == "" || strings.TrimSpace(model) == "" || anyMatch(model, rule.Alias) {
			return true
		}
		return anyMatch(model, rule.Name)
	}
	for _, rule := range rules {
		if exactMatch(aliasTarget, rule.Alias) && aliasConsistentWithModel(rule) {
			return rule.OpenAPI
		}
	}
	for _, rule := range rules {
		if baseMatch(aliasTarget, rule.Alias) && aliasConsistentWithModel(rule) {
			return rule.OpenAPI
		}
	}
	canMatchName := func(rule config.OAuthModelAlias) bool {
		return strings.TrimSpace(rule.Alias) == "" || strings.EqualFold(strings.TrimSpace(rule.Alias), strings.TrimSpace(rule.Name)) ||
			requestedModel == "" || anyMatch(requestedModel, rule.Alias) || anyMatch(requestedModel, rule.Name)
	}
	for _, rule := range rules {
		if rule.OpenAPI != nil && (exactMatch(model, rule.Name) || exactMatch(requestedModel, rule.Name)) && canMatchName(rule) {
			return rule.OpenAPI
		}
	}
	for _, rule := range rules {
		if rule.OpenAPI != nil && (baseMatch(model, rule.Name) || baseMatch(requestedModel, rule.Name)) && canMatchName(rule) {
			return rule.OpenAPI
		}
	}
	return nil
}

// ResolveVertexCompatConfig finds the API-key configuration belonging to auth.
func ResolveVertexCompatConfig(cfg *config.Config, auth *cliproxyauth.Auth) *config.VertexCompatKey {
	if cfg == nil || auth == nil {
		return nil
	}
	attrKey := strings.TrimSpace(auth.Attributes["api_key"])
	attrBase := strings.TrimSpace(auth.Attributes["base_url"])
	matches := func(entry config.VertexCompatKey) bool {
		key, base := strings.TrimSpace(entry.APIKey), strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			return strings.EqualFold(key, attrKey) && strings.EqualFold(base, attrBase)
		}
		if attrKey != "" {
			return strings.EqualFold(key, attrKey) && (base == "" || strings.EqualFold(base, attrBase))
		}
		return attrBase != "" && strings.EqualFold(base, attrBase)
	}
	if index, errIndex := strconv.Atoi(auth.Attributes["config_index"]); errIndex == nil && index >= 0 && index < len(cfg.VertexCompatAPIKey) && matches(cfg.VertexCompatAPIKey[index]) {
		return &cfg.VertexCompatAPIKey[index]
	}
	for i := range cfg.VertexCompatAPIKey {
		entry := &cfg.VertexCompatAPIKey[i]
		if matches(*entry) && strings.EqualFold(strings.TrimSpace(entry.Prefix), strings.TrimSpace(auth.Prefix)) && strings.EqualFold(strings.TrimSpace(entry.ProxyURL), strings.TrimSpace(auth.ProxyURL)) {
			return entry
		}
	}
	for i := range cfg.VertexCompatAPIKey {
		if matches(cfg.VertexCompatAPIKey[i]) {
			return &cfg.VertexCompatAPIKey[i]
		}
	}
	if attrKey != "" {
		for i := range cfg.VertexCompatAPIKey {
			if strings.EqualFold(strings.TrimSpace(cfg.VertexCompatAPIKey[i].APIKey), attrKey) {
				return &cfg.VertexCompatAPIKey[i]
			}
		}
	}
	return nil
}

func vertexOpenAPIExplicitSetting(auth *cliproxyauth.Auth) (bool, bool) {
	if auth == nil {
		return false, false
	}
	if auth.Attributes != nil {
		if v, ok := parseVertexOpenAPIBool(auth.Attributes["openapi"]); ok {
			return v, true
		}
	}
	if auth.Metadata != nil {
		if v, ok := parseVertexOpenAPIBool(auth.Metadata["openapi"]); ok {
			return v, true
		}
	}
	return false, false
}

func parseVertexOpenAPIBool(raw any) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(v))
		switch trimmed {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
	}
	return false, false
}

// IsGoogleVertexHost reports whether baseURL is empty or points to Google's
// official aiplatform.googleapis.com domain.
func IsGoogleVertexHost(baseURL string) bool {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return true
	}
	parsed, errParse := url.Parse(trimmed)
	if errParse != nil || parsed.Host == "" {
		lower := strings.ToLower(trimmed)
		return strings.Contains(lower, "aiplatform.googleapis.com")
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "aiplatform.googleapis.com" || strings.HasSuffix(host, "-aiplatform.googleapis.com") || strings.HasSuffix(host, ".aiplatform.googleapis.com")
}

// VertexExplicitOpenAPILocation returns an explicitly configured OpenAPI location
// from auth attributes/metadata or the matching VertexCompatKey config, or "" if unset.
func VertexExplicitOpenAPILocation(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if auth.Attributes != nil {
			for _, key := range []string{"openapi_location", "openapi-location"} {
				if v := strings.TrimSpace(auth.Attributes[key]); v != "" {
					return v
				}
			}
		}
		if auth.Metadata != nil {
			for _, key := range []string{"openapi_location", "openapi-location"} {
				if v, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	if entry := ResolveVertexCompatConfig(cfg, auth); entry != nil && strings.TrimSpace(entry.OpenAPILocation) != "" {
		return strings.TrimSpace(entry.OpenAPILocation)
	}
	return ""
}

// VertexOpenAPILocation returns the target Vertex location for OpenAPI requests,
// preferring an explicit openapi_location override on the credential.
func VertexOpenAPILocation(auth *cliproxyauth.Auth, defaultLocation string) string {
	if explicit := VertexExplicitOpenAPILocation(nil, auth); explicit != "" {
		return explicit
	}
	if loc := strings.TrimSpace(defaultLocation); loc != "" {
		return loc
	}
	if auth != nil {
		if auth.Attributes != nil {
			if v := strings.TrimSpace(auth.Attributes["location"]); v != "" {
				return v
			}
		}
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["location"].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return "us-central1"
}

// VertexAuthBaseURL extracts an optional custom base_url from a Vertex credential.
func VertexAuthBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return v
		}
	}
	if auth.Metadata != nil {
		for _, key := range []string{"base_url", "base-url"} {
			if v, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// VertexAuthProjectID extracts an optional project_id from a Vertex credential.
func VertexAuthProjectID(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	for _, key := range []string{"project_id", "project"} {
		if v, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// VertexOpenAPIHostBaseURL returns the Vertex regional or global host URL.
func VertexOpenAPIHostBaseURL(location string) string {
	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = "us-central1"
	} else if strings.EqualFold(loc, "global") {
		return "https://aiplatform.googleapis.com"
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com", loc)
}

// VertexServiceAccountOpenAPIBaseURL builds the OpenAI-compatible base URL for a
// Vertex service-account credential (.../v1/projects/{project}/locations/{location}/endpoints/openapi).
func VertexServiceAccountOpenAPIBaseURL(baseURL, version, projectID, location string) string {
	explicitLoc := strings.TrimSpace(location)
	loc := explicitLoc
	if loc == "" {
		loc = "us-central1"
	}
	ver := strings.TrimSpace(version)
	if ver == "" {
		ver = "v1"
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = VertexOpenAPIHostBaseURL(loc)
	}
	if strings.HasSuffix(base, "/endpoints/openapi") {
		return rewriteVertexOpenAPILocation(strings.TrimSuffix(base, "/endpoints/openapi"), explicitLoc) + "/endpoints/openapi"
	}
	if idx := strings.Index(base, "/publishers/"); idx >= 0 {
		return rewriteVertexOpenAPILocation(base[:idx], explicitLoc) + "/endpoints/openapi"
	}
	if strings.Contains(base, "/projects/") && strings.Contains(base, "/locations/") {
		return rewriteVertexOpenAPILocation(base, explicitLoc) + "/endpoints/openapi"
	}
	if !vertexOpenAPIHasVersionSegment(base) {
		base = fmt.Sprintf("%s/%s", base, ver)
	}
	built := fmt.Sprintf("%s/projects/%s/locations/%s/endpoints/openapi", base, strings.TrimSpace(projectID), loc)
	return rewriteVertexOpenAPILocation(built, explicitLoc)
}

// VertexAPIKeyOpenAPIBaseURL builds the OpenAI-compatible base URL for a third-party
// Vertex-compatible API-key credential.
func VertexAPIKeyOpenAPIBaseURL(baseURL, version, projectID, location string) string {
	ver := strings.TrimSpace(version)
	if ver == "" {
		ver = "v1"
	}
	loc := strings.TrimSpace(location)
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/endpoints/openapi") {
		return rewriteVertexOpenAPILocation(strings.TrimSuffix(base, "/endpoints/openapi"), loc) + "/endpoints/openapi"
	}
	if idx := strings.Index(base, "/publishers/"); idx >= 0 {
		return rewriteVertexOpenAPILocation(base[:idx], loc) + "/endpoints/openapi"
	}
	if strings.Contains(base, "/projects/") && strings.Contains(base, "/locations/") {
		return rewriteVertexOpenAPILocation(base, loc) + "/endpoints/openapi"
	}
	if strings.TrimSpace(projectID) != "" {
		return VertexServiceAccountOpenAPIBaseURL(base, ver, projectID, loc)
	}
	if !vertexOpenAPIHasVersionSegment(base) {
		base = fmt.Sprintf("%s/%s", base, ver)
	}
	return rewriteVertexOpenAPILocation(base, loc) + "/endpoints/openapi"
}

func rewriteVertexOpenAPILocation(base, location string) string {
	loc := strings.TrimSpace(location)
	if loc == "" {
		return base
	}
	rewritten := base
	const marker = "/locations/"
	if idx := strings.Index(rewritten, marker); idx >= 0 {
		segStart := idx + len(marker)
		segEnd := len(rewritten)
		if slashIdx := strings.IndexByte(rewritten[segStart:], '/'); slashIdx >= 0 {
			segEnd = segStart + slashIdx
		}
		rewritten = rewritten[:segStart] + loc + rewritten[segEnd:]
	}
	if parsed, errParse := url.Parse(rewritten); errParse == nil && IsGoogleVertexHost(rewritten) && parsed.Host != "" {
		targetHostURL, errHost := url.Parse(VertexOpenAPIHostBaseURL(loc))
		if errHost == nil && targetHostURL.Host != "" {
			parsed.Scheme = targetHostURL.Scheme
			parsed.Host = targetHostURL.Host
			return parsed.String()
		}
	}
	return rewritten
}

func vertexOpenAPIHasVersionSegment(base string) bool {
	if base == "" {
		return false
	}
	if strings.Contains(base, "/v1/") || strings.Contains(base, "/v1beta1/") {
		return true
	}
	seg := base[strings.LastIndexByte(base, '/')+1:]
	if len(seg) < 2 || seg[0] != 'v' {
		return false
	}
	hasDigit := false
	for i := 1; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case c == '_' || c == '-' || (c >= 'a' && c <= 'z'):
		default:
			return false
		}
	}
	return hasDigit
}
