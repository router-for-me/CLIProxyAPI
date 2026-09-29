package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetRequestPolicy(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"options": model.CurrentRequestPolicy().Options}})
}

func UpdateRequestPolicy(c *gin.Context) {
	var request struct {
		Options map[string]string `json:"options"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := model.UpdateRequestPolicyOptions(request.Options); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	GetRequestPolicy(c)
}
