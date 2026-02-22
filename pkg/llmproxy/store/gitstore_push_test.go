package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
)

func TestIsNonFastForwardUpdateError(t *testing.T) {
	t.Parallel()

	if !isNonFastForwardUpdateError(git.ErrNonFastForwardUpdate) {
		t.Fatalf("expected ErrNonFastForwardUpdate to be detected")
	}
	if !isNonFastForwardUpdateError(errors.New("remote rejected: non-fast-forward update")) {
		t.Fatalf("expected textual non-fast-forward error to be detected")
	}
	if isNonFastForwardUpdateError(errors.New("some other push error")) {
		t.Fatalf("did not expect unrelated error to be detected")
	}
	if isNonFastForwardUpdateError(nil) {
		t.Fatalf("nil must not be detected as non-fast-forward")
	}
}

func TestBootstrapPullDivergedError(t *testing.T) {
	t.Parallel()

	err := bootstrapPullDivergedError(git.ErrNonFastForwardUpdate)
	if !errors.Is(err, ErrConcurrentGitWrite) {
		t.Fatalf("expected ErrConcurrentGitWrite wrapper, got: %v", err)
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "bootstrap pull diverged") {
		t.Fatalf("expected bootstrap divergence context, got: %s", err.Error())
	}
	if !strings.Contains(msg, "retry") {
		t.Fatalf("expected retry guidance in error message, got: %s", err.Error())
	}
}
