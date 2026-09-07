package cliproxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestService_ForcedCooldownSurvivesWatcherReplacement(t *testing.T) {
	ctx := context.Background()
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	const authID = "watcher-forced-cooldown"
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(authID) })
	fresh := func() *coreauth.Auth {
		return &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"disable_cooling": false}}
	}
	service.applyCoreAuthAddOrUpdate(ctx, fresh())
	models := GlobalModelRegistry().GetModelsForClient(authID)
	if len(models) == 0 {
		t.Fatal("fixture did not register models")
	}
	model := models[0].ID
	service.coreManager.MarkResult(ctx, coreauth.Result{AuthID: authID, Provider: "claude", Model: model, Success: true})
	retry := 10 * time.Minute
	service.coreManager.MarkResult(ctx, coreauth.Result{AuthID: authID, Provider: "claude", RetryAfter: &retry, Error: &coreauth.Error{Code: coreauth.ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
	before, _ := service.coreManager.GetByID(authID)
	prepared := service.prepareCoreAuthForModelRegistration(ctx, fresh())
	if prepared == nil || !prepared.ForcedCooldownUntil.Equal(before.ForcedCooldownUntil) {
		t.Error("watcher preparation lost the credential forced deadline")
	}
	service.completeModelRegistrationForAuth(ctx, prepared)
	service.coreManager.MarkResult(ctx, coreauth.Result{AuthID: authID, Provider: "claude", Model: model, Success: true})
	after, _ := service.coreManager.GetByID(authID)
	if !after.ForcedCooldownUntil.Equal(before.ForcedCooldownUntil) || !after.Unavailable {
		t.Error("watcher replacement and model registration cleared the forced cooldown")
	}
	selector := &coreauth.FillFirstSelector{}
	if selected, errPick := selector.Pick(ctx, "claude", model, cliproxyexecutor.Options{}, []*coreauth.Auth{after}); selected != nil || errPick == nil {
		t.Error("selector reused the credential after a watcher replacement")
	}
}
