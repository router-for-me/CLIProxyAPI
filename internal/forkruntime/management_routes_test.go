package forkruntime

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestRegisterManagementRoutesRegistersUsageAndQueueRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := management.NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)

	RegisterManagementRoutes(router.Group("/v0/management"), handler)

	expectedRoutes := map[string]bool{
		http.MethodGet + " /v0/management/usage":              false,
		http.MethodDelete + " /v0/management/usage":           false,
		http.MethodGet + " /v0/management/usage-queue":        false,
		http.MethodGet + " /v0/management/auth-refresh-queue": false,
	}

	registeredRoutes := router.Routes()
	if len(registeredRoutes) != len(expectedRoutes) {
		t.Fatalf("registered route count = %d, want %d: %#v", len(registeredRoutes), len(expectedRoutes), registeredRoutes)
	}

	for _, route := range registeredRoutes {
		key := route.Method + " " + route.Path
		if _, ok := expectedRoutes[key]; !ok {
			t.Fatalf("unexpected route registered: %s", key)
		}
		expectedRoutes[key] = true
	}

	for route, found := range expectedRoutes {
		if !found {
			t.Fatalf("expected route was not registered: %s", route)
		}
	}
}

func TestRegisterManagementRoutesNilInputsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var nilGroup gin.IRoutes
	handler := management.NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)

	RegisterManagementRoutes(nilGroup, handler)

	router := gin.New()
	group := router.Group("/v0/management")

	RegisterManagementRoutes(group, nil)

	if routes := router.Routes(); len(routes) != 0 {
		t.Fatalf("nil handler registered routes: %#v", routes)
	}
}
