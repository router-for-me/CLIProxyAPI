package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestCopilotCatalogQueueCoalescesAndReplacesRotatedCredentials(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
	r.jobs = make(chan *copilotCatalogJob, 1)
	now := time.Now()
	a := registerCopilotCatalogTestAuth(t, m, now.Add(-copilot.CatalogMaxAge*3/4))
	for range 20 {
		r.OnAuthUpdated(context.Background(), a)
		r.reconcile(t.Context(), now)
	}
	if len(r.jobs) != 1 || len(r.inFlight) != 1 || len(r.wake) != 1 {
		t.Fatal("repeated notifications accumulated duplicate work")
	}
	old := <-r.jobs
	_, _, err := m.Modify(t.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
		current.Metadata["access_token"] = "rotated"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.reconcile(t.Context(), now)
	if old.ctx.Err() == nil {
		t.Fatal("rotation did not cancel stale discovery")
	}
	r.finish(old, now)
	r.reconcile(t.Context(), now)
	if len(r.jobs) != 1 {
		t.Fatal("old request backoff delayed new credentials")
	}
	next := <-r.jobs
	if next.identity.token != "rotated" {
		t.Fatal("replacement used stale token")
	}
	// A late duplicate completion must not discard the replacement job.
	r.finish(old, now)
	if r.inFlight[a.ID] != next {
		t.Fatal("stale completion discarded newer discovery")
	}
	m.Remove(t.Context(), a.ID)
	r.reconcile(t.Context(), now)
	if next.ctx.Err() == nil || len(r.states) != 0 {
		t.Fatal("removed account retained active discovery")
	}
}

func TestCopilotCatalogCommitPreservesConcurrentAuthEdits(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
	a := registerCopilotCatalogTestAuth(t, m, time.Now().Add(-copilot.CatalogMaxAge*3/4))
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
		_, _, err := m.Modify(t.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
			current.Metadata["note"] = "concurrent edit"
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: req}, nil
	}))
	r.reconcile(ctx, time.Now())
	job := <-r.jobs
	if err := r.fetch(job); err != nil {
		t.Fatal(err)
	}
	current, _ := m.GetByID(a.ID)
	if current.Metadata["note"] != "concurrent edit" || current.Metadata["access_token"] != a.Metadata["access_token"] || current.CredentialVersion != a.CredentialVersion {
		t.Fatal("catalog commit replaced concurrent edits or credential state")
	}
	raw, _ := current.Metadata[copilot.CatalogKey].(string)
	catalog := copilot.ReadCatalog(raw, time.Now())
	if catalog.FetchedAt.IsZero() || len(catalog.Models) != 0 {
		t.Fatal("new catalog was not committed")
	}
}
