package auth

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestParseModelWeights_NormalizesKeysAndValues(t *testing.T) {
	t.Parallel()

	got, err := ParseModelWeights(map[string]any{
		"Claude-Fable-5-1(8192)": float64(7),
		"claude-opus-5":          "3",
		"claude-sonnet-5":        float64(0),
		"claude-haiku-4-5":       float64(-4),
	})
	if err != nil {
		t.Fatalf("ParseModelWeights() error = %v", err)
	}
	want := map[string]int64{"claude-fable-5-1": 7, "claude-opus-5": 3, "claude-sonnet-5": 0, "claude-haiku-4-5": 0}
	if len(got) != len(want) {
		t.Fatalf("ParseModelWeights() = %#v, want %#v", got, want)
	}
	for key, weight := range want {
		if got[key] != weight {
			t.Fatalf("ParseModelWeights()[%q] = %d, want %d", key, got[key], weight)
		}
	}
	if encoded := EncodeModelWeights(got); encoded != `{"claude-fable-5-1":7,"claude-haiku-4-5":0,"claude-opus-5":3,"claude-sonnet-5":0}` {
		t.Fatalf("EncodeModelWeights() = %s", encoded)
	}
	roundTrip, errRoundTrip := ParseModelWeights(EncodeModelWeights(got))
	if errRoundTrip != nil || len(roundTrip) != len(got) {
		t.Fatalf("round trip = %#v, %v", roundTrip, errRoundTrip)
	}
}

func TestParseModelWeights_RejectsInvalidShapes(t *testing.T) {
	t.Parallel()

	for _, input := range []any{
		"not-json",
		[]any{"claude-opus-5"},
		map[string]any{"claude-opus-5": 1.5},
		map[string]any{"claude-opus-5": float64(1_000_001)},
		map[string]any{"  ": float64(1)},
		map[string]any{"claude-opus-5": "abc"},
	} {
		if _, err := ParseModelWeights(input); err == nil {
			t.Fatalf("ParseModelWeights(%#v) expected error", input)
		}
	}
	for _, input := range []any{nil, "", "  ", map[string]any{}} {
		got, err := ParseModelWeights(input)
		if err != nil || got != nil {
			t.Fatalf("ParseModelWeights(%#v) = %#v, %v; want nil, nil", input, got, err)
		}
	}
}

func TestValidateAuthWeight_CoversModelWeights(t *testing.T) {
	t.Parallel()

	if err := ValidateAuthWeight(&Auth{Metadata: map[string]any{AttributeModelWeights: map[string]any{"claude-opus-5": 1.5}}}); err == nil {
		t.Fatal("ValidateAuthWeight(metadata) expected error for fractional model weight")
	}
	if err := ValidateAuthWeight(&Auth{Attributes: map[string]string{AttributeModelWeights: "{bad"}}); err == nil {
		t.Fatal("ValidateAuthWeight(attributes) expected error for malformed table")
	}
	if err := ValidateAuthWeight(&Auth{Metadata: map[string]any{AttributeModelWeights: map[string]any{"claude-opus-5": float64(2)}}}); err != nil {
		t.Fatalf("ValidateAuthWeight(valid) error = %v", err)
	}
}

func TestApplyAuthWeightMetadata_EncodesModelWeightsAttribute(t *testing.T) {
	t.Parallel()

	auth := &Auth{}
	err := ApplyAuthWeightMetadata(auth, map[string]any{
		AttributeWeight:       float64(4),
		AttributeModelWeights: map[string]any{"claude-opus-5": float64(9), "Claude-Fable-5-1": float64(0)},
	})
	if err != nil {
		t.Fatalf("ApplyAuthWeightMetadata() error = %v", err)
	}
	if auth.Attributes[AttributeWeight] != "4" {
		t.Fatalf("weight attribute = %q, want 4", auth.Attributes[AttributeWeight])
	}
	if auth.Attributes[AttributeModelWeights] != `{"claude-fable-5-1":0,"claude-opus-5":9}` {
		t.Fatalf("model_weights attribute = %q", auth.Attributes[AttributeModelWeights])
	}

	// An explicit empty table clears the attribute so a stale override cannot linger.
	if err := ApplyAuthWeightMetadata(auth, map[string]any{AttributeModelWeights: map[string]any{}}); err != nil {
		t.Fatalf("ApplyAuthWeightMetadata(empty) error = %v", err)
	}
	if _, ok := auth.Attributes[AttributeModelWeights]; ok {
		t.Fatalf("model_weights attribute should be removed, got %q", auth.Attributes[AttributeModelWeights])
	}
}

func TestAuthWeightForModel_OverrideAndFallback(t *testing.T) {
	t.Parallel()

	auth := &Auth{Attributes: map[string]string{
		AttributeWeight:       "5",
		AttributeModelWeights: `{"claude-fable-5-1":1,"claude-opus-5":0}`,
	}}
	if got := authWeightForModel(auth, "claude-fable-5-1"); got != 1 {
		t.Fatalf("authWeightForModel(fable) = %d, want 1", got)
	}
	if got := authWeightForModel(auth, "Claude-Fable-5-1(16384)"); got != 1 {
		t.Fatalf("authWeightForModel(fable thinking suffix, mixed case) = %d, want 1", got)
	}
	if got := authWeightForModel(auth, "claude-opus-5"); got != 0 {
		t.Fatalf("authWeightForModel(opus) = %d, want 0", got)
	}
	if got := authWeightForModel(auth, "claude-sonnet-5"); got != 5 {
		t.Fatalf("authWeightForModel(unlisted) = %d, want account weight 5", got)
	}
	if got := authWeightForModel(auth, ""); got != 5 {
		t.Fatalf("authWeightForModel(empty model) = %d, want account weight 5", got)
	}
	// Metadata form (no synthesizer) is honored too, attribute form wins when both exist.
	metaAuth := &Auth{Metadata: map[string]any{AttributeWeight: float64(2), AttributeModelWeights: map[string]any{"claude-opus-5": float64(8)}}}
	if got := authWeightForModel(metaAuth, "claude-opus-5"); got != 8 {
		t.Fatalf("authWeightForModel(metadata) = %d, want 8", got)
	}
	metaAuth.Attributes = map[string]string{AttributeModelWeights: `{"claude-opus-5":3}`}
	if got := authWeightForModel(metaAuth, "claude-opus-5"); got != 3 {
		t.Fatalf("authWeightForModel(attribute over metadata) = %d, want 3", got)
	}
	// Without a table the behaviour is identical to authWeight.
	plain := &Auth{Attributes: map[string]string{AttributeWeight: "7"}}
	if got := authWeightForModel(plain, "claude-opus-5"); got != authWeight(plain) {
		t.Fatalf("authWeightForModel(no table) = %d, want %d", got, authWeight(plain))
	}
}

func pickWeightedCounts(t *testing.T, selector Selector, model string, auths []*Auth, rounds int) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for index := 0; index < rounds; index++ {
		picked, errPick := selector.Pick(context.Background(), "claude", model, cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick(%s) #%d error = %v", model, index, errPick)
		}
		if picked == nil {
			t.Fatalf("Pick(%s) #%d returned nil", model, index)
		}
		counts[picked.ID]++
	}
	return counts
}

func TestWeightedRoundRobinSelectorPick_UsesPerModelWeights(t *testing.T) {
	t.Parallel()

	authA := &Auth{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeModelWeights: `{"claude-fable-5-1":1,"claude-opus-5":10000}`}}
	authB := &Auth{ID: "b", Provider: "claude", Attributes: map[string]string{AttributeModelWeights: `{"claude-fable-5-1":10000,"claude-opus-5":1}`}}
	auths := []*Auth{authA, authB}
	selector := &WeightedRoundRobinSelector{}

	fable := pickWeightedCounts(t, selector, "claude-fable-5-1", auths, 20)
	if fable["b"] < 19 {
		t.Fatalf("fable picks = %#v, want b >= 19", fable)
	}
	opus := pickWeightedCounts(t, selector, "claude-opus-5", auths, 20)
	if opus["a"] < 19 {
		t.Fatalf("opus picks = %#v, want a >= 19", opus)
	}
	// Thinking-suffix variants share the same per-model weight.
	fableThinking := pickWeightedCounts(t, selector, "claude-fable-5-1(8192)", auths, 20)
	if fableThinking["b"] < 19 {
		t.Fatalf("fable(8192) picks = %#v, want b >= 19", fableThinking)
	}
}

func TestWeightedRoundRobinSelectorPick_ModelWeightsFallBackToAccountWeight(t *testing.T) {
	t.Parallel()

	authA := &Auth{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1", AttributeModelWeights: `{"claude-fable-5-1":3}`}}
	authC := &Auth{ID: "c", Provider: "claude", Attributes: map[string]string{AttributeWeight: "3"}}
	auths := []*Auth{authA, authC}
	selector := &WeightedRoundRobinSelector{}

	// Model listed for A: A=3, C=3 (account weight) -> even split.
	fable := pickWeightedCounts(t, selector, "claude-fable-5-1", auths, 60)
	if fable["a"] != 30 || fable["c"] != 30 {
		t.Fatalf("fable picks = %#v, want 30/30", fable)
	}
	// Model not listed for A: A=1 (account), C=3 -> 1:3.
	sonnet := pickWeightedCounts(t, selector, "claude-sonnet-5", auths, 60)
	if sonnet["a"] != 15 || sonnet["c"] != 45 {
		t.Fatalf("sonnet picks = %#v, want 15/45", sonnet)
	}
}

func TestWeightedRoundRobinSelectorPick_ZeroModelWeightExcludesOnlyThatModel(t *testing.T) {
	t.Parallel()

	authA := &Auth{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeWeight: "5", AttributeModelWeights: `{"claude-fable-5-1":0}`}}
	authB := &Auth{ID: "b", Provider: "claude", Attributes: map[string]string{AttributeWeight: "5"}}
	auths := []*Auth{authA, authB}
	selector := &WeightedRoundRobinSelector{}

	fable := pickWeightedCounts(t, selector, "claude-fable-5-1", auths, 20)
	if fable["a"] != 0 || fable["b"] != 20 {
		t.Fatalf("fable picks = %#v, want a excluded", fable)
	}
	opus := pickWeightedCounts(t, selector, "claude-opus-5", auths, 20)
	if opus["a"] != 10 || opus["b"] != 10 {
		t.Fatalf("opus picks = %#v, want 10/10", opus)
	}

	// All candidates zero for the model -> auth_unavailable, other models unaffected.
	authB.Attributes[AttributeModelWeights] = `{"claude-fable-5-1":0}`
	if _, errPick := selector.Pick(context.Background(), "claude", "claude-fable-5-1", cliproxyexecutor.Options{}, auths); errPick == nil {
		t.Fatal("Pick(fable) expected error when every credential is zero for the model")
	}
	if _, errPick := selector.Pick(context.Background(), "claude", "claude-opus-5", cliproxyexecutor.Options{}, auths); errPick != nil {
		t.Fatalf("Pick(opus) error = %v", errPick)
	}
}

func TestWeightedRoundRobinSelectorPick_NoModelWeightsMatchesLegacyBehaviour(t *testing.T) {
	t.Parallel()

	build := func() []*Auth {
		return []*Auth{
			{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeWeight: "3"}},
			{ID: "b", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1"}},
			{ID: "c", Provider: "claude"},
		}
	}
	withEmptyTable := build()
	withEmptyTable[0].Attributes[AttributeModelWeights] = ""
	withEmptyTable[1].Metadata = map[string]any{AttributeModelWeights: map[string]any{}}

	plainSeq := make([]string, 0, 10)
	emptySeq := make([]string, 0, 10)
	plainSelector := &WeightedRoundRobinSelector{}
	emptySelector := &WeightedRoundRobinSelector{}
	for index := 0; index < 10; index++ {
		plain, errPlain := plainSelector.Pick(context.Background(), "claude", "claude-opus-5", cliproxyexecutor.Options{}, build())
		empty, errEmpty := emptySelector.Pick(context.Background(), "claude", "claude-opus-5", cliproxyexecutor.Options{}, withEmptyTable)
		if errPlain != nil || errEmpty != nil {
			t.Fatalf("Pick() errors = %v / %v", errPlain, errEmpty)
		}
		plainSeq = append(plainSeq, plain.ID)
		emptySeq = append(emptySeq, empty.ID)
	}
	if fmt.Sprint(plainSeq) != fmt.Sprint(emptySeq) {
		t.Fatalf("sequence with empty model_weights = %v, want identical to plain %v", emptySeq, plainSeq)
	}
	if fmt.Sprint(plainSeq) != "[a b a c a a b a c a]" {
		t.Fatalf("legacy sequence = %v", plainSeq)
	}
}

func TestSessionAffinitySelector_ZeroModelWeightBlocksNewBindingForThatModelOnly(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&WeightedRoundRobinSelector{})
	defer selector.Stop()
	authA := &Auth{ID: "auth-a", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1000000", AttributeModelWeights: `{"claude-fable-5-1":0}`}}
	authB := &Auth{ID: "auth-b", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1"}}
	auths := []*Auth{authA, authB}
	pick := func(model string, index int) *Auth {
		t.Helper()
		opts := cliproxyexecutor.Options{OriginalRequest: []byte(fmt.Sprintf(`{"session_id":"mw-session-%s-%d"}`, model, index))}
		picked, errPick := selector.Pick(context.Background(), "claude", model, opts, auths)
		if errPick != nil {
			t.Fatalf("Pick(%s #%d) error = %v", model, index, errPick)
		}
		return picked
	}
	for index := 0; index < 10; index++ {
		if got := pick("claude-fable-5-1", index); got.ID != authB.ID {
			t.Fatalf("fable new session #%d bound to %q, want %q (A is zero for fable)", index, got.ID, authB.ID)
		}
	}
	opusCounts := make(map[string]int)
	for index := 0; index < 10; index++ {
		opusCounts[pick("claude-opus-5", index).ID]++
	}
	if opusCounts[authA.ID] != 10 {
		t.Fatalf("opus new sessions = %#v, want all on auth-a", opusCounts)
	}
}

func TestSchedulerPick_WeightedRoundRobinUsesPerModelWeights(t *testing.T) {
	t.Parallel()

	reg := registry.GetGlobalRegistry()
	for _, authID := range []string{"mw-a", "mw-b", "mw-c"} {
		reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: "claude-fable-5-1"}, {ID: "claude-opus-5"}})
	}
	t.Cleanup(func() {
		for _, authID := range []string{"mw-a", "mw-b", "mw-c"} {
			reg.UnregisterClient(authID)
		}
	})
	authA := &Auth{ID: "mw-a", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1", AttributeModelWeights: `{"claude-fable-5-1":1,"claude-opus-5":10000}`}}
	authB := &Auth{ID: "mw-b", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1", AttributeModelWeights: `{"claude-fable-5-1":10000,"claude-opus-5":1}`}}
	authC := &Auth{ID: "mw-c", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1", AttributeModelWeights: `{"claude-fable-5-1":0}`}}
	scheduler := newSchedulerForTest(&WeightedRoundRobinSelector{}, authA, authB, authC)

	count := func(model string, rounds int) map[string]int {
		t.Helper()
		counts := make(map[string]int)
		for index := 0; index < rounds; index++ {
			got, errPick := scheduler.pickSingle(context.Background(), "claude", model, cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickSingle(%s) #%d error = %v", model, index, errPick)
			}
			counts[got.ID]++
		}
		return counts
	}
	fable := count("claude-fable-5-1", 20)
	if fable["mw-b"] < 19 || fable["mw-c"] != 0 {
		t.Fatalf("scheduler fable picks = %#v, want b >= 19 and c excluded", fable)
	}
	opus := count("claude-opus-5", 20)
	if opus["mw-a"] < 19 {
		t.Fatalf("scheduler opus picks = %#v, want a >= 19", opus)
	}
	// c has no opus entry, so its account weight (1) applies: over one full smooth-WRR
	// cycle (10000+1+1 picks) it must surface exactly once, proving it was not excluded.
	opusCycle := count("claude-opus-5", 10002)
	if opusCycle["mw-c"] != 1 || opusCycle["mw-b"] != 1 || opusCycle["mw-a"] != 10000 {
		t.Fatalf("scheduler opus full cycle = %#v, want a:10000 b:1 c:1", opusCycle)
	}

	// Hot update: flip A's fable weight and the shard must follow without a rebuild.
	authA.Attributes[AttributeModelWeights] = `{"claude-fable-5-1":10000,"claude-opus-5":10000}`
	scheduler.upsertAuth(authA)
	authB.Attributes[AttributeModelWeights] = `{"claude-fable-5-1":1,"claude-opus-5":1}`
	scheduler.upsertAuth(authB)
	fableAfter := count("claude-fable-5-1", 20)
	if fableAfter["mw-a"] < 19 {
		t.Fatalf("scheduler fable picks after update = %#v, want a >= 19", fableAfter)
	}
}
