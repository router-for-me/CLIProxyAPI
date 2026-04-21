package mcp

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

func (m *MCPModule) registerRoutes(engine *gin.Engine, auth gin.HandlerFunc) {
	middlewares := make([]gin.HandlerFunc, 0, 2)
	if auth != nil {
		middlewares = append(middlewares, auth)
	}
	middlewares = append(middlewares, m.proxyHandler())

	engine.Any("/mcp", middlewares...)
	engine.Any("/mcp/*path", middlewares...)
}

func (m *MCPModule) proxyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					return
				}
				panic(rec)
			}
		}()

		proxy := m.getProxy()
		if proxy == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "mcp upstream proxy not available"})
			return
		}

		proxy.ServeHTTP(c.Writer, c.Request)
	}
}
