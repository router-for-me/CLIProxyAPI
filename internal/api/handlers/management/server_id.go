package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ServerIDHeader identifies the server on authenticated management responses.
const ServerIDHeader = "X-CPA-SERVER-ID"

const managementIdentityKey = "management.identity"

type managementIdentity struct {
	id        string
	setHeader func(string)
}

func managementRequestIdentity(c *gin.Context) managementIdentity {
	value, _ := c.Get(managementIdentityKey)
	identity, _ := value.(managementIdentity)
	return identity
}

// useConfigServerID selects the identity of the exact response or committed
// snapshot. Only Middleware can install the authenticated header writer.
func useConfigServerID(c *gin.Context, id string) {
	if setHeader := managementRequestIdentity(c).setHeader; setHeader != nil {
		setHeader(id)
	}
}

func (h *Handler) GetServerID(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"server-id": managementRequestIdentity(c).id})
}
