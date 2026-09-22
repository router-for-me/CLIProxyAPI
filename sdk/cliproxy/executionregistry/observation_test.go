package executionregistry

import (
	"testing"
	"time"
)

func TestFreezeInFlightWaitsForPendingBarrierAndCopiesScopes(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	registry.ObserveBarrier(14)

	before := registry.FreezeInFlight(time.Unix(12, 0).UTC())
	if before.BarrierRevision != 0 {
		t.Fatalf("barrier before install = %d", before.BarrierRevision)
	}

	scope, errInstall := registry.Install(pending, ScopeSpec{
		RequestID: "req-a", CredentialID: "cred", Model: "gpt-5",
		Kind: "http", StartedAt: time.Unix(10, 0).UTC(), Accounted: true,
	})
	if errInstall != nil {
		t.Fatal(errInstall)
	}

	after := registry.FreezeInFlight(time.Unix(13, 0).UTC())
	if after.BarrierRevision != 14 || len(after.Executions) != 1 || !after.Executions[0].Accounted {
		t.Fatalf("freeze after install = %#v", after)
	}
	after.Executions[0].RequestID = "mutated"

	copied := registry.FreezeInFlight(time.Unix(13, 0).UTC())
	if len(copied.Executions) != 1 || copied.Executions[0].RequestID != "req-a" {
		t.Fatalf("freeze did not copy scope = %#v", copied)
	}

	scope.End("completed")
	ended := registry.FreezeInFlight(time.Unix(14, 0).UTC())
	if len(ended.Executions) != 0 || ended.Revision <= after.Revision {
		t.Fatalf("freeze after end = %#v", ended)
	}
}
