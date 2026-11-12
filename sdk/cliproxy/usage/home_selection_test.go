package usage

import "testing"

func TestHomeSelectionContextAndPublishSnapshot(t *testing.T) {
	ctx := WithHomeSelection(t.Context(), HomeSelection{Model: "route"})
	followup := HomeSelectionFromContext(ctx)
	followup.Reused = true
	if HomeSelectionFromContext(ctx).Reused {
		t.Fatal("follow-up mutated the first turn")
	}
	mgr := NewManager(10)
	t.Cleanup(mgr.Stop)
	plugin := &captureUsagePlugin{records: make(chan Record, 2)}
	mgr.Register(plugin)
	mgr.Publish(ctx, Record{})
	mgr.Publish(ctx, Record{HomeSelection: followup})
	first, second := <-plugin.records, <-plugin.records
	if first.HomeSelection == nil || first.HomeSelection.Reused || second.HomeSelection == nil || !second.HomeSelection.Reused {
		t.Fatalf("lost attempt metadata: first=%+v second=%+v", first.HomeSelection, second.HomeSelection)
	}
}
