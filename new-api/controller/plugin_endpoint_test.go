package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRelayTaskPluginEndpointPreservesUnclaimedFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	fallbackCalls := 0

	RelayTaskPluginEndpoint(c, func(c *gin.Context) {
		fallbackCalls++
		c.Status(http.StatusNoContent)
		c.Writer.WriteHeaderNow()
	})

	assert.Equal(t, 1, fallbackCalls)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestRelayTaskPluginEndpointNeverEntersOrdinaryRelayWhenClaimed(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set(jsplugin.ContextKeyPinnedEndpoint, jsplugin.PinnedEndpoint{
		Generation: &jsplugin.RoutingGeneration{},
		Plugin:     &jsplugin.LoadedPlugin{},
		Protocol:   "openai_responses",
		Operation:  jsplugin.HostProtocolOperation{Name: "create"},
	})
	fallbackCalls := 0

	RelayTaskPluginEndpoint(c, func(c *gin.Context) {
		fallbackCalls++
		c.Status(http.StatusNoContent)
		c.Writer.WriteHeaderNow()
	})

	assert.Zero(t, fallbackCalls)
	assert.NotEqual(t, http.StatusNoContent, recorder.Code)
}
