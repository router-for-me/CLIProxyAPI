package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// pluginRoutesGroup covers routes that plugins register under /v0/management.
const pluginRoutesGroup = "plugin-routes"

// GuestRouteGroup is a set of Management API v8 routes a guest permission is
// set for. view allows GET; edit allows every method. Edit implies view.
type GuestRouteGroup struct {
	ID string `json:"id"`
	// Paths are prefixes below /v8/management. The longest matching prefix wins,
	// so a group can carve a sub-route out of another group's prefix.
	Paths []string `json:"paths"`
	// EditOnGet marks groups whose GET routes change state, so they need edit.
	EditOnGet bool `json:"edit-on-get,omitempty"`
}

// guestRouteGroups lists every route a guest key can be granted. A route that
// is not listed here is management-key only. /config.yaml, /session and
// /guests are deliberately absent.
var guestRouteGroups = []GuestRouteGroup{
	{ID: "config", Paths: []string{"/config"}},
	{ID: "config-plugins", Paths: []string{"/config/plugins"}},
	{ID: "config-logging", Paths: []string{"/config/observability/logs"}},
	{ID: "logs", Paths: []string{"/observability/logs"}},
	{ID: "usage", Paths: []string{"/observability/usage"}},
	{ID: "credentials", Paths: []string{"/credentials"}},
	{ID: "credentials-download", Paths: []string{"/credentials/download"}},
	{ID: "cooldown", Paths: []string{"/routing/cooldown/reset"}},
	{ID: "model-definitions", Paths: []string{"/routing/model-definitions", "/models/refresh"}},
	{ID: "oauth", Paths: []string{"/oauth"}, EditOnGet: true},
	{ID: "oauth-status", Paths: []string{"/oauth/status", "/oauth/siwc/status"}},
	{ID: "api-call", Paths: []string{"/requests/api-call"}},
	{ID: "plugins", Paths: []string{"/plugins"}},
	{ID: "plugins-store", Paths: []string{"/plugins/store"}},
	{ID: pluginRoutesGroup, Paths: []string{}},
}

func validGuestRouteGroup(id string) bool {
	for _, group := range guestRouteGroups {
		if group.ID == id {
			return true
		}
	}
	return false
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func neededLevel(group *GuestRouteGroup, method string) GuestLevel {
	if isReadMethod(method) && !(group != nil && group.EditOnGet) {
		return GuestLevelView
	}
	return GuestLevelEdit
}

// matchGuestRoute returns the group owning rel, a path below /v8/management.
func matchGuestRoute(rel string) *GuestRouteGroup {
	rel = "/" + strings.Trim(rel, "/")
	var (
		best    *GuestRouteGroup
		bestLen = -1
	)
	for i := range guestRouteGroups {
		group := &guestRouteGroups[i]
		for _, prefix := range group.Paths {
			if rel != prefix && !strings.HasPrefix(rel, prefix+"/") {
				continue
			}
			if len(prefix) > bestLen {
				best, bestLen = group, len(prefix)
			}
		}
	}
	return best
}

// guestAllowed reports whether g may call method on rel, a v8 path below
// /v8/management.
func guestAllowed(g *guestRecord, method, rel string) bool {
	if rel = "/" + strings.Trim(rel, "/"); rel == "/server/latest-version" {
		return isReadMethod(method)
	}
	group := matchGuestRoute(rel)
	if group == nil {
		return false
	}
	return g.level(group.ID).rank() >= neededLevel(group, method).rank()
}

// authorizeGuest applies the guest route permissions to a request.
func authorizeGuest(c *gin.Context, g *guestRecord) bool {
	path := c.Request.URL.Path
	method := c.Request.Method
	if strings.HasPrefix(path, managementV8Prefix+"/") || path == managementV8Prefix {
		rel := strings.TrimPrefix(path, managementV8Prefix)
		if rel == "/session" && method == http.MethodGet {
			return true
		}
		return guestAllowed(g, method, rel)
	}
	if c.GetBool(PluginRouteContextKey) && strings.HasPrefix(path, managementV0Prefix+"/") {
		need := neededLevel(nil, method)
		if _, isAuthURL := pluginAuthProviderFromURL(c.Request.URL); isAuthURL {
			// Starting a login registers an OAuth session, so a GET here is a write.
			need = GuestLevelEdit
		}
		return g.level(pluginRoutesGroup).rank() >= need.rank()
	}
	return false
}
