package openai

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"
)

func responsesHTTPStoreTestState() responsesHTTPContinuation {
	return responsesHTTPContinuation{Input: []byte(`[{"role":"user","content":"hello"}]`), Output: []byte(`[{"type":"message","content":[]}]`), AuthID: "auth", Model: "model"}
}

func TestResponsesHTTPContinuationStoreIsolationAndOwnership(t *testing.T) {
	var store responsesHTTPContinuationStore
	state := responsesHTTPStoreTestState()
	wantInput, wantOutput := bytes.Clone(state.Input), bytes.Clone(state.Output)
	if !store.save("caller-a", "response", state) {
		t.Fatal("save failed")
	}
	state.Input[0], state.Output[0] = 'x', 'x'
	got, ok := store.load("caller-a", "response")
	if !ok || !bytes.Equal(got.Input, wantInput) || !bytes.Equal(got.Output, wantOutput) {
		t.Fatal("save did not preserve owned snapshots")
	}
	if _, ok := store.load("caller-b", "response"); ok {
		t.Fatal("snapshot crossed caller scope")
	}
	other := responsesHTTPStoreTestState()
	other.AuthID = "other-auth"
	if !store.save("caller-b", "response", other) {
		t.Fatal("same response ID in independent scope rejected")
	}
	var independent responsesHTTPContinuationStore
	if _, ok := independent.load("caller-a", "response"); ok {
		t.Fatal("snapshot crossed store instances")
	}
}

func TestResponsesHTTPContinuationStoreTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	store := responsesHTTPContinuationStore{now: func() time.Time { return now }}
	state := responsesHTTPStoreTestState()
	if !store.save("scope", "old", state) {
		t.Fatal("save failed")
	}
	now = now.Add(responsesHTTPContinuationTTL - time.Nanosecond)
	if _, ok := store.load("scope", "old"); !ok {
		t.Fatal("expired before exact TTL boundary")
	}
	if !store.save("scope", "old", state) {
		t.Fatal("idempotent save failed")
	}
	now = now.Add(time.Nanosecond)
	if _, ok := store.load("scope", "old"); ok {
		t.Fatal("idempotent save extended absolute TTL")
	}
	if store.bytes != 0 || len(store.entries) != 0 {
		t.Fatal("expired entry retained accounting")
	}
	if !store.save("scope", "old", state) {
		t.Fatal("expired ID could not be reused")
	}
	now = now.Add(responsesHTTPContinuationTTL)
	if !store.save("scope", "new", state) || len(store.entries) != 1 {
		t.Fatal("write did not sweep expired entries")
	}
}

func TestResponsesHTTPContinuationStoreValidationAndCollisions(t *testing.T) {
	valid := responsesHTTPStoreTestState()
	cases := []struct {
		name   string
		id     string
		input  string
		output string
		auth   string
	}{
		{"empty-id", "", "[]", "[]", "auth"},
		{"empty-auth", "id", "[]", "[]", ""},
		{"null-input", "id", "null", "[]", "auth"},
		{"object-input", "id", "{}", "[]", "auth"},
		{"invalid-input", "id", "[", "[]", "auth"},
		{"null-output", "id", "[]", "null", "auth"},
		{"object-output", "id", "[]", "{}", "auth"},
		{"invalid-output", "id", "[]", "[false,]", "auth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var store responsesHTTPContinuationStore
			if store.save("scope", tc.id, responsesHTTPContinuation{Input: []byte(tc.input), Output: []byte(tc.output), AuthID: tc.auth}) || len(store.entries) != 0 || store.bytes != 0 {
				t.Fatal("invalid snapshot retained")
			}
		})
	}
	var store responsesHTTPContinuationStore
	if !store.save("scope", "id", valid) {
		t.Fatal("save failed")
	}
	for _, field := range []string{"auth", "model", "input"} {
		changed := responsesHTTPStoreTestState()
		switch field {
		case "auth":
			changed.AuthID = "different"
		case "model":
			changed.Model = "different"
		case "input":
			changed.Input = []byte(`[]`)
		}
		if store.save("scope", "id", changed) {
			t.Fatalf("ambiguous %s collision accepted", field)
		}
	}
	got, ok := store.load("scope", "id")
	if !ok || !bytes.Equal(got.Input, valid.Input) || got.AuthID != valid.AuthID || got.Model != valid.Model {
		t.Fatal("collision poisoned previous snapshot")
	}
	if !store.save("scope", "whitespace", responsesHTTPContinuation{Input: []byte(" \n[]\t"), Output: []byte(" [] "), AuthID: "auth"}) {
		t.Fatal("valid whitespace-surrounded arrays rejected")
	}
}

func TestResponsesHTTPContinuationStoreBounds(t *testing.T) {
	state := responsesHTTPContinuation{Input: []byte("[]"), Output: []byte("[]"), AuthID: "a", Model: "m"}
	// Each entry has exactly eight payload/metadata bytes: s + ID + [] + [] + a + m.
	const size = 8
	t.Run("entry-count-and-order", func(t *testing.T) {
		now := time.Unix(1000, 0)
		store := responsesHTTPContinuationStore{now: func() time.Time { return now }, maxEntries: 2}
		for _, id := range []string{"a", "b", "c"} {
			if !store.save("s", id, state) {
				t.Fatal("save failed")
			}
		}
		if _, ok := store.load("s", "a"); ok {
			t.Fatal("oldest entry survived eviction")
		}
		for _, id := range []string{"b", "c"} {
			if _, ok := store.load("s", id); !ok {
				t.Fatalf("entry %s evicted out of order", id)
			}
		}
	})
	t.Run("byte-budget", func(t *testing.T) {
		store := responsesHTTPContinuationStore{maxBytes: 2 * size}
		for _, id := range []string{"a", "b", "c"} {
			if !store.save("s", id, state) {
				t.Fatal("save failed")
			}
		}
		if len(store.entries) != 2 || store.bytes != 2*size {
			t.Fatal("byte accounting or eviction incorrect")
		}
		if _, ok := store.load("s", "a"); ok {
			t.Fatal("oldest entry survived byte eviction")
		}
	})
	t.Run("entry-budget-includes-metadata", func(t *testing.T) {
		store := responsesHTTPContinuationStore{maxEntryBytes: size}
		if !store.save("s", "a", state) {
			t.Fatal("exact entry limit rejected")
		}
		if store.save("s", "long-id", state) {
			t.Fatal("metadata excluded from entry limit")
		}
		if len(store.entries) != 1 || store.bytes != size {
			t.Fatal("rejected state changed accounting")
		}
	})
	t.Run("total-smaller-than-entry-limit", func(t *testing.T) {
		store := responsesHTTPContinuationStore{maxBytes: size - 1, maxEntryBytes: size + 1}
		if store.save("s", "a", state) || len(store.entries) != 0 {
			t.Fatal("entry larger than total budget accepted")
		}
	})
	t.Run("replacement-accounting-and-age", func(t *testing.T) {
		now := time.Unix(1000, 0)
		store := responsesHTTPContinuationStore{now: func() time.Time { return now }, maxBytes: 2*size + 4, ttl: time.Minute}
		if !store.save("s", "a", state) || !store.save("s", "b", state) {
			t.Fatal("save failed")
		}
		now = now.Add(30 * time.Second)
		changed := state
		changed.Output = []byte("[null]")
		if !store.save("s", "a", changed) || store.bytes != 2*size+4 {
			t.Fatal("replacement accounting incorrect")
		}
		if !store.save("s", "c", state) {
			t.Fatal("save failed")
		}
		if _, ok := store.load("s", "a"); ok {
			t.Fatal("replacement refreshed insertion order")
		}
		now = now.Add(30 * time.Second)
		if _, ok := store.load("s", "b"); ok {
			t.Fatal("original entry did not expire")
		}
		if _, ok := store.load("s", "c"); !ok {
			t.Fatal("new entry expired too early")
		}
	})
}

func TestResponsesHTTPContinuationStoreForksAndConcurrentSnapshots(t *testing.T) {
	var store responsesHTTPContinuationStore
	root := responsesHTTPStoreTestState()
	if !store.save("s", "root", root) {
		t.Fatal("root save failed")
	}
	parent, _ := store.load("s", "root")
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			for turn := range 8 {
				id := fmt.Sprintf("fork-%d-%d", i, turn)
				input := []byte(fmt.Sprintf(`[{"role":"user","content":"branch-%d-%d"}]`, i, turn))
				state := responsesHTTPContinuation{Input: input, Output: bytes.Clone(parent.Output), AuthID: parent.AuthID, Model: parent.Model}
				if !store.save("s", id, state) {
					t.Errorf("save %s failed", id)
					return
				}
				input[0], state.Output[0] = 'x', 'x'
				got, ok := store.load("s", id)
				if !ok || !responsesHTTPContinuationArray(got.Input) || !bytes.Equal(got.Output, parent.Output) {
					t.Errorf("snapshot %s corrupted", id)
					return
				}
				current, ok := store.load("s", "root")
				if !ok || !bytes.Equal(current.Input, root.Input) || !bytes.Equal(current.Output, root.Output) {
					t.Error("fork modified root")
					return
				}
			}
		}(i)
	}
	workers.Wait()
	if len(store.entries) != 257 {
		t.Fatalf("lost fork snapshots: got %d", len(store.entries))
	}
}

func TestResponsesHTTPContinuationStoreReplacementDoesNotExtendTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	store := responsesHTTPContinuationStore{now: func() time.Time { return now }, ttl: time.Minute}
	state := responsesHTTPStoreTestState()
	if !store.save("scope", "id", state) {
		t.Fatal("save failed")
	}
	original, _ := store.load("scope", "id")
	now = now.Add(30 * time.Second)
	state.Output = []byte(`[{"type":"message","content":[{"type":"output_text","text":"changed"}]}]`)
	if !store.save("scope", "id", state) {
		t.Fatal("compatible replacement failed")
	}
	replaced, ok := store.load("scope", "id")
	if !ok || !bytes.Equal(replaced.Output, state.Output) {
		t.Fatal("replacement was not saved")
	}
	if !bytes.Equal(original.Output, responsesHTTPStoreTestState().Output) {
		t.Fatal("replacement mutated a previously loaded snapshot")
	}
	now = now.Add(30 * time.Second)
	if _, ok := store.load("scope", "unrelated"); ok {
		t.Fatal("unknown ID found")
	}
	if len(store.entries) != 0 || store.bytes != 0 {
		t.Fatal("lookup did not sweep all expired snapshots at original TTL boundary")
	}
}
