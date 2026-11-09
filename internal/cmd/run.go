// Package cmd provides command-line interface functionality for the CLI Proxy API server.
// It includes authentication flows for various AI service providers, service startup,
// and other command-line operations.
package cmd

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	log "github.com/sirupsen/logrus"
)

const parentMonitorInterval = 2 * time.Second

// HostOptions configures lifecycle and in-memory access owned by a supervising process.
type HostOptions struct {
	RuntimeContractVersion string
	ParentPID              int
	EphemeralAPIKey        string
}

// StartService builds and runs the proxy service using the exported SDK.
// It creates a new proxy service instance, sets up signal handling for graceful shutdown,
// and starts the service with the provided configuration.
//
// Parameters:
//   - cfg: The application configuration
//   - configPath: The path to the configuration file
//   - localPassword: Optional password accepted for local management requests
func StartService(cfg *config.Config, configPath string, localPassword string) {
	StartServiceWithPluginHost(cfg, configPath, localPassword, nil, HostOptions{})
}

// StartServiceWithPluginHost builds and runs the proxy service with a shared plugin host.
func StartServiceWithPluginHost(cfg *config.Config, configPath string, localPassword string, host *pluginhost.Host, hostOptions HostOptions, serverOptions ...api.ServerOption) {
	builder := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithLocalManagementPassword(localPassword)
	if host != nil {
		builder = builder.WithPluginHost(host)
	}
	if len(serverOptions) > 0 {
		builder = builder.WithServerOptions(serverOptions...)
	}

	ctxSignal, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	runCtx, cancelRun := context.WithCancel(ctxSignal)
	defer cancelRun()

	var errHostOptions error
	builder, errHostOptions = applyHostOptions(builder, hostOptions, runCtx, cancelRun)
	if errHostOptions != nil {
		log.Errorf("invalid host options: %v", errHostOptions)
		return
	}

	if localPassword != "" {
		var keepAliveCancel context.CancelFunc
		runCtx, keepAliveCancel = context.WithCancel(runCtx)
		builder = builder.WithServerOptions(api.WithKeepAliveEndpoint(10*time.Second, func() {
			log.Warn("keep-alive endpoint idle for 10s, shutting down")
			keepAliveCancel()
		}))
	}

	service, err := builder.Build()
	if err != nil {
		log.Errorf("failed to build proxy service: %v", err)
		return
	}

	err = service.Run(runCtx)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Errorf("proxy service exited with error: %v", err)
	}
}

func applyHostOptions(builder *cliproxy.Builder, hostOptions HostOptions, runCtx context.Context, cancelRun context.CancelFunc) (*cliproxy.Builder, error) {
	if hostOptions.EphemeralAPIKey != "" {
		builder = builder.WithEphemeralAPIKey(hostOptions.EphemeralAPIKey)
	}
	if hostOptions.ParentPID > 0 {
		if errParent := validateParentPID(hostOptions.ParentPID, processExists); errParent != nil {
			return builder, errParent
		}
		go func() {
			if errParent := monitorParent(runCtx, hostOptions.ParentPID, parentMonitorInterval, processExists); errors.Is(errParent, errParentProcessExited) {
				log.Warnf("parent process %d exited, shutting down", hostOptions.ParentPID)
				cancelRun()
			} else if errParent != nil && !errors.Is(errParent, context.Canceled) {
				log.Errorf("parent process monitor failed: %v", errParent)
				cancelRun()
			}
		}()
	}
	if hostOptions.RuntimeContractVersion != "" {
		builder = builder.WithServerOptions(api.WithHostRuntimeControl(managementHandlers.RuntimeInfo{
			ContractVersion:  hostOptions.RuntimeContractVersion,
			ComponentVersion: buildinfo.Version,
			Commit:           buildinfo.Commit,
			BuildTime:        buildinfo.BuildDate,
			PID:              os.Getpid(),
			StartedAt:        time.Now().UTC(),
		}, cancelRun))
	}
	return builder, nil
}

// StartServiceBackground starts the proxy service in a background goroutine
// and returns a cancel function for shutdown and a done channel.
func StartServiceBackground(cfg *config.Config, configPath string, localPassword string) (cancel func(), done <-chan struct{}) {
	return StartServiceBackgroundWithPluginHost(cfg, configPath, localPassword, nil, HostOptions{})
}

// StartServiceBackgroundWithPluginHost starts the proxy service with a shared plugin host.
func StartServiceBackgroundWithPluginHost(cfg *config.Config, configPath string, localPassword string, host *pluginhost.Host, hostOptions HostOptions, serverOptions ...api.ServerOption) (cancel func(), done <-chan struct{}) {
	builder := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithLocalManagementPassword(localPassword)
	if host != nil {
		builder = builder.WithPluginHost(host)
	}
	if len(serverOptions) > 0 {
		builder = builder.WithServerOptions(serverOptions...)
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	var errHostOptions error
	builder, errHostOptions = applyHostOptions(builder, hostOptions, ctx, cancelFn)
	if errHostOptions != nil {
		log.Errorf("invalid host options: %v", errHostOptions)
		cancelFn()
		close(doneCh)
		return cancelFn, doneCh
	}

	service, err := builder.Build()
	if err != nil {
		log.Errorf("failed to build proxy service: %v", err)
		cancelFn()
		close(doneCh)
		return cancelFn, doneCh
	}

	go func() {
		defer close(doneCh)
		if err := service.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("proxy service exited with error: %v", err)
		}
	}()

	return cancelFn, doneCh
}

// WaitForCloudDeploy waits indefinitely for shutdown signals in cloud deploy mode
// when no configuration file is available.
func WaitForCloudDeploy() {
	// Clarify that we are intentionally idle for configuration and not running the API server.
	log.Info("Cloud deploy mode: No config found; standing by for configuration. API server is not started. Press Ctrl+C to exit.")

	ctxSignal, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Block until shutdown signal is received
	<-ctxSignal.Done()
	log.Info("Cloud deploy mode: Shutdown signal received; exiting")
}
