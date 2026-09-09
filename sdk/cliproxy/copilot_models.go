package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (s *Service) fetchCopilotModels(ctx context.Context, auth *coreauth.Auth) []*ModelInfo {
	models, err := executor.NewCopilotExecutor(s.cfg).Models(ctx, auth)
	if err != nil {
		log.WithError(err).WithField("provider", "github-copilot").Warn("could not refresh account models")
		return GlobalModelRegistry().GetModelsForClient(auth.ID)
	}
	return models
}
