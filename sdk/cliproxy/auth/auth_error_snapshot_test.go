package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestManagerAuthErrorSnapshotsDoNotChangeRetryEligibility(t *testing.T) {
	for _, boundary := range []string{"registration input", "registration result", "GetByID", "List"} {
		t.Run(boundary, func(t *testing.T) {
			want := Error{Code: "unauthorized", Message: "credential rejected", HTTPStatus: http.StatusUnauthorized}
			initial := want
			auth := &Auth{ID: t.Name(), Provider: "codex", Status: StatusError, Unavailable: true, LastError: &initial, NextRetryAfter: time.Now().Add(time.Hour)}
			manager := NewManager(nil, nil, nil)
			registered, err := manager.Register(t.Context(), auth)
			if err != nil {
				t.Fatal(err)
			}
			if _, retry := manager.closestCooldownWait([]string{"codex"}, "", 0, authSelectionEligibility{}, "", 1); retry {
				t.Fatal("401 control unexpectedly permits a credential retry round")
			}
			var snapshot *Auth
			switch boundary {
			case "registration input":
				snapshot = auth
			case "registration result":
				snapshot = registered
			case "GetByID":
				snapshot, _ = manager.GetByID(auth.ID)
			case "List":
				snapshot = manager.List()[0]
			}
			if snapshot == nil || snapshot.LastError == nil {
				t.Fatal("missing error snapshot")
			}
			// A caller may annotate its own diagnostic without updating the credential.
			*snapshot.LastError = Error{Code: "display-only", Message: "temporary error", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
			fresh, ok := manager.GetByID(auth.ID)
			if !ok || fresh.LastError == nil || *fresh.LastError != want {
				t.Errorf("caller mutation changed the manager error: %+v, want %+v", fresh.LastError, want)
			}
			if wait, retry := manager.closestCooldownWait([]string{"codex"}, "", 0, authSelectionEligibility{}, "", 1); retry {
				t.Errorf("caller mutation enabled a credential retry round with wait %v", wait)
			}
		})
	}
}

func TestAuthCloneKeepsAbsentLastErrorNil(t *testing.T) {
	if cloned := (&Auth{ID: "healthy"}).Clone(); cloned.LastError != nil {
		t.Fatalf("healthy clone acquired an error: %+v", cloned.LastError)
	}
}
