package oidc

import (
	"encoding/json"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func ResolveModels(cfg *config.Config, auth *coreauth.Auth) []config.OIDCModel {
	if auth == nil {
		return nil
	}
	if auth.Metadata != nil {
		if raw, ok := auth.Metadata["models"]; ok && raw != nil {
			if rendered, err := json.Marshal(raw); err == nil {
				var models []config.OIDCModel
				if err = json.Unmarshal(rendered, &models); err == nil && len(models) > 0 {
					return models
				}
			}
		}
	}
	if name, ok := auth.Metadata["oidc_name"]; ok && name != "" {
		config, err := SelectOIDCConfig(cfg, name.(string))
		if err == nil {
			return config.Models
		}
	}

	return []config.OIDCModel{}
}
