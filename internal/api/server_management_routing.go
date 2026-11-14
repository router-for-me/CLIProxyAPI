package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
)

// RoutingPublisher is the runtime authority behind the residual management API.
// Its receipt describes a generation already available to inference.
type RoutingPublisher interface {
	RoutingState() modelrouting.ActiveStateV2
	RoutingInventory() (modelrouting.Inventory, error)
	PublishRouting(context.Context, []byte, *modelrouting.ActiveIdentityV2) (modelrouting.ActivationReceiptV2, error)
}

func WithRoutingPublisher(p RoutingPublisher) ServerOption {
	return func(options *serverOptionConfig) { options.routingPublisher = p }
}

func (s *Server) registerRoutingManagement(p RoutingPublisher) {
	routes := s.engine.Group("/v8dc/management/routing", s.managementAvailabilityMiddleware(), s.mgmt.Middleware())
	routes.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store") })
	routes.GET("/model-inventory", func(c *gin.Context) {
		inventory, err := p.RoutingInventory()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "inventory_unavailable"})
			return
		}
		c.JSON(http.StatusOK, inventory)
	})
	routes.GET("/config.yaml", func(c *gin.Context) {
		state := p.RoutingState()
		if state.Identity == nil && state.Projection == nil && state.LoadedAt == nil {
			c.Data(http.StatusOK, "application/yaml", []byte("model-routing: null\n"))
			return
		}
		if state.Identity == nil || state.Projection == nil || state.LoadedAt == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "inconsistent_routing_state"})
			return
		}
		etag, etagErr := modelrouting.ActiveETag(*state.Identity)
		body, bodyErr := modelrouting.RoutingYAML(state.Projection)
		if etagErr != nil || bodyErr != nil || modelrouting.ConfigDigest(body) != state.Identity.ConfigDigest {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_routing_state"})
			return
		}
		c.Header("ETag", etag)
		c.Data(http.StatusOK, "application/yaml", body)
	})
	routes.PUT("/config.yaml", func(c *gin.Context) {
		match, none := c.Request.Header.Values("If-Match"), c.Request.Header.Values("If-None-Match")
		var expected *modelrouting.ActiveIdentityV2
		var err error
		switch {
		case len(match) == 0 && len(none) == 1 && none[0] == "*":
		case len(match) == 1 && len(none) == 0:
			expected, err = modelrouting.ParseActiveETag(match[0])
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_precondition"})
				return
			}
		default:
			c.JSON(http.StatusPreconditionRequired, gin.H{"error": "precondition_required"})
			return
		}
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_yaml"})
			return
		}
		var receipt modelrouting.ActivationReceiptV2
		s.mgmt.SerializeRoutingPublication(func() {
			receipt, err = p.PublishRouting(c.Request.Context(), body, expected)
		})
		switch {
		case errors.Is(err, modelrouting.ErrCASConflict):
			c.JSON(http.StatusPreconditionFailed, gin.H{"error": "cas_conflict"})
			return
		case errors.Is(err, modelrouting.ErrInvalidPublication):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_publication"})
			return
		case err != nil:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "publication_failed"})
			return
		}
		etag, err := modelrouting.ActiveETag(receipt.Active)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "receipt_failed"})
			return
		}
		c.Header("ETag", etag)
		c.JSON(http.StatusOK, receipt)
	})
}
