package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestServiceRun_WatcherReloadConcurrentWithStartupModelRegistration_Issue6583(t *testing.T) {
	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("failed to close listener: %v", errClose)
	}

	authDir := t.TempDir()
	fileName := "codex-startup-race-6583.json"
	filePath := filepath.Join(authDir, fileName)
	teamIDToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ICJ1c2VyQGV4YW1wbGUuY29tIiwgImh0dHBzOi8vYXBpLm9wZW5haS5jb20vYXV0aCI6IHsiY2hhdGdwdF9wbGFuX3R5cGUiOiAidGVhbSIsICJjaGF0Z3B0X2FjY291bnRfaWQiOiAiYWNjLTEyMyJ9fQ.sig"
	initialAuthJSON := fmt.Sprintf(`{"type":"codex","email":"user@example.com","access_token":"token-initial","id_token":%q,"prefix":"acct"}`, teamIDToken)
	if errWrite := os.WriteFile(filePath, []byte(initialAuthJSON), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	cfgPath := filepath.Join(authDir, "config.yaml")
	if errWrite := os.WriteFile(cfgPath, []byte("{}"), 0o600); errWrite != nil {
		t.Fatalf("write dummy config: %v", errWrite)
	}

	initialCfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ForceModelPrefix: false,
		},
		Host:    "127.0.0.1",
		Port:    port,
		AuthDir: authDir,
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "compat-startup",
				BaseURL: "https://compat.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-startup"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "model-initial", Alias: "alias-initial"},
				},
			},
		},
	}

	updatedCfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ForceModelPrefix: true,
		},
		Host:    "127.0.0.1",
		Port:    port,
		AuthDir: authDir,
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "compat-startup",
				BaseURL: "https://compat.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-startup"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "model-updated", Alias: "alias-updated"},
				},
			},
		},
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-6-astra", Alias: "astra-aliased", Fork: true},
			},
		},
	}

	reg := internalregistry.GetGlobalRegistry()
	secondAuthID := "codex-added-during-batch-6583"
	reg.UnregisterClient(fileName)
	reg.UnregisterClient(secondAuthID)
	t.Cleanup(func() {
		reg.UnregisterClient(fileName)
		reg.UnregisterClient(secondAuthID)
		modelRegistrationTaskHook = nil
		modelRegistrationTaskPostRunHook = nil
	})

	var service *Service
	var reloadFn func(*config.Config)
	var reloadWg sync.WaitGroup
	watcherStarted := make(chan struct{})
	var watcherStartedOnce sync.Once
	startupBatchEntered := make(chan struct{})
	var startupBatchEnteredOnce sync.Once

	var watcherStartReached atomic.Bool
	var addedSecondAuth atomic.Bool
	modelRegistrationTaskPostRunHook = func(authID string) {
		if !watcherStartReached.Load() || authID != fileName {
			return
		}
		if addedSecondAuth.CompareAndSwap(false, true) && service != nil && service.coreManager != nil {
			_, _ = service.coreManager.Register(context.Background(), &coreauth.Auth{
				ID:       secondAuthID,
				Provider: "codex",
				Prefix:   "acct2",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"plan_type": "team",
				},
			})
			if reloadFn != nil {
				clonedReload := updatedCfg.CloneForRuntime()
				reloadWg.Add(1)
				go func() {
					defer reloadWg.Done()
					reloadFn(clonedReload)
				}()
			}
			// Do not block inside the hook on commit completion so the second pass of
			// registerModelsForAuthBatch in Service.Run races freely against reloadFn.
			startupBatchEnteredOnce.Do(func() { close(startupBatchEntered) })
		}
	}

	customWatcherFactory := func(configPath, dir string, reload func(*config.Config)) (*WatcherWrapper, error) {
		reloadFn = reload
		clonedFactoryReload := updatedCfg.CloneForRuntime()
		reloadWg.Add(1)
		go func() {
			defer reloadWg.Done()
			reload(clonedFactoryReload)
		}()
		return &WatcherWrapper{
			start: func(ctx context.Context) error {
				watcherStartReached.Store(true)
				watcherStartedOnce.Do(func() { close(watcherStarted) })
				return nil
			},
			stop: func() error {
				return nil
			},
			setConfig: func(cfg *config.Config) {
				if cfg != nil {
					_ = cfg.AuthDir
				}
			},
			setPluginAuthParser: func(parser PluginAuthParser) {},
		}, nil
	}

	var errBuild error
	service, errBuild = NewBuilder().
		WithConfig(initialCfg).
		WithConfigPath(cfgPath).
		WithWatcherFactory(customWatcherFactory).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}

	waitReloadWg := func() bool {
		done := make(chan struct{})
		go func() {
			reloadWg.Wait()
			close(done)
		}()
		select {
		case <-done:
			return true
		case <-time.After(5 * time.Second):
			return false
		}
	}

	waitRuntimeSettled := func() bool {
		done := make(chan struct{})
		go func() {
			service.configRuntimeMu.Lock()
			service.configRuntimeMu.Unlock()
			close(done)
		}()
		select {
		case <-done:
			return true
		case <-time.After(5 * time.Second):
			return false
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	var stopOnce sync.Once
	stopRun := func() {
		stopOnce.Do(func() {
			cancel()
			if !waitReloadWg() {
				t.Errorf("timed out waiting for reload goroutines to exit")
			}
			select {
			case errRun := <-runErrCh:
				if errRun != nil && !errors.Is(errRun, context.Canceled) {
					t.Errorf("unexpected Run error: %v", errRun)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for Run to exit")
			}
		})
	}
	t.Cleanup(func() {
		stopRun()
		for _, a := range service.coreManager.List() {
			if a != nil {
				reg.UnregisterClient(a.ID)
			}
		}
	})

	go func() {
		runErrCh <- service.Run(runCtx)
	}()

	select {
	case <-watcherStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watcher Start callback")
	}

	select {
	case <-startupBatchEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Service.Run startup batch model registration hook")
	}

	if !waitReloadWg() {
		t.Fatal("timed out waiting for concurrent watcher reload to complete")
	}
	if !waitRuntimeSettled() {
		t.Fatal("timed out waiting for Service.Run and config runtime updates to settle")
	}
	if reloadFn == nil {
		t.Fatal("expected watcherFactory to provide reload callback")
	}
	if !addedSecondAuth.Load() {
		t.Fatal("expected startup batch model registration hook to add secondAuthID")
	}

	if !reg.ClientSupportsModel(secondAuthID, "acct2/astra-aliased") {
		t.Fatalf("expected secondAuthID %q to support aliased prefixed model %q from updatedCfg", secondAuthID, "acct2/astra-aliased")
	}
	if reg.ClientSupportsModel(secondAuthID, "gpt-6-astra") {
		t.Fatalf("expected ForceModelPrefix=true from updatedCfg to suppress unprefixed model for %q", secondAuthID)
	}
	foundUpdatedCompat := false
	for _, a := range service.coreManager.List() {
		if a != nil && reg.ClientSupportsModel(a.ID, "alias-updated") {
			foundUpdatedCompat = true
		}
		if a != nil && reg.ClientSupportsModel(a.ID, "alias-initial") {
			t.Fatalf("expected stale startup compat model alias-initial to not overwrite updatedCfg on %q", a.ID)
		}
	}
	if !foundUpdatedCompat {
		t.Fatal("expected registered openai-compatibility auth to support alias-updated from updatedCfg")
	}

	stopRun()
}

func TestRegisterModelsForAuthBatch_ConsistentConfigSnapshotAcrossConcurrentCommit_Issue6583(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "compat-snap"})

	initialCfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ForceModelPrefix: false,
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "compat-snap",
				BaseURL: "https://compat.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-snap"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "model-v1", Alias: "alias-v1"},
				},
			},
		},
	}

	updatedCfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ForceModelPrefix: true,
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "compat-snap",
				BaseURL: "https://compat.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-snap"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "model-v2", Alias: "alias-v2"},
				},
			},
		},
	}

	service := &Service{
		cfg:         initialCfg.CloneForRuntime(),
		coreManager: manager,
	}

	authID := "compat-auth-6583"
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "compat-snap",
		Prefix:   "pfx",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":                   "apikey",
			"compat_name":                 "compat-snap",
			"provider_key":                "compat-snap",
			coreauth.AttributeSource:      "config:compat-snap[token-1]",
			coreauth.AttributeConfigIndex: "0",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	reg := internalregistry.GetGlobalRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		modelRegistrationTaskHook = nil
	})

	var committed atomic.Bool
	modelRegistrationTaskHook = func() {
		if committed.CompareAndSwap(false, true) {
			service.commitConfigUpdate(updatedCfg.CloneForRuntime())
		}
	}

	service.registerModelsForAuthBatch(ctx, manager.List())

	// Because commitConfigUpdate ran before registerModelsForAuthWithCache read the config,
	// registration must not mix compatCache from initialCfg (alias-v1) with ForceModelPrefix from updatedCfg.
	if reg.ClientSupportsModel(authID, "pfx/alias-v1") && !reg.ClientSupportsModel(authID, "alias-v1") {
		t.Fatalf("detected torn config snapshot: registered initialCfg model alias-v1 with updatedCfg ForceModelPrefix=true (only pfx/alias-v1 present)")
	}
	if !reg.ClientSupportsModel(authID, "pfx/alias-v2") {
		t.Fatalf("expected auth %q to support pfx/alias-v2 from updatedCfg", authID)
	}
}

func TestRegisterModelsForAuthBatch_ConcurrentCommitConfigUpdateRace_Issue6583(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "codex"})
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "claude"})
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "compat-race"})

	makeCfg := func(forcePrefix bool, aliasSuffix int) *config.Config {
		return &config.Config{
			SDKConfig: config.SDKConfig{
				ForceModelPrefix: forcePrefix,
			},
			OAuthExcludedModels: map[string][]string{
				"codex": {"gpt-5.5"},
			},
			OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"codex": {
					{Name: "gpt-6-astra", Alias: fmt.Sprintf("astra-alias-%d", aliasSuffix), Fork: true},
				},
			},
			OAuthSettings: map[string][]config.OAuthModelSetting{
				"codex": {
					{Name: "gpt-6-astra", MaxContextLength: 128000},
				},
			},
			ClaudeKey: []config.ClaudeKey{
				{
					APIKey: "sk-claude-race",
					Models: []config.ClaudeModel{
						{Name: "claude-opus-4-6", Alias: fmt.Sprintf("claude-alias-%d", aliasSuffix)},
					},
				},
			},
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "compat-race",
					BaseURL: "https://compat.example.com/v1",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "sk-compat-race"},
					},
					Models: []config.OpenAICompatibilityModel{
						{Name: "compat-model", Alias: fmt.Sprintf("compat-alias-%d", aliasSuffix)},
					},
				},
			},
		}
	}

	service := &Service{
		cfg:         makeCfg(false, 0),
		coreManager: manager,
	}

	codexFirstID := "codex-first-pass-6583"
	claudeID := "claude-apikey-6583"
	compatID := "compat-apikey-6583"
	codexSecondID := "codex-second-pass-6583"

	initialAuths := []*coreauth.Auth{
		{
			ID:       codexFirstID,
			Provider: "codex",
			Prefix:   "c1",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"plan_type": "team",
			},
		},
		{
			ID:       claudeID,
			Provider: "claude",
			Prefix:   "cl",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"auth_kind":                   "apikey",
				coreauth.AttributeAPIKey:      "sk-claude-race",
				coreauth.AttributeSource:      "config:claude[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		},
		{
			ID:       compatID,
			Provider: "compat-race",
			Prefix:   "cp",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"auth_kind":                   "apikey",
				"compat_name":                 "compat-race",
				"provider_key":                "compat-race",
				coreauth.AttributeSource:      "config:compat-race[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		},
	}

	reg := internalregistry.GetGlobalRegistry()
	for _, a := range initialAuths {
		reg.UnregisterClient(a.ID)
		if _, errRegister := manager.Register(ctx, a); errRegister != nil {
			t.Fatalf("failed to register auth %s: %v", a.ID, errRegister)
		}
	}
	reg.UnregisterClient(codexSecondID)
	t.Cleanup(func() {
		for _, a := range initialAuths {
			reg.UnregisterClient(a.ID)
		}
		reg.UnregisterClient(codexSecondID)
		modelRegistrationTaskPostRunHook = nil
	})

	startCommits := make(chan struct{})
	var startCommitsOnce sync.Once
	modelRegistrationTaskPostRunHook = func(authID string) {
		if authID == codexFirstID {
			_, _ = manager.Register(ctx, &coreauth.Auth{
				ID:       codexSecondID,
				Provider: "codex",
				Prefix:   "c2",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"plan_type": "team",
				},
			})
			startCommitsOnce.Do(func() { close(startCommits) })
		}
	}

	commitDone := make(chan struct{})
	go func() {
		defer close(commitDone)
		<-startCommits
		for i := 1; i <= 20; i++ {
			service.commitConfigUpdate(makeCfg(i%2 == 1, i))
		}
	}()

	service.registerModelsForAuthBatch(ctx, manager.List())
	select {
	case <-commitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for concurrent commitConfigUpdate loop")
	}
}
