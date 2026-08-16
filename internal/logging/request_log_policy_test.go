package logging

import "testing"

func TestRequestLogPolicyNilReceiverFailsClosed(t *testing.T) {
	var policy *RequestLogPolicy
	if !policy.ShouldSkipLog("credential") {
		t.Fatal("nil policy must suppress an authenticated request")
	}
	if policy.ShouldSkipLog("") {
		t.Fatal("nil policy must not suppress a request without a credential")
	}
}

func TestRequestLogPolicyReloadTransactionCommitAndRollback(t *testing.T) {
	policy := NewRequestLogPolicy([]string{"old-key"})
	reloadRollback := policy.BeginReload([]string{"new-key"})
	if !policy.ShouldSkipLog("old-key") || !policy.ShouldSkipLog("new-key") {
		t.Fatal("reload window must protect the union of old and candidate keys")
	}
	reloadRollback.Rollback()
	if !policy.ShouldSkipLog("old-key") || policy.ShouldSkipLog("new-key") {
		t.Fatal("failed reload did not restore the previous key set")
	}

	reloadCommit := policy.BeginReload([]string{"new-key"})
	if !reloadCommit.Commit() {
		t.Fatal("Commit() = false")
	}
	if policy.ShouldSkipLog("old-key") || !policy.ShouldSkipLog("new-key") {
		t.Fatal("successful reload did not publish the candidate key set")
	}
}

func TestRequestLogPolicyReloadFailClosedRetainsUnionUntilLaterCommit(t *testing.T) {
	policy := NewRequestLogPolicy([]string{"old-key"})
	failedReload := policy.BeginReload([]string{"candidate-key"})
	if !policy.ShouldSkipLog("old-key") || !policy.ShouldSkipLog("candidate-key") {
		t.Fatal("reload window must protect the union of old and candidate keys")
	}
	failedReload.FailClosed()
	if !policy.ShouldSkipLog("old-key") || !policy.ShouldSkipLog("candidate-key") {
		t.Fatal("failed reload completion did not retain the protected union")
	}

	latestReload := policy.BeginReload([]string{"latest-key"})
	if !latestReload.Commit() {
		t.Fatal("latest Commit() = false")
	}
	if policy.ShouldSkipLog("old-key") || policy.ShouldSkipLog("candidate-key") ||
		!policy.ShouldSkipLog("latest-key") {
		t.Fatal("successful reload did not replace the retained union with the latest candidate set")
	}
}

func TestRequestLogPolicyReloadFailClosedIgnoresSupersededGeneration(t *testing.T) {
	policy := NewRequestLogPolicy([]string{"old-key"})
	firstReload := policy.BeginReload([]string{"first-key"})
	secondReload := policy.BeginReload([]string{"second-key"})

	firstReload.FailClosed()
	if !policy.ShouldSkipLog("old-key") || !policy.ShouldSkipLog("first-key") ||
		!policy.ShouldSkipLog("second-key") {
		t.Fatal("superseded fail-closed completion mutated the active reload union")
	}
	if !secondReload.Commit() {
		t.Fatal("superseding Commit() = false")
	}
	if policy.ShouldSkipLog("old-key") || policy.ShouldSkipLog("first-key") ||
		!policy.ShouldSkipLog("second-key") {
		t.Fatal("superseding reload did not publish its candidate set")
	}
}
