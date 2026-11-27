package api

import (
	proxengine "github.com/router-for-me/CLIProxyAPI/v8/internal/prox/engine"
)

// registerProxRoutes mounts the prox JSON API. Every call requires the
// management key, like the rest of the management surface. The dashboard is
// the embedded panel served at /management.html.
func (s *Server) registerProxRoutes() {
	api := s.engine.Group(proxengine.APIBase)
	api.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware())
	api.Any("/*path", proxengine.GinHandler)
}
