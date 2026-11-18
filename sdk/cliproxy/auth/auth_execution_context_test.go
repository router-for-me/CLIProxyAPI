package auth

import (
	"context"
	"testing"
	"time"
)

func TestAuthExecutionStateInheritance(t *testing.T) {
	t.Run("request-precedence-and-parent-lifetime", func(t *testing.T) {
		parentValue := &struct{ label string }{"parent"}
		requestValue := &struct{ label string }{"request"}
		parent, cancel := context.WithDeadline(context.WithValue(context.Background(), authUseContextKey{}, "parent-value"), time.Now().Add(time.Minute))
		defer cancel()
		parent = WithAuthExecutionState(parent, parentValue)
		request, cancelRequest := context.WithCancel(WithAuthExecutionState(context.Background(), requestValue))
		defer cancelRequest()
		inherited := InheritAuthExecutionState(parent, request)
		deadline, hasDeadline := inherited.Deadline()
		parentDeadline, _ := parent.Deadline()
		if AuthExecutionState(inherited) != requestValue || inherited.Value(authUseContextKey{}) != "parent-value" || !hasDeadline || !deadline.Equal(parentDeadline) {
			t.Fatal("state inheritance lost precedence or parent context")
		}
		cancelRequest()
		if inherited.Err() != nil {
			t.Fatal("state inheritance changed parent cancellation")
		}
		cancel()
		if inherited.Err() != context.Canceled {
			t.Fatal("state inheritance detached parent cancellation")
		}
	})
	t.Run("absent-and-nil-contexts", func(t *testing.T) {
		parent := context.WithValue(context.Background(), authUseContextKey{}, "parent")
		if InheritAuthExecutionState(parent, nil) != parent || InheritAuthExecutionState(parent, context.Background()) != parent ||
			WithAuthExecutionState(parent, nil) != parent || WithAuthExecutionState(nil, nil) != nil || AuthExecutionState(nil) != nil || InheritAuthExecutionState(nil, nil) != nil {
			t.Fatal("absent policy state changed its parent")
		}
		state := &struct{ value bool }{true}
		if AuthExecutionState(WithAuthExecutionState(nil, state)) != state {
			t.Fatal("nil parent did not accept server-owned state")
		}
	})
}
