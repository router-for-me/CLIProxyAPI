package api

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/decisions"
)

// registerProxxDecisionRoutes adds the decision-model passthrough routes (ADR 0008) to the
// client API group, behind the same client-key auth as the other /v1 routes.
func (s *Server) registerProxxDecisionRoutes(v1 *gin.RouterGroup) {
	for _, vendor := range []*decisions.Vendor{decisions.TypeSafe, decisions.OpenAI} {
		v1.POST(vendor.Route[len("/v1"):], decisions.NewHandler(vendor, s.getConfig))
	}
}
