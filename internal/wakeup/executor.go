package wakeup

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	wakeupPrompt         = "hi"
	wakeupModel          = "gemini-2.5-flash"
	defaultWakeupTimeout = 30 * time.Second
)

// Executor handles the actual wakeup requests to providers.
// It embeds AntigravityExecutor to reuse OAuth token handling and URL fallback logic.
type Executor struct {
	cfg                  *config.Config
	authManager          *coreauth.Manager
	history              *History
	antigravityExecutor  *executor.AntigravityExecutor
}

// NewExecutor creates a new wakeup executor.
func NewExecutor(cfg *config.Config, authManager *coreauth.Manager, history *History) *Executor {
	return &Executor{
		cfg:                 cfg,
		authManager:         authManager,
		history:             history,
		antigravityExecutor: executor.NewAntigravityExecutor(cfg),
	}
}

// SetConfig updates the configuration reference.
func (e *Executor) SetConfig(cfg *config.Config) {
	e.cfg = cfg
	e.antigravityExecutor = executor.NewAntigravityExecutor(cfg)
}

// SetAuthManager updates the auth manager reference.
func (e *Executor) SetAuthManager(manager *coreauth.Manager) {
	e.authManager = manager
}

// Execute performs wakeup for the specified provider and returns execution records.
func (e *Executor) Execute(ctx context.Context, scheduleID, scheduleName, provider string, models, accounts []string) []WakeupRecord {
	var records []WakeupRecord

	if provider == "" {
		provider = "antigravity"
	}

	// Get all auth entries for the provider
	auths := e.getAuthsForProvider(provider, accounts)
	if len(auths) == 0 {
		log.Warnf("wakeup: no auth entries found for provider %s", provider)
		return records
	}

	for _, auth := range auths {
		record := e.wakeupAuth(ctx, scheduleID, scheduleName, auth, models)
		records = append(records, record...)
		for _, r := range record {
			e.history.Add(r)
		}
	}

	return records
}

func (e *Executor) getAuthsForProvider(provider string, accounts []string) []*coreauth.Auth {
	if e.authManager == nil {
		return nil
	}

	var result []*coreauth.Auth
	allAuths := e.authManager.List()

	accountSet := make(map[string]struct{}, len(accounts))
	for _, a := range accounts {
		accountSet[strings.ToLower(strings.TrimSpace(a))] = struct{}{}
	}

	for _, auth := range allAuths {
		if auth == nil || auth.Provider != provider {
			continue
		}
		if auth.Status != coreauth.StatusActive {
			continue
		}
		// Filter by accounts if specified
		if len(accountSet) > 0 {
			id := strings.ToLower(auth.ID)
			label := strings.ToLower(auth.Label)
			if _, ok := accountSet[id]; !ok {
				if _, ok := accountSet[label]; !ok {
					continue
				}
			}
		}
		result = append(result, auth)
	}

	return result
}

func (e *Executor) wakeupAuth(ctx context.Context, scheduleID, scheduleName string, auth *coreauth.Auth, models []string) []WakeupRecord {
	var records []WakeupRecord

	// If no models specified, just ping the API
	if len(models) == 0 {
		models = []string{wakeupModel}
	}

	for _, model := range models {
		start := time.Now()
		record := WakeupRecord{
			ID:           uuid.New().String(),
			ScheduleID:   scheduleID,
			ScheduleName: scheduleName,
			AccountID:    auth.ID,
			AccountLabel: auth.Label,
			Model:        model,
			Status:       StatusRunning,
			ExecutedAt:   start,
		}

		err := e.sendWakeupRequest(ctx, auth, model)
		record.Duration = time.Since(start).Milliseconds()

		if err != nil {
			record.Status = StatusFailed
			record.Message = err.Error()
			log.Warnf("wakeup: failed for %s/%s: %v", auth.Label, model, err)
		} else {
			record.Status = StatusSuccess
			record.Message = "Wakeup successful"
			log.Infof("wakeup: success for %s/%s in %dms", auth.Label, model, record.Duration)
		}

		records = append(records, record)
	}

	return records
}

func (e *Executor) sendWakeupRequest(ctx context.Context, auth *coreauth.Auth, model string) error {
	// Resolve model alias to upstream name using authManager
	upstreamModel := model
	if e.authManager != nil {
		upstreamModel = e.authManager.ResolveUpstreamModel(auth, model)
		if upstreamModel != model {
			log.Debugf("wakeup: resolved model alias %s -> %s", model, upstreamModel)
		}
	}

	// Build a simple Gemini-format request payload
	// The antigravity executor will transform this to the correct format
	geminiPayload := []byte(`{
		"contents": [{"role": "user", "parts": [{"text": "` + wakeupPrompt + `"}]}],
		"generationConfig": {"maxOutputTokens": 10}
	}`)

	// Create executor request using Gemini format
	// The antigravity executor handles all the translation internally
	req := cliproxyexecutor.Request{
		Model:   upstreamModel,
		Payload: geminiPayload,
		Format:  sdktranslator.FromString("gemini"),
	}

	opts := cliproxyexecutor.Options{
		Stream:       false,
		SourceFormat: sdktranslator.FromString("gemini"),
	}

	// Use timeout context for wakeup
	reqCtx, cancel := context.WithTimeout(ctx, defaultWakeupTimeout)
	defer cancel()

	// Execute request using antigravity executor (reuses all URL fallback, token refresh logic)
	_, err := e.antigravityExecutor.Execute(reqCtx, auth, req, opts)
	return err
}
