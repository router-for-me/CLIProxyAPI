package oidc

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func SelectOIDCConfig(cfg *config.Config, initName string) (*config.OIDCConfig, error) {
	if cfg == nil || len(cfg.OIDC) == 0 {
		return nil, nil
	}
	if initName != "" {
		for i := range cfg.OIDC {
			candidate := &cfg.OIDC[i]
			if strings.EqualFold(strings.TrimSpace(candidate.Name), initName) {
				return candidate, nil
			}
		}
		return nil, fmt.Errorf("oidc config %q not found", initName)
	}
	if len(cfg.OIDC) == 1 {
		return &cfg.OIDC[0], nil
	}
	return nil, fmt.Errorf("multiple oidc configs found, please specify -oidc-init <name>")
}
