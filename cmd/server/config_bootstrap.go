package main

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

type configPersister interface {
	PersistConfig(context.Context) error
}

func bootstrapGitBackedConfig(ctx context.Context, examplePath, configFilePath string, persister configPersister) error {
	if err := misc.CopyConfigTemplate(examplePath, configFilePath); err != nil {
		return fmt.Errorf("copy config template: %w", err)
	}
	if err := persister.PersistConfig(ctx); err != nil {
		return fmt.Errorf("persist config: %w", err)
	}
	return nil
}
