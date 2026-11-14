package cliproxy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRoutingPublicationPreservesAccountConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	initial := []byte("# existing account setup\nport: 8317\napi-keys:\n  - existing-client-key\n")
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := internalconfig.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: cfg, configPath: path, coreManager: coreauth.NewManager(nil, nil, coreauth.NoopHook{})}
	projection := &modelrouting.Config{
		SchemaVersion: modelrouting.SchemaVersion, Generation: 1, SnapshotDigest: modelrouting.ConfigDigest([]byte("snapshot")),
		Aliases: []modelrouting.Alias{}, DirectModels: []modelrouting.DirectModel{},
		FailurePolicy: modelrouting.FailurePolicy{Mode: "classified_candidate_failover", CredentialAcquisitionTimeoutSeconds: 120, AutomaticFailover: true, PreserveFirstError: true, TerminateOwnedRequestOnCancel: true, FailoverRules: []modelrouting.FailoverRule{{RuleID: "capacity", HTTPStatuses: []int{429}, ErrorCodes: []string{"rate_limit"}, FailureKinds: []modelrouting.FailureKind{modelrouting.FailureKindCredential}}}},
	}
	projection.ProjectionDigest, err = modelrouting.ProjectionDigest(projection)
	if err != nil {
		t.Fatal(err)
	}
	body, err := modelrouting.RoutingYAML(projection)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = svc.PublishRouting(ctx, append([]byte("api-keys: [replacement]\n"), body...), nil); !errors.Is(err, modelrouting.ErrInvalidPublication) {
		t.Fatalf("mixed payload error = %v", err)
	}
	receipt, err := svc.PublishRouting(ctx, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(saved, []byte("existing-client-key")) || !bytes.Contains(saved, []byte("# existing account setup")) {
		t.Fatal("publication lost the accounts or their comments")
	}
	if receipt.Active.ConfigDigest != modelrouting.ConfigDigest(body) {
		t.Fatal("receipt does not bind the active routing resource")
	}
	if _, err := svc.PublishRouting(ctx, body, nil); !errors.Is(err, modelrouting.ErrCASConflict) {
		t.Fatalf("duplicate bootstrap error = %v", err)
	}
	projection.Generation++
	projection.ProjectionDigest, err = modelrouting.ProjectionDigest(projection)
	if err != nil {
		t.Fatal(err)
	}
	nextBody, err := modelrouting.RoutingYAML(projection)
	if err != nil {
		t.Fatal(err)
	}
	projection.SnapshotDigest = modelrouting.ConfigDigest([]byte("competing snapshot"))
	projection.ProjectionDigest, err = modelrouting.ProjectionDigest(projection)
	if err != nil {
		t.Fatal(err)
	}
	competingBody, err := modelrouting.RoutingYAML(projection)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, candidate := range [][]byte{nextBody, competingBody} {
		go func() {
			_, publishErr := svc.PublishRouting(ctx, candidate, &receipt.Active)
			results <- publishErr
		}()
	}
	passed, conflicts := 0, 0
	for range 2 {
		switch publishErr := <-results; {
		case publishErr == nil:
			passed++
		case errors.Is(publishErr, modelrouting.ErrCASConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", publishErr)
		}
	}
	if passed != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS passed=%d conflicts=%d", passed, conflicts)
	}
	active := svc.RoutingState()
	if active.Identity == nil || active.Identity.Generation != 2 {
		t.Fatal("runtime does not attest to the winning generation")
	}

	inventory, err := svc.RoutingInventory()
	if err != nil || inventory.Active == nil || *inventory.Active != *active.Identity {
		t.Fatalf("inventory did not attest the winner: %v", err)
	}
	restarted := &Service{configPath: path, coreManager: coreauth.NewManager(nil, nil, coreauth.NoopHook{})}
	persisted, err := internalconfig.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.initializeRoutingPublication(persisted.ModelRouting); err != nil {
		t.Fatal(err)
	}
	restored := restarted.RoutingState()
	if restored.Identity == nil || *restored.Identity != *active.Identity {
		t.Fatal("disk and live identities differ after restart")
	}

}
