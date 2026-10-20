package helps

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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
	if setting, hasExplicit := vertexOpenAPIExplicitSetting(auth); hasExplicit {
		if !setting {
			return false
		}
		lowerModel := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
		if strings.Contains(lowerModel, "imagen") {
			return false
		}
		return true
	}
	return IsVertexOpenAPIModel(model)
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

// VertexOpenAPILocation returns the target Vertex location for OpenAPI requests,
// preferring an explicit openapi_location override on the credential.
func VertexOpenAPILocation(auth *cliproxyauth.Auth, defaultLocation string) string {
	if auth != nil {
		if auth.Attributes != nil {
			for _, key := range []string{"openapi_location", "openapi-location", "location"} {
				if v := strings.TrimSpace(auth.Attributes[key]); v != "" {
					if key != "location" || strings.TrimSpace(defaultLocation) == "" {
						return v
					}
				}
			}
		}
		if auth.Metadata != nil {
			for _, key := range []string{"openapi_location", "openapi-location"} {
				if v, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
			if strings.TrimSpace(defaultLocation) == "" {
				if v, ok := auth.Metadata["location"].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	loc := strings.TrimSpace(defaultLocation)
	if loc == "" {
		return "us-central1"
	}
	return loc
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
	loc := strings.TrimSpace(location)
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
		return base
	}
	if idx := strings.Index(base, "/publishers/"); idx >= 0 {
		return base[:idx] + "/endpoints/openapi"
	}
	if strings.Contains(base, "/projects/") && strings.Contains(base, "/locations/") {
		return base + "/endpoints/openapi"
	}
	if !vertexOpenAPIHasVersionSegment(base) {
		base = fmt.Sprintf("%s/%s", base, ver)
	}
	return fmt.Sprintf("%s/projects/%s/locations/%s/endpoints/openapi", base, strings.TrimSpace(projectID), loc)
}

// VertexAPIKeyOpenAPIBaseURL builds the OpenAI-compatible base URL for a third-party
// Vertex-compatible API-key credential.
func VertexAPIKeyOpenAPIBaseURL(baseURL, version, projectID, location string) string {
	ver := strings.TrimSpace(version)
	if ver == "" {
		ver = "v1"
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/endpoints/openapi") {
		return base
	}
	if idx := strings.Index(base, "/publishers/"); idx >= 0 {
		return base[:idx] + "/endpoints/openapi"
	}
	if strings.Contains(base, "/projects/") && strings.Contains(base, "/locations/") {
		return base + "/endpoints/openapi"
	}
	if strings.TrimSpace(projectID) != "" {
		return VertexServiceAccountOpenAPIBaseURL(base, ver, projectID, location)
	}
	if !vertexOpenAPIHasVersionSegment(base) {
		base = fmt.Sprintf("%s/%s", base, ver)
	}
	return base + "/endpoints/openapi"
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
