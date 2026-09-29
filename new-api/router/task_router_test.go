/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gin panics at registration time when routes sharing a path position use
// different wildcard names, which unit tests that build their own routers
// never catch. Registering against a real engine is the only guard.
func TestSetTaskRouterRegistersWithoutConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	require.NotPanics(t, func() { SetTaskRouter(engine) })

	routes := engine.Routes()
	require.Len(t, routes, 5)
	actual := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		actual[route.Method+" "+route.Path] = struct{}{}
	}
	assert.Contains(t, actual, http.MethodPost+" /v1/tasks/:key")
	assert.Contains(t, actual, http.MethodGet+" /v1/tasks/:key")
	assert.Contains(t, actual, http.MethodGet+" /v1/tasks/:key/artifacts")
	assert.Contains(t, actual, http.MethodGet+" /v1/tasks/:key/artifacts/:artifact_key/content")
	assert.Contains(t, actual, http.MethodHead+" /v1/tasks/:key/artifacts/:artifact_key/content")
	for route := range actual {
		assert.NotContains(t, route, "/native/")
	}
}
