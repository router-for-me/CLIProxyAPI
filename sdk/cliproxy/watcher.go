package cliproxy

import (
	"context"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/watcher"
	coreauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/kooshapari/CLIProxyAPI/v7/sdk/config"
)

func defaultWatcherFactory(configPath, authDir string, reload func(*config.Config)) (*WatcherWrapper, error) {
	w, err := watcher.NewWatcher(configPath, authDir, reload)
	if err != nil {
		return nil, err
	}

	return &WatcherWrapper{
		start: func(ctx context.Context) error {
			return w.Start(ctx)
		},
		stop: func() error {
			return w.Stop()
		},
		setConfig: func(cfg *config.Config) {
			w.SetConfig(cfg)
		},
		snapshotAuths: func() []*coreauth.Auth { return w.SnapshotCoreAuths() },
		setUpdateQueue: func(queue chan<- watcher.AuthUpdate) {
			w.SetAuthUpdateQueue(queue)
		},
		dispatchRuntimeUpdate: func(update watcher.AuthUpdate) bool {
			return w.DispatchRuntimeAuthUpdate(update)
		},
		dispatchPersistedAuth: func(update watcher.AuthUpdate) bool {
			return w.DispatchPersistedAuthUpdate(update)
		},
		setPluginAuthParser: func(parser PluginAuthParser) {
			w.SetPluginAuthParser(parser)
		},
		reloadConfigIfChanged: func() {
			w.ReloadConfigIfChanged()
		},
	}, nil
}
