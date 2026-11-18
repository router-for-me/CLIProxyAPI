package pluginhost

import (
	"context"
	"testing"
	"time"
)

func TestHostHTTPOperationParentCancellationCleansUp(t *testing.T) {
	bridge := newHostHTTPOperationBridge()
	parent, cancelParent := context.WithCancel(context.Background())
	operationID, entry, opened := bridge.open("plugin", nil, "", parent)
	if !opened {
		t.Fatal("failed to open operation")
	}
	cleanupDone := make(chan struct{})
	if !bridge.setCleanup("plugin", operationID, entry, func() { close(cleanupDone) }) {
		t.Fatal("failed to attach cleanup")
	}

	cancelParent()
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not clean up the operation")
	}
	if _, claimed := bridge.claim("plugin", nil, operationID, ""); claimed {
		t.Fatal("operation remained registered after parent cancellation")
	}
}

func TestHostHTTPOperationCancelIsPluginScoped(t *testing.T) {
	bridge := newHostHTTPOperationBridge()
	operationID, entry, opened := bridge.open("plugin-a", nil, "", context.Background())
	if !opened {
		t.Fatal("failed to open operation")
	}
	bridge.cancel("plugin-b", nil, operationID)
	if entry.ctx.Err() != nil {
		t.Fatalf("foreign plugin canceled operation: %v", entry.ctx.Err())
	}
	bridge.cancel("plugin-a", nil, operationID)
	if entry.ctx.Err() != context.Canceled {
		t.Fatalf("operation context error = %v, want context.Canceled", entry.ctx.Err())
	}
}

func TestHostHTTPOperationFinishDoesNotRemoveReusedID(t *testing.T) {
	bridge := newHostHTTPOperationBridge()
	oldID, oldEntry, openedOld := bridge.open("plugin", nil, "", context.Background())
	if !openedOld {
		t.Fatal("failed to open first operation")
	}
	bridge.cancel("plugin", nil, oldID)
	if oldEntry.ctx.Err() != context.Canceled {
		t.Fatalf("first operation context error = %v, want context.Canceled", oldEntry.ctx.Err())
	}
	newID, newEntry, openedNew := bridge.open("plugin", nil, "", context.Background())
	if !openedNew {
		t.Fatal("failed to open second operation")
	}
	if oldID == newID {
		t.Fatalf("operation ID was reused: %q", oldID)
	}
	bridge.finish("plugin", oldID, oldEntry)
	bridge.cancel("plugin", nil, newID)
	if newEntry.ctx.Err() != context.Canceled {
		t.Fatalf("new operation context error = %v, want context.Canceled", newEntry.ctx.Err())
	}
}

func TestHostHTTPOperationCloseInstanceBlocksLateOpen(t *testing.T) {
	bridge := newHostHTTPOperationBridge()
	oldInstance := &hostCallbackInstance{}
	operationID, entry, opened := bridge.open("plugin", oldInstance, "", context.Background())
	if !opened {
		t.Fatal("failed to open operation")
	}

	bridge.closeInstance("plugin", oldInstance)
	if entry.ctx.Err() != context.Canceled {
		t.Fatalf("operation context error = %v, want context.Canceled", entry.ctx.Err())
	}
	if _, _, openedLate := bridge.open("plugin", oldInstance, "", context.Background()); openedLate {
		t.Fatal("opened operation for a closed callback instance")
	}

	newInstance := &hostCallbackInstance{}
	newOperationID, _, openedNew := bridge.open("plugin", newInstance, "", context.Background())
	if !openedNew {
		t.Fatal("new callback instance could not open an operation")
	}
	bridge.cancel("plugin", newInstance, newOperationID)
	bridge.finish("plugin", operationID, entry)
}
