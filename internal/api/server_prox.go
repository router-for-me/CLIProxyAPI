package api

import (
	proxengine "github.com/router-for-me/CLIProxyAPI/v8/internal/prox/engine"
)

// registerProxRoutes mounts the prox dashboard and its JSON API. The static
// dashboard is public (it holds no data); every API call requires the
// management key, like the rest of the management surface.
func (s *Server) registerProxRoutes() {
	s.engine.GET(proxengine.UIBase+"/*path", s.managementAvailabilityMiddleware(), proxengine.GinHandler)
	api := s.engine.Group(proxengine.APIBase)
	api.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware())
	api.Any("/*path", proxengine.GinHandler)
}
