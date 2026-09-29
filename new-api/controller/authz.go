package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/service/authz"

	"github.com/gin-gonic/gin"
)

// GetPermissionCatalog returns the permission schema used by the client to
// render the permission editor: the registry of resources with their actions
// and display label keys, plus the roles with their baseline grant matrices.
// Defining it in the authz package keeps the schema in a single place.
func GetPermissionCatalog(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"resources": authz.Catalog(),
			"roles":     authz.Roles(),
		},
	})
}
