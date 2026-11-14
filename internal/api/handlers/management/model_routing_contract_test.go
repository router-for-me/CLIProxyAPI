package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

const (
	contractSnapshotDigest = "sha256:15303dbab83d64d09f79f1f3a22bc09fb3ad5916f2624283f2c6a0ecbe969801"
)

func managementRoutingProjection() *modelrouting.Config {
	modelKey := modelrouting.ModelKey{CatalogProviderID: "openai", CanonicalModelID: "gpt-5.4"}
	routeKey := modelrouting.RouteKey{ModelKey: modelKey, RouteChannel: "openai"}
	routeSelector := modelrouting.SelectorForRoute(routeKey, "gpt-5.4")
	credentialID := coreauth.CredentialReferenceID("credential-a")
	pricing := &modelrouting.Pricing{
		Currency: "USD", Unit: "per_million_tokens", SourceID: "models_dev",
		Entries: []modelrouting.PricingEntry{{Name: "input", Amount: "2.5"}, {Name: "output", Amount: "15"}},
	}
	health := modelrouting.Health{Status: "healthy", Selectable: true, ObservedAt: "2026-08-27T18:45:00Z"}
	route := modelrouting.DirectRoute{
		RouteKey: routeKey, CatalogRouteProviderID: "openrouter", CatalogRouteModelID: "openai/gpt-5.4",
		RuntimeModelID: "gpt-5.4", RouteSelector: routeSelector,
		QuotaDomains: []string{"quota-a"}, CredentialRefs: []modelrouting.CredentialRef{{ID: credentialID, Kind: "oauth"}},
		Protocols: []string{"openai_chat"}, Restrictions: []modelrouting.Restriction{}, Health: health,
		Pricing: pricing, Selectable: true, SelectionReason: "eligible",
	}
	projection := &modelrouting.Config{
		SchemaVersion: modelrouting.SchemaVersion, Generation: 1, SnapshotDigest: contractSnapshotDigest,
		ProjectionDigest: contractSnapshotDigest,
		DirectModels: []modelrouting.DirectModel{{
			ModelKey: modelKey, DisplayName: "GPT-5.4", Active: true,
			Variants: []modelrouting.Variant{}, Routes: []modelrouting.DirectRoute{route},
		}},
		Aliases: []modelrouting.Alias{{
			Name: "aihub-primary", TierID: "primary", Selectable: true, Reason: "selected",
			Members: []modelrouting.Member{{
				ModelKey: modelKey, MemberRank: 1, ModelScore: "1", SelectionReason: "selected member",
				Candidates: []modelrouting.Candidate{{
					RouteKey: routeKey, CatalogRouteProviderID: "openrouter",
					CatalogRouteModelID: "openai/gpt-5.4", RuntimeModelID: "gpt-5.4", RouteSelector: routeSelector,
					RouteRank: 1, QuotaDomains: []string{"quota-a"},
					CredentialRefs: []modelrouting.CredentialRef{{ID: credentialID, Kind: "oauth"}},
					Protocols:      []string{"openai_chat"}, Health: health, Restrictions: []modelrouting.Restriction{},
					Pricing: pricing, SelectionReason: "ranked first",
				}},
			}},
		}},
		FailurePolicy: modelrouting.FailurePolicy{
			Mode: "classified_candidate_failover", CredentialAcquisitionTimeoutSeconds: 120,
			AutomaticRetry: false, AutomaticFailover: true,
			FailoverRules: []modelrouting.FailoverRule{{
				RuleID: "capacity", HTTPStatuses: []int{429},
				ErrorCodes:   []string{"credential_concurrency_exceeded", "model_cooldown", "rate_limit"},
				FailureKinds: []modelrouting.FailureKind{modelrouting.FailureKindCredential},
			}, {
				RuleID: "pre-response-transient", HTTPStatuses: []int{408, 500, 502, 503, 504},
				ErrorCodes:   []string{"empty_completion", "empty_stream", "home_unavailable", "upstream_failed"},
				FailureKinds: []modelrouting.FailureKind{modelrouting.FailureKindEmptyPreResponse, modelrouting.FailureKindTransport, modelrouting.FailureKindUpstreamTimeout},
			}},
			ServeStaleOnError:  false,
			PreserveFirstError: true, TerminateOwnedRequestOnCancel: true,
		},
	}
	digest, err := modelrouting.ProjectionDigest(projection)
	if err != nil {
		panic(err)
	}
	projection.ProjectionDigest = digest
	return projection
}

func TestPutConfigYAMLReturnsActiveDigestReceipt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projection := managementRoutingProjection()
	shared := projection.Aliases[0]
	shared.Name = "aihub-shared"
	shared.TierID = "shared"
	projection.Aliases = append(projection.Aliases, shared)
	digest, errDigest := modelrouting.ProjectionDigest(projection)
	if errDigest != nil {
		t.Fatal(errDigest)
	}
	projection.ProjectionDigest = digest
	payload, err := yaml.Marshal(&config.Config{
		Port:               8317,
		CredentialInFlight: config.DefaultCredentialInFlightConfig(),
		ModelRouting:       projection,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(&config.Config{Port: 8317}, path, nil)
	loadedAt := time.Date(2026, 8, 28, 11, 20, 57, 0, time.UTC)
	published := false
	h.SetConfigPublishHook(func(_ context.Context, body []byte, expected *modelrouting.ActiveIdentityV2, bootstrap bool) (*config.Config, *modelrouting.ActivationReceiptV2, error) {
		if !bootstrap || expected != nil {
			t.Fatalf("publisher precondition = bootstrap %t, expected %+v", bootstrap, expected)
		}
		parsed, errParse := config.ParseConfigBytes(body)
		if errParse != nil {
			t.Fatalf("ParseConfigBytes() error = %v", errParse)
		}
		published = true
		active := modelrouting.ActiveIdentityV2{
			Generation:       projection.Generation,
			SnapshotDigest:   projection.SnapshotDigest,
			ProjectionDigest: projection.ProjectionDigest,
			ConfigDigest:     modelrouting.ConfigDigest(body),
		}
		return parsed, &modelrouting.ActivationReceiptV2{
			Active: active, RoutingSchema: modelrouting.RoutingSchemaInfo{Version: 3, Digest: modelrouting.SchemaDigest()}, LoadedAt: loadedAt,
		}, nil
	})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(string(payload)))
	c.Request.Header.Set("Content-Type", "application/yaml")
	c.Request.Header.Set("If-None-Match", "*")
	h.PutConfigYAML(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !published {
		t.Fatal("Service-owned CAS publisher was not called")
	}
	var receipt modelrouting.ActivationReceiptV2
	if err = json.Unmarshal(recorder.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Active.Generation != projection.Generation || receipt.Active.SnapshotDigest != contractSnapshotDigest || receipt.Active.ProjectionDigest != projection.ProjectionDigest {
		t.Fatalf("receipt = %+v", receipt)
	}
	wantETag, err := modelrouting.ActiveETag(receipt.Active)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Header().Get("ETag") != wantETag {
		t.Fatalf("ETag = %q, want %q", recorder.Header().Get("ETag"), wantETag)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config-write-") {
			t.Fatalf("atomic writer leaked staging file %q", entry.Name())
		}
	}
}

func TestPutConfigYAMLHashesManagementSecretBeforeAtomicPublish(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const plaintext = "management-secret-that-must-not-reach-disk"
	payload := "port: 8317\nremote-management:\n  secret-key: " + plaintext + "\n"
	h := NewHandler(&config.Config{Port: 8317}, path, nil)
	h.SetConfigReloadHook(func(_ context.Context, _ *config.Config) error { return nil })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(payload))
	h.PutConfigYAML(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), plaintext) {
		t.Fatalf("plaintext management secret was published: %s", persisted)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = bcrypt.CompareHashAndPassword([]byte(loaded.RemoteManagement.SecretKey), []byte(plaintext)); err != nil {
		t.Fatalf("persisted secret does not verify as bcrypt: %v", err)
	}
}

func TestPutConfigYAMLRollsBackFileMemoryAndRuntimeOnReloadFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	previousBody := []byte("port: 8317\n")
	if err := os.WriteFile(path, previousBody, 0o600); err != nil {
		t.Fatal(err)
	}
	previousConfig := &config.Config{Port: 8317, CredentialInFlight: config.DefaultCredentialInFlightConfig()}
	h := NewHandler(previousConfig, path, nil)
	errApply := errors.New("runtime rejected new config")
	var appliedPorts []int
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) error {
		appliedPorts = append(appliedPorts, cfg.Port)
		if cfg.Port == 8318 {
			return errApply
		}
		return nil
	})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader("port: 8318\n"))
	h.PutConfigYAML(c)

	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), errApply.Error()) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(previousBody) {
		t.Fatalf("persisted config = %q, want rollback %q", persisted, previousBody)
	}
	h.mu.Lock()
	activePort := h.cfg.Port
	h.mu.Unlock()
	if activePort != previousConfig.Port {
		t.Fatalf("handler config port = %d, want %d", activePort, previousConfig.Port)
	}
	if len(appliedPorts) != 2 || appliedPorts[0] != 8318 || appliedPorts[1] != 8317 {
		t.Fatalf("runtime applications = %v, want failed new then restored previous", appliedPorts)
	}
}

func TestProjectedInventoryFailsClosedForSuspendedCredential(t *testing.T) {
	projection := managementRoutingProjection()
	auth := &coreauth.Auth{
		ID: "credential-a", Provider: "openai", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "quota_domain": "quota-a"},
		UpdatedAt:  time.Date(2026, 8, 27, 18, 45, 0, 0, time.UTC),
	}
	registered := []registry.RegisteredRouteSnapshot{{
		ClientID: "credential-a", RouteChannel: "openai", RuntimeModelID: "gpt-5.4",
		Model: &registry.ModelInfo{ID: "gpt-5.4"}, SuspensionReason: "account suspended",
	}}
	models := projectedInventoryModels(projection, registered, map[string]*coreauth.Auth{"credential-a": auth}, time.Now())
	if len(models) != 1 || len(models[0].Routes) != 1 || len(models[0].Routes[0].Credentials) != 1 {
		t.Fatalf("inventory shape = %+v", models)
	}
	route := models[0].Routes[0]
	credential := route.Credentials[0]
	if route.Selectable || models[0].Active || credential.Health.Selectable || !credential.Suspension.Active {
		t.Fatalf("suspended route failed open: model=%+v route=%+v credential=%+v", models[0], route, credential)
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "credential-a") {
		t.Fatalf("inventory leaked raw credential ID: %s", encoded)
	}
}

func TestProjectedInventoryQuotaCooldownEndsAtRecoveryInstant(t *testing.T) {
	const credentialID = "credential-a"
	const runtimeModelID = "gpt-5.4"
	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(ctx, &coreauth.Auth{
		ID: credentialID, Provider: "openai", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "quota_domain": "quota-a"},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(credentialID, "openai", []*registry.ModelInfo{{ID: runtimeModelID}})
	t.Cleanup(func() { reg.UnregisterClient(credentialID) })

	if errMark := manager.MarkResult(ctx, coreauth.Result{
		AuthID: credentialID, Provider: "openai", Model: runtimeModelID, Success: false,
		Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
	}); errMark != nil {
		t.Fatalf("MarkResult() error = %v", errMark)
	}
	auth, ok := manager.GetByID(credentialID)
	if !ok || auth == nil || auth.ModelStates[runtimeModelID] == nil {
		t.Fatalf("model state after quota cooldown is missing: %+v", auth)
	}
	recoverAt := auth.ModelStates[runtimeModelID].Quota.NextRecoverAt
	if recoverAt.IsZero() {
		t.Fatal("quota cooldown without Retry-After has no recovery instant")
	}
	auths := map[string]*coreauth.Auth{credentialID: auth}
	inventory := func(now time.Time) (modelrouting.InventoryModel, modelrouting.InventoryCredential) {
		t.Helper()
		registered := make([]registry.RegisteredRouteSnapshot, 0, 1)
		for _, snapshot := range reg.RegisteredRouteSnapshots() {
			if snapshot.ClientID == credentialID {
				registered = append(registered, snapshot)
			}
		}
		models := projectedInventoryModels(managementRoutingProjection(), registered, auths, now)
		if len(models) != 1 || len(models[0].Routes) != 1 || len(models[0].Routes[0].Credentials) != 1 {
			t.Fatalf("inventory shape = %+v", models)
		}
		return models[0], models[0].Routes[0].Credentials[0]
	}

	blockedModel, blocked := inventory(recoverAt.Add(-time.Nanosecond))
	wantResetsAt := recoverAt.UTC().Format(time.RFC3339Nano)
	if blockedModel.Active || blocked.Health.Selectable || blocked.Quota.Status != "blocked" || blocked.Quota.ResetsAt == nil || *blocked.Quota.ResetsAt != wantResetsAt || blocked.Suspension.Active {
		t.Fatalf("inventory before recovery = model %+v credential %+v, want quota blocked until %s", blockedModel, blocked, wantResetsAt)
	}

	recoveredModel, recovered := inventory(recoverAt)
	if !recoveredModel.Active || !recoveredModel.Routes[0].Selectable || !recovered.Health.Selectable || recovered.Quota.Status != "available" || recovered.Quota.ResetsAt != nil || recovered.Suspension.Active || recovered.Suspension.ResumesAt != nil {
		t.Fatalf("inventory at recovery instant = model %+v credential %+v, want selectable", recoveredModel, recovered)
	}

	reg.SuspendClientModel(credentialID, runtimeModelID, "manual")
	suspendedModel, suspended := inventory(recoverAt.Add(24 * time.Hour))
	if suspendedModel.Active || suspended.Health.Selectable || !suspended.Suspension.Active || suspended.Suspension.Reason == nil || *suspended.Suspension.Reason != "manual" || suspended.Suspension.ResumesAt != nil {
		t.Fatalf("inventory with explicit suspension = model %+v credential %+v, want persistent suspension", suspendedModel, suspended)
	}
}

func TestInventoryCredentialReportsTimeBoundSuspensionResume(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	resumeAt := now.Add(time.Minute)
	auth := &coreauth.Auth{
		ID: "credential-a", Provider: "openai", Status: coreauth.StatusError,
		Attributes: map[string]string{"auth_kind": "oauth", "quota_domain": "quota-a"},
		ModelStates: map[string]*coreauth.ModelState{"gpt-5.4": {
			Status: coreauth.StatusError, StatusMessage: "upstream unavailable", Unavailable: true, NextRetryAfter: resumeAt,
		}},
	}
	route := registry.RegisteredRouteSnapshot{ClientID: "credential-a", RouteChannel: "openai", RuntimeModelID: "gpt-5.4"}

	blocked := inventoryCredential(route, auth, now)
	wantResumesAt := resumeAt.Format(time.RFC3339Nano)
	if blocked.Health.Selectable || !blocked.Suspension.Active || blocked.Suspension.ResumesAt == nil || *blocked.Suspension.ResumesAt != wantResumesAt || blocked.Quota.Status != "available" {
		t.Fatalf("credential before resume = %+v, want suspension resuming at %s", blocked, wantResumesAt)
	}
	resumed := inventoryCredential(route, auth, resumeAt)
	if !resumed.Health.Selectable || resumed.Suspension.Active || resumed.Suspension.ResumesAt != nil {
		t.Fatalf("credential at resume instant = %+v, want selectable", resumed)
	}
}

func TestBootstrapInventoryFailsLoudlyForIncompleteOrManagedRoutes(t *testing.T) {
	auth := &coreauth.Auth{
		ID: "credential-a", Provider: "openai", RouteChannel: "openai", QuotaDomain: "quota-a", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	model := &registry.ModelInfo{
		ID: "gpt-5.4", CatalogProviderID: "openai", CatalogModelID: "gpt-5.4",
		CatalogRouteProviderID: "openai", CatalogRouteModelID: "gpt-5.4", Protocols: []string{"openai_chat"},
	}
	tests := []struct {
		name  string
		route registry.RegisteredRouteSnapshot
		want  string
	}{
		{
			name:  "missing model facts",
			route: registry.RegisteredRouteSnapshot{ClientID: auth.ID, RouteChannel: "openai", RuntimeModelID: "gpt-5.4"},
			want:  "registered route 0 has no model facts",
		},
		{
			name: "managed alias before projection",
			route: registry.RegisteredRouteSnapshot{
				ClientID: auth.ID, RouteChannel: "openai", RuntimeModelID: "aihub-primary", Model: model,
			},
			want: "registered route 0 contains a managed alias before projection",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := bootstrapInventoryModels(
				[]registry.RegisteredRouteSnapshot{test.route},
				map[string]*coreauth.Auth{auth.ID: auth},
				time.Now(),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("bootstrapInventoryModels() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestBootstrapInventorySurfacesRoutesThatCannotDeclareCatalogFacts pins the
// distinction the bootstrap has to make. Only openai-compatibility models carry
// catalog facts: buildConfigModels (which serves claude-api-key, xai, gemini and
// codex) never sets CatalogProviderID, CatalogModelID, CatalogRouteProviderID,
// CatalogRouteModelID or Protocols. Failing the whole inventory over a route
// whose type has nowhere to put those facts strands generation one, because the
// projection that would supply them can only be published once the inventory
// answers. A route that declares none of them is simply not catalog-declared and
// is surfaced under its channel identity; a route that declares some of them is
// malformed and must still fail loudly.
func TestBootstrapInventorySurfacesRoutesThatCannotDeclareCatalogFacts(t *testing.T) {
	auth := &coreauth.Auth{
		ID: "credential-a", Provider: "anthropic", RouteChannel: "claude", QuotaDomain: "quota-a", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "api_key"},
	}
	auths := map[string]*coreauth.Auth{auth.ID: auth}

	// Exactly what buildConfigModels(entry.Models, "anthropic", "claude")
	// produces for a claude-api-key entry: an identity, and no catalog facts.
	claudeRoute := registry.RegisteredRouteSnapshot{
		ClientID: auth.ID, RouteChannel: "claude", RuntimeModelID: "glm-5.3",
		Model: &registry.ModelInfo{ID: "glm-5.3", DisplayName: "glm-5.3"},
	}
	catalogRoute := registry.RegisteredRouteSnapshot{
		ClientID: auth.ID, RouteChannel: "claude", RuntimeModelID: "gpt-5.4",
		Model: &registry.ModelInfo{
			ID: "gpt-5.4", CatalogProviderID: "openai", CatalogModelID: "gpt-5.4",
			CatalogRouteProviderID: "openai", CatalogRouteModelID: "gpt-5.4",
			Protocols: []string{"openai_chat"},
		},
	}

	t.Run("route without catalog facts surfaces under channel identity", func(t *testing.T) {
		models, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{claudeRoute}, auths, time.Now(),
		)
		if err != nil {
			t.Fatalf("bootstrapInventoryModels() error = %v, want nil", err)
		}
		if len(models) != 1 {
			t.Fatalf("bootstrapInventoryModels() built %d models, want 1", len(models))
		}
		if got := models[0].ModelKey.CatalogProviderID; got != "claude" {
			t.Fatalf("catalog provider = %q, want %q", got, "claude")
		}
		if got := models[0].ModelKey.CanonicalModelID; got != "glm-5.3" {
			t.Fatalf("canonical model = %q, want %q", got, "glm-5.3")
		}
	})

	t.Run("catalog-declared route still builds alongside a channel route", func(t *testing.T) {
		models, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{claudeRoute, catalogRoute}, auths, time.Now(),
		)
		if err != nil {
			t.Fatalf("bootstrapInventoryModels() error = %v, want nil", err)
		}
		if len(models) != 2 {
			t.Fatalf("bootstrapInventoryModels() built %d models, want 2", len(models))
		}
	})

	t.Run("route without a wire protocol is not a routing candidate", func(t *testing.T) {
		native := registry.RegisteredRouteSnapshot{
			ClientID: auth.ID, RouteChannel: "opencode", RuntimeModelID: "gemini-3-pro",
			Model: &registry.ModelInfo{ID: "gemini-3-pro", DisplayName: "gemini-3-pro"},
		}
		models, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{native, catalogRoute}, auths, time.Now(),
		)
		if err != nil {
			t.Fatalf("bootstrapInventoryModels() error = %v, want nil", err)
		}
		if len(models) != 1 || models[0].ModelKey.CanonicalModelID != "gpt-5.4" {
			t.Fatalf("bootstrapInventoryModels() built %+v, want only the protocol-bearing route", models)
		}
		for _, route := range models[0].Routes {
			if len(route.Protocols) == 0 {
				t.Fatalf("route %+v has no protocols", route.RouteKey)
			}
		}
	})

	t.Run("partially declared route is malformed and still fails", func(t *testing.T) {
		partial := claudeRoute
		partial.Model = &registry.ModelInfo{ID: "glm-5.3", CatalogProviderID: "zhipuai"}
		_, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{partial}, auths, time.Now(),
		)
		const want = "registered route 0 has invalid catalog model fact"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("bootstrapInventoryModels() error = %v, want %q", err, want)
		}
	})

	t.Run("managed alias is still rejected even without catalog facts", func(t *testing.T) {
		managed := claudeRoute
		managed.RuntimeModelID = "aihub-primary"
		_, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{managed}, auths, time.Now(),
		)
		const want = "registered route 0 contains a managed alias before projection"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("bootstrapInventoryModels() error = %v, want %q", err, want)
		}
	})

	t.Run("every credential of a surfaced route is kept", func(t *testing.T) {
		secondAuth := &coreauth.Auth{
			ID: "credential-b", Provider: "anthropic", RouteChannel: "claude", QuotaDomain: "quota-b", Status: coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "api_key"},
		}
		duplicate := claudeRoute
		duplicate.ClientID = secondAuth.ID
		models, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{claudeRoute, duplicate},
			map[string]*coreauth.Auth{claudeRoute.ClientID: auth, secondAuth.ID: secondAuth},
			time.Now(),
		)
		if err != nil {
			t.Fatalf("bootstrapInventoryModels() error = %v, want nil", err)
		}
		if len(models) != 1 || len(models[0].Routes) != 1 {
			t.Fatalf("bootstrapInventoryModels() built %d models / %d routes, want 1/1", len(models), len(models[0].Routes))
		}
		if got := len(models[0].Routes[0].Credentials); got != 2 {
			t.Fatalf("surfaced route carries %d credentials, want 2 (a duplicate channel+model pair must not drop credentials)", got)
		}
	})

	t.Run("catalog route credentials survive slice growth across three routes of one model", func(t *testing.T) {
		registryRoutes := make([]registry.RegisteredRouteSnapshot, 0, 6)
		registryAuths := map[string]*coreauth.Auth{}
		for channelIndex := 0; channelIndex < 3; channelIndex++ {
			channel := fmt.Sprintf("compat-%d", channelIndex)
			for credentialIndex := 0; credentialIndex < 2; credentialIndex++ {
				credentialID := fmt.Sprintf("credential-%d-%d", channelIndex, credentialIndex)
				registryAuths[credentialID] = &coreauth.Auth{
					ID: credentialID, Provider: channel, RouteChannel: channel,
					QuotaDomain: fmt.Sprintf("quota-%d-%d", channelIndex, credentialIndex), Status: coreauth.StatusActive,
					Attributes: map[string]string{"auth_kind": "api_key"},
				}
				registryRoutes = append(registryRoutes, registry.RegisteredRouteSnapshot{
					ClientID: credentialID, RouteChannel: channel, RuntimeModelID: "glm-5.3",
					Model: &registry.ModelInfo{
						ID: "glm-5.3", CatalogProviderID: "zhipuai", CatalogModelID: "glm-5.3",
						CatalogRouteProviderID: "zhipuai", CatalogRouteModelID: "glm-5.3",
						Protocols: []string{"openai_chat"},
					},
				})
			}
		}
		models, err := bootstrapInventoryModels(registryRoutes, registryAuths, time.Now())
		if err != nil {
			t.Fatalf("bootstrapInventoryModels() error = %v, want nil", err)
		}
		if len(models) != 1 {
			t.Fatalf("bootstrapInventoryModels() built %d models, want 1", len(models))
		}
		for _, route := range models[0].Routes {
			if got := len(route.Credentials); got != 2 {
				t.Fatalf("route %s carries %d credentials, want 2 (slice growth must not orphan route bookkeeping)", route.RouteKey.RouteChannel, got)
			}
		}
		if got := len(models[0].Routes); got != 3 {
			t.Fatalf("model carries %d routes, want 3", got)
		}
	})
}

// A provider activated after the first publication is registered but absent
// from the projection; the inventory endpoint must still report it, or the
// pipeline that reads this surface can never select it.
func TestModelInventoryReportsRegisteredRoutesTheProjectionDoesNotDescribe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()
	for _, auth := range []*coreauth.Auth{
		{
			ID: "credential-a", Provider: "openai", RouteChannel: "openai", QuotaDomain: "quota-a", Status: coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "quota_domain": "quota-a"},
		},
		{
			ID: "credential-later", Provider: "opencode", RouteChannel: "opencode", QuotaDomain: "quota-later", Status: coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "api_key", "quota_domain": "quota-later"},
		},
	} {
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	reg.RegisterClient("credential-a", "openai", []*registry.ModelInfo{{ID: "gpt-5.4"}})
	reg.RegisterClient("credential-later", "opencode", []*registry.ModelInfo{
		{ID: "later-model", VariantID: "high", Protocols: []string{"openai_chat"},
			CatalogProviderID: "opencode", CatalogModelID: "later-model",
			CatalogRouteProviderID: "opencode", CatalogRouteModelID: "later-model"},
		// A gateway route declaring a canonical model whose provider sorts
		// before the projected one: the merged inventory must still be ordered.
		{ID: "early-model", Protocols: []string{"openai_chat"},
			CatalogProviderID: "deepseek", CatalogModelID: "early-model",
			CatalogRouteProviderID: "opencode", CatalogRouteModelID: "early-model"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient("credential-a")
		reg.UnregisterClient("credential-later")
	})

	loadedAt := time.Date(2026, 8, 27, 18, 45, 0, 0, time.UTC)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	h.SetModelRoutingStateHook(func() modelrouting.ActiveStateV2 {
		return modelrouting.ActiveStateV2{
			Identity: &modelrouting.ActiveIdentityV2{Generation: 1},
			LoadedAt: &loadedAt, Projection: managementRoutingProjection(),
		}
	})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/model-inventory", nil)
	h.GetModelInventory(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GetModelInventory() status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var inventory modelrouting.Inventory
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &inventory); errDecode != nil {
		t.Fatalf("decode inventory: %v", errDecode)
	}
	routes := map[string]modelrouting.InventoryModel{}
	for _, model := range inventory.DirectModels {
		for _, route := range model.Routes {
			routes[route.RouteKey.RouteChannel+"/"+route.RuntimeModelID] = model
		}
	}
	projected, okProjected := routes["openai/gpt-5.4"]
	if !okProjected || projected.ModelKey.CatalogProviderID != "openai" || len(projected.Routes) != 1 {
		t.Fatalf("projected route changed or missing: %+v", inventory.DirectModels)
	}
	later, okLater := routes["opencode/later-model"]
	if !okLater {
		t.Fatalf("registered route outside the projection is not reported: %+v", inventory.DirectModels)
	}
	if !later.Active || len(later.Routes) != 1 || len(later.Routes[0].Credentials) != 1 || len(later.Variants) != 1 {
		t.Fatalf("later model lost its credential or variant: %+v", later)
	}
	early, okEarly := routes["opencode/early-model"]
	if !okEarly || early.ModelKey.CatalogProviderID != "deepseek" || early.ModelKey.CanonicalModelID != "early-model" {
		t.Fatalf("gateway route with a declared canonical model is missing or misidentified: %+v", inventory.DirectModels)
	}
	keys := make([]string, 0, len(inventory.DirectModels))
	for _, model := range inventory.DirectModels {
		keys = append(keys, model.ModelKey.CatalogProviderID+"\x00"+model.ModelKey.CanonicalModelID)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("direct models are not ordered by identity: %q", keys)
	}
}

// A route that joins a model the projection already describes brings the
// variants that model did not have yet.
func TestMergeRegistryRoutesKeepsVariantsOfJoiningRoutes(t *testing.T) {
	key := modelrouting.ModelKeyJSON{CatalogProviderID: "openai", CanonicalModelID: "gpt-5.4"}
	variant := func(id string) modelrouting.InventoryVariant {
		return modelrouting.InventoryVariant{VariantKey: modelrouting.VariantKeyJSON{ModelKey: key, VariantID: id}}
	}
	route := func(channel string) modelrouting.InventoryRoute {
		return modelrouting.InventoryRoute{
			RouteKey: modelrouting.RouteKeyJSON{ModelKey: key, RouteChannel: channel}, RuntimeModelID: "gpt-5.4", Selectable: true,
		}
	}
	projected := []modelrouting.InventoryModel{{
		ModelKey: key, Variants: []modelrouting.InventoryVariant{variant("low")}, Routes: []modelrouting.InventoryRoute{route("openai")},
	}}
	registryModels := []modelrouting.InventoryModel{{
		ModelKey: key, Variants: []modelrouting.InventoryVariant{variant("low"), variant("high")},
		Routes: []modelrouting.InventoryRoute{route("openai"), route("codex")},
	}}
	merged := mergeRegistryRoutes(projected, registryModels)
	if len(merged) != 1 || len(merged[0].Routes) != 2 || len(merged[0].Variants) != 2 || !merged[0].Active {
		t.Fatalf("merged model = %+v, want one model with both routes and both variants", merged)
	}
	if merged[0].Routes[0].RouteKey.RouteChannel != "codex" || merged[0].Variants[0].VariantKey.VariantID != "high" {
		t.Fatalf("merged model = %+v, want routes and variants in identity order", merged[0])
	}
}

// An undeclared route keeps the wire protocol its channel owns: a claude
// channel route speaks Anthropic Messages, and a route that registered its own
// protocols keeps them.
func TestBootstrapInventoryKeepsChannelProtocolsForUndeclaredRoutes(t *testing.T) {
	auths := map[string]*coreauth.Auth{"credential-a": {
		ID: "credential-a", Provider: "anthropic", RouteChannel: "claude", QuotaDomain: "quota-a", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "api_key"},
	}}
	models, err := bootstrapInventoryModels([]registry.RegisteredRouteSnapshot{
		{ClientID: "credential-a", RouteChannel: "claude", RuntimeModelID: "glm-5.3", Model: &registry.ModelInfo{ID: "glm-5.3"}},
		{ClientID: "credential-a", RouteChannel: "opencode", RuntimeModelID: "later-model", Model: &registry.ModelInfo{ID: "later-model", Protocols: []string{"openai_chat"}}},
	}, auths, time.Now())
	if err != nil || len(models) != 2 {
		t.Fatalf("bootstrapInventoryModels() = %d models, error %v; want 2, nil", len(models), err)
	}
	protocols := map[string][]string{}
	for _, model := range models {
		protocols[model.Routes[0].RouteKey.RouteChannel] = model.Routes[0].Protocols
	}
	if !equalStrings(protocols["claude"], []string{"anthropic_messages"}) || !equalStrings(protocols["opencode"], []string{"openai_chat"}) {
		t.Fatalf("undeclared route protocols = %v, want claude anthropic_messages and opencode openai_chat", protocols)
	}
}

// The quota-domain failure reaches the HTTP body, so it names the credential by
// its hashed reference and never by the raw client id.
func TestBootstrapInventoryQuotaDomainErrorHidesRawCredentialID(t *testing.T) {
	const rawID = "/home/operator/.auths/credential-raw.json"
	auth := &coreauth.Auth{
		ID: rawID, Provider: "opencode", RouteChannel: "opencode", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "api_key"},
	}
	_, err := bootstrapInventoryModels([]registry.RegisteredRouteSnapshot{{
		ClientID: rawID, RouteChannel: "opencode", RuntimeModelID: "later-model", Model: &registry.ModelInfo{ID: "later-model"},
	}}, map[string]*coreauth.Auth{rawID: auth}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "registered route 0 has no canonical quota domain") {
		t.Fatalf("bootstrapInventoryModels() error = %v, want the quota-domain failure", err)
	}
	if strings.Contains(err.Error(), rawID) || !strings.Contains(err.Error(), coreauth.CredentialReferenceID(rawID)) {
		t.Fatalf("quota-domain error = %q, want the hashed credential reference only", err)
	}
}
