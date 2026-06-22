package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
)

func (s *Service) syncHomePlugins(ctx context.Context, cfg *config.Config) error {
	if s == nil || cfg == nil || !cfg.Home.Enabled || !cfg.Plugins.Enabled {
		return nil
	}
	return homeplugins.Sync(ctx, cfg, s.pluginHost)
}
