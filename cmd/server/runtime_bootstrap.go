package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	configaccess "github.com/router-for-me/CLIProxyAPI/v6/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cmd"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/managementasset"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/tui"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type startupState struct {
	cfg            *config.Config
	configFilePath string
	configReady    bool
	cloudDeploy    bool
	loginOptions   *cmd.LoginOptions
}

type ioState struct {
	stdout    *os.File
	stderr    *os.File
	logOutput io.Writer
	devNull   *os.File
}

func prepareStartup(flags runtimeFlags) (startupState, error) {
	ctx, err := resolveStartupContext()
	if err != nil {
		return startupState{}, err
	}
	loadDotEnvFile(ctx.workdir)

	result, err := loadConfigResult(flags, ctx, loadStoreSettings(ctx))
	if err != nil {
		return startupState{}, err
	}

	cfg := result.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	if err := configureRuntimeConfig(cfg); err != nil {
		return startupState{}, err
	}

	managementasset.SetCurrentConfig(cfg)
	sdkAuth.RegisterTokenStore(resolveTokenStore(result.tokenStore))
	configaccess.Register(&cfg.SDKConfig)

	return startupState{
		cfg:            cfg,
		configFilePath: result.configFilePath,
		configReady:    detectConfigReady(ctx.cloudDeploy, result.configFilePath, cfg),
		cloudDeploy:    ctx.cloudDeploy,
		loginOptions: &cmd.LoginOptions{
			NoBrowser:    flags.noBrowser,
			CallbackPort: flags.oauthCallbackPort,
		},
	}, nil
}

func configureRuntimeConfig(cfg *config.Config) error {
	usage.SetStatisticsEnabled(cfg.UsageStatisticsEnabled)
	usage.SetDetailRetentionLimit(cfg.UsageDetailRetentionLimit)
	coreauth.SetQuotaCooldownDisabled(cfg.DisableCooling)

	if err := logging.ConfigureLogOutput(cfg); err != nil {
		return fmt.Errorf("failed to configure log output: %w", err)
	}
	log.Infof(
		"CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s",
		buildinfo.Version,
		buildinfo.Commit,
		buildinfo.BuildDate,
	)

	util.SetLogLevel(cfg)
	resolvedAuthDir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err != nil {
		return fmt.Errorf("failed to resolve auth directory: %w", err)
	}
	cfg.AuthDir = resolvedAuthDir
	return nil
}

func resolveTokenStore(store coreauth.Store) coreauth.Store {
	if store != nil {
		return store
	}
	return sdkAuth.NewFileTokenStore()
}

func detectConfigReady(cloudDeploy bool, configFilePath string, cfg *config.Config) bool {
	if !cloudDeploy {
		return true
	}
	info, err := os.Stat(configFilePath)
	if err != nil {
		log.Info("Cloud deploy mode: No configuration file detected; standing by for configuration")
		return false
	}
	if info.IsDir() {
		log.Info("Cloud deploy mode: Config path is a directory; standing by for configuration")
		return false
	}
	if cfg.Port == 0 {
		log.Info("Cloud deploy mode: Configuration file is empty or invalid; standing by for valid configuration")
		return false
	}
	log.Info("Cloud deploy mode: Configuration file detected; starting service")
	return true
}

func dispatchCommand(flags runtimeFlags, state startupState) {
	switch {
	case flags.vertexImport != "":
		cmd.DoVertexImport(state.cfg, flags.vertexImport, flags.vertexImportPrefix)
	case flags.login:
		cmd.DoLogin(state.cfg, flags.projectID, state.loginOptions)
	case flags.antigravityLogin:
		cmd.DoAntigravityLogin(state.cfg, state.loginOptions)
	case flags.codexLogin:
		cmd.DoCodexLogin(state.cfg, state.loginOptions)
	case flags.codexDeviceLogin:
		cmd.DoCodexDeviceLogin(state.cfg, state.loginOptions)
	case flags.claudeLogin:
		cmd.DoClaudeLogin(state.cfg, state.loginOptions)
	case flags.kimiLogin:
		cmd.DoKimiLogin(state.cfg, state.loginOptions)
	default:
		runApplication(flags, state)
	}
}

func runApplication(flags runtimeFlags, state startupState) {
	if state.cloudDeploy && !state.configReady {
		cmd.WaitForCloudDeploy()
		return
	}
	if flags.localModel && (!flags.tuiMode || flags.standalone) {
		log.Info("Local model mode: using embedded model catalog, remote model updates disabled")
	}
	if flags.tuiMode {
		runTUI(flags, state)
		return
	}
	startSupportServices(state.configFilePath, flags.localModel)
	cmd.StartService(state.cfg, state.configFilePath, flags.password)
}

func runTUI(flags runtimeFlags, state startupState) {
	if flags.standalone {
		runStandaloneTUI(state, flags.password, flags.localModel)
		return
	}
	if err := tui.Run(state.cfg.Port, flags.password, nil, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
	}
}

func startSupportServices(configFilePath string, localModel bool) {
	managementasset.StartAutoUpdater(context.Background(), configFilePath)
	misc.StartAntigravityVersionUpdater(context.Background())
	if !localModel {
		registry.StartModelsUpdater(context.Background())
	}
}

func runStandaloneTUI(state startupState, password string, localModel bool) {
	startSupportServices(state.configFilePath, localModel)

	hook := tui.NewLogHook(2000)
	hook.SetFormatter(&logging.LogFormatter{})
	log.AddHook(hook)

	ioState, err := suppressProcessIO()
	if err == nil {
		defer ioState.restore()
	}

	password = effectiveTUISecret(password)
	cancel, done := cmd.StartServiceBackground(state.cfg, state.configFilePath, password)

	if !waitForEmbeddedServer(state.cfg.Port, password) {
		if ioState != nil {
			ioState.restore()
		}
		cancel()
		<-done
		fmt.Fprintf(os.Stderr, "TUI error: embedded server is not ready\n")
		return
	}

	runErr := tui.Run(state.cfg.Port, password, hook, ioState.output())
	if ioState != nil {
		ioState.restore()
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", runErr)
	}

	cancel()
	<-done
}

func suppressProcessIO() (*ioState, error) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}

	state := &ioState{
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		logOutput: log.StandardLogger().Out,
		devNull:   devNull,
	}
	log.SetOutput(io.Discard)
	os.Stdout = devNull
	os.Stderr = devNull
	return state, nil
}

func (s *ioState) restore() {
	if s == nil {
		return
	}
	os.Stdout = s.stdout
	os.Stderr = s.stderr
	log.SetOutput(s.logOutput)
	if s.devNull != nil {
		_ = s.devNull.Close()
		s.devNull = nil
	}
}

func (s *ioState) output() io.Writer {
	if s == nil || s.stdout == nil {
		return os.Stdout
	}
	return s.stdout
}

func effectiveTUISecret(password string) string {
	if password != "" {
		return password
	}
	return fmt.Sprintf("tui-%d-%d", os.Getpid(), time.Now().UnixNano())
}

func waitForEmbeddedServer(port int, password string) bool {
	client := tui.NewClient(port, password)
	backoff := 100 * time.Millisecond
	for range 30 {
		if _, err := client.GetConfig(); err == nil {
			return true
		}
		time.Sleep(backoff)
		if backoff < time.Second {
			backoff = time.Duration(float64(backoff) * 1.5)
		}
	}
	return false
}
