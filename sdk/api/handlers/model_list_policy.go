package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// WriteModelList applies an optional authenticated catalog policy before writing
// a successful model-list response. Execution authorization remains independent.
func WriteModelList(c *gin.Context, payload any) {
	metadata, _ := c.Get("accessMetadata")
	attributes, _ := metadata.(map[string]string)
	rawPolicy, restricted := attributes[pluginapi.ModelListAllowedIDsMetadataKey]
	if !restricted {
		c.JSON(http.StatusOK, payload)
		return
	}
	// Catalogs vary by authenticated identity and must not enter shared caches.
	c.Header("Cache-Control", "private, no-store")
	filtered, errFilter := filterModelList(payload, rawPolicy)
	if errFilter != nil {
		// Do not disclose policy contents, model identifiers, or credentials.
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "server_error", "message": "Invalid authenticated model-list policy or catalog",
		}})
		return
	}
	c.JSON(http.StatusOK, filtered)
}

func filterModelList(payload any, rawPolicy string) (map[string]json.RawMessage, error) {
	var ids []string
	if errDecode := json.Unmarshal([]byte(rawPolicy), &ids); errDecode != nil || ids == nil {
		return nil, fmt.Errorf("policy must be a JSON string array")
	}
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, fmt.Errorf("policy contains an invalid model ID")
		}
		allowed[id] = struct{}{}
	}
	raw, errEncode := json.Marshal(payload)
	if errEncode != nil {
		return nil, errEncode
	}
	var catalog map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &catalog); errDecode != nil {
		return nil, errDecode
	}
	field := "data"
	if _, exists := catalog[field]; !exists {
		field = "models"
	}
	var rows []json.RawMessage
	if rawRows, exists := catalog[field]; !exists {
		return nil, fmt.Errorf("missing model list")
	} else if errDecode := json.Unmarshal(rawRows, &rows); errDecode != nil {
		return nil, errDecode
	}
	kept := make([]json.RawMessage, 0, len(rows))
	keptIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		var model struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		}
		if errDecode := json.Unmarshal(row, &model); errDecode != nil {
			return nil, errDecode
		}
		id := model.ID
		if id == "" {
			id = model.Slug
		}
		if id == "" {
			id = model.Name
		}
		_, match := allowed[id]
		// Gemini resource names use models/<id>, whereas calls commonly use <id>.
		if !match && model.ID == "" && model.Slug == "" && strings.HasPrefix(model.Name, "models/") {
			_, match = allowed[strings.TrimPrefix(model.Name, "models/")]
		}
		if id != "" && match {
			kept = append(kept, row)
			keptIDs = append(keptIDs, id)
		}
	}
	catalog[field], errEncode = json.Marshal(kept)
	if errEncode != nil {
		return nil, errEncode
	}
	// Claude catalogs carry boundary IDs; never leave a removed ID in them.
	for _, boundary := range []string{"first_id", "last_id"} {
		if _, exists := catalog[boundary]; !exists {
			continue
		}
		var value any
		if len(keptIDs) > 0 {
			value = keptIDs[0]
			if boundary == "last_id" {
				value = keptIDs[len(keptIDs)-1]
			}
		}
		catalog[boundary], _ = json.Marshal(value)
	}
	return catalog, nil
}
