package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestFillFirstSelectorPick_Deterministic(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "a" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "a")
	}
}

func TestRoundRobinSelectorPick_CyclesDeterministic(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	want := []string{"a", "b", "c", "a", "b"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
	}
}

func TestWeightedRoundRobinSelectorPick_DistributesAndSkipsNonPositiveWeights(t *testing.T) {
	t.Parallel()

	selector := &WeightedRoundRobinSelector{}
	auths := []*Auth{
		{ID: "a", Attributes: map[string]string{AttributeWeight: "5"}},
		{ID: "b", Attributes: map[string]string{AttributeWeight: "3"}},
		{ID: "c", Attributes: map[string]string{AttributeWeight: "2"}},
		{ID: "disabled-by-weight", Attributes: map[string]string{AttributeWeight: "0"}},
	}

	counts := make(map[string]int)
	for index := 0; index < 100; index++ {
		got, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick() #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	want := map[string]int{"a": 50, "b": 30, "c": 20}
	for authID, wantCount := range want {
		if counts[authID] != wantCount {
			t.Fatalf("auth %q picks = %d, want %d", authID, counts[authID], wantCount)
		}
	}
	if counts["disabled-by-weight"] != 0 {
		t.Fatalf("non-positive weight auth picks = %d, want 0", counts["disabled-by-weight"])
	}
}

func TestWeightedRoundRobinSelectorPick_ResetsCreditsWhenWeightsChange(t *testing.T) {
	t.Parallel()

	selector := &WeightedRoundRobinSelector{}
	authA := &Auth{ID: "a", Attributes: map[string]string{AttributeWeight: "1000000"}}
	authB := &Auth{ID: "b", Attributes: map[string]string{AttributeWeight: "1"}}
	auths := []*Auth{authA, authB}
	for index := 0; index < 1000; index++ {
		if _, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths); errPick != nil {
			t.Fatalf("warmup Pick() #%d error = %v", index, errPick)
		}
	}

	authA.Attributes[AttributeWeight] = "1"
	counts := make(map[string]int)
	for index := 0; index < 20; index++ {
		got, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick() after weight change #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	if counts["a"] != 10 || counts["b"] != 10 {
		t.Fatalf("picks after weight change = %#v, want a:b=10:10", counts)
	}
}

func TestWeightedRoundRobinSelectorPick_RebalancesWhenHighestWeightUnavailable(t *testing.T) {
	t.Parallel()

	selector := &WeightedRoundRobinSelector{}
	auths := []*Auth{
		{ID: "a", Disabled: true, Attributes: map[string]string{AttributeWeight: "5"}},
		{ID: "b", Attributes: map[string]string{AttributeWeight: "3"}},
		{ID: "c", Attributes: map[string]string{AttributeWeight: "2"}},
	}
	counts := make(map[string]int)
	for index := 0; index < 100; index++ {
		got, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick() #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	if counts["a"] != 0 || counts["b"] != 60 || counts["c"] != 40 {
		t.Fatalf("weighted failover counts = %#v, want b:c=60:40 with a skipped", counts)
	}
}

func TestWeightedRoundRobinSelectorPick_SkipsUnavailableAndQuotaExceededWithoutRecovery(t *testing.T) {
	t.Parallel()

	model := "test-model"
	selector := &WeightedRoundRobinSelector{}
	auths := []*Auth{
		{
			ID: "model-unavailable",
			ModelStates: map[string]*ModelState{
				model: {Unavailable: true},
			},
		},
		{ID: "quota-exceeded", Quota: QuotaState{Exceeded: true}},
		{ID: "available"},
	}

	gotModel, errModel := selector.Pick(context.Background(), "gemini", model, cliproxyexecutor.Options{}, auths)
	if errModel != nil || gotModel == nil || gotModel.ID != "available" {
		t.Fatalf("model Pick() = %#v, %v; want available", gotModel, errModel)
	}
	for index := 0; index < 4; index++ {
		gotAuth, errAuth := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
		if errAuth != nil || gotAuth == nil {
			t.Fatalf("auth Pick() #%d = %#v, %v; want available auth", index, gotAuth, errAuth)
		}
		if gotAuth.ID == "quota-exceeded" {
			t.Fatalf("auth Pick() #%d selected quota-exceeded credential", index)
		}
	}
}

func TestAuthWeight_MetadataFallbackAndAttributePrecedence(t *testing.T) {
	t.Parallel()

	if got := authWeight(&Auth{Metadata: map[string]any{AttributeWeight: float64(7)}}); got != 7 {
		t.Fatalf("authWeight(metadata) = %d, want 7", got)
	}
	if got := authWeight(&Auth{
		Attributes: map[string]string{AttributeWeight: "3"},
		Metadata:   map[string]any{AttributeWeight: float64(7)},
	}); got != 3 {
		t.Fatalf("authWeight(attribute and metadata) = %d, want attribute weight 3", got)
	}
}

func TestAuthWeight_InvalidAndOverflowValuesAreExcluded(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"1.5", "1000001", "9223372036854775807", "9223372036854775808"} {
		auth := &Auth{Attributes: map[string]string{AttributeWeight: raw}}
		if got := authWeight(auth); got != 0 {
			t.Fatalf("authWeight(%q) = %d, want 0", raw, got)
		}
	}
	if got := authWeight(&Auth{Metadata: map[string]any{AttributeWeight: 1.5}}); got != 0 {
		t.Fatalf("authWeight(invalid metadata) = %d, want 0", got)
	}
	if got := authWeight(&Auth{Attributes: map[string]string{AttributeWeight: "-1"}}); got != 0 {
		t.Fatalf("authWeight(-1) = %d, want 0", got)
	}
}

func TestPickSmoothWeightedAuth_SaturatesCorruptState(t *testing.T) {
	t.Parallel()

	current := map[string]int64{"a": math.MaxInt64, "b": math.MinInt64}
	picked := pickSmoothWeightedAuth([]*Auth{{ID: "a"}, {ID: "b"}}, current)
	if picked == nil {
		t.Fatal("pickSmoothWeightedAuth() returned nil")
	}
	if current["a"] != math.MaxInt64-2 || current["b"] != math.MinInt64+1 {
		t.Fatalf("current state = %#v, want saturated arithmetic", current)
	}
}

func TestWeightedRoundRobinSelectorPick_RecoveredAuthReturnsWithoutAccumulatedCredit(t *testing.T) {
	t.Parallel()

	selector := &WeightedRoundRobinSelector{}
	authA := &Auth{ID: "a", Attributes: map[string]string{AttributeWeight: "5"}}
	authB := &Auth{ID: "b", Attributes: map[string]string{AttributeWeight: "1"}}
	auths := []*Auth{authA, authB}

	for index := 0; index < 6; index++ {
		if _, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths); errPick != nil {
			t.Fatalf("warmup Pick() #%d error = %v", index, errPick)
		}
	}
	authA.Unavailable = true
	authA.NextRetryAfter = time.Now().Add(time.Hour)
	for index := 0; index < 6; index++ {
		got, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths)
		if errPick != nil || got == nil || got.ID != "b" {
			t.Fatalf("unavailable Pick() #%d = %#v, %v; want b", index, got, errPick)
		}
	}
	authA.Unavailable = false
	authA.NextRetryAfter = time.Time{}

	counts := make(map[string]int)
	for index := 0; index < 6; index++ {
		got, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("recovered Pick() #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	if counts["a"] != 5 || counts["b"] != 1 {
		t.Fatalf("recovered picks = %#v, want a:b=5:1", counts)
	}
}

func TestWeightedRoundRobinSelectorPick_DefaultWeightIsOne(t *testing.T) {
	t.Parallel()

	selector := &WeightedRoundRobinSelector{}
	auths := []*Auth{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	counts := make(map[string]int)
	for index := 0; index < 30; index++ {
		got, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick() #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	for _, authID := range []string{"a", "b", "c"} {
		if counts[authID] != 10 {
			t.Fatalf("auth %q picks = %d, want 10", authID, counts[authID])
		}
	}
}

func TestRoundRobinSelectorPick_PriorityBuckets(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "c", Attributes: map[string]string{"priority": "0"}},
		{ID: "a", Attributes: map[string]string{"priority": "10"}},
		{ID: "b", Attributes: map[string]string{"priority": "10"}},
	}

	want := []string{"a", "b", "a", "b"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "mixed", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
		if got.ID == "c" {
			t.Fatalf("Pick() #%d unexpectedly selected lower priority auth", i)
		}
	}
}

func TestFillFirstSelectorPick_PriorityFallbackCooldown(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	now := time.Now()
	model := "test-model"

	high := &Auth{
		ID:         "high",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Quota: QuotaState{
					Exceeded: true,
				},
			},
		},
	}
	low := &Auth{ID: "low", Attributes: map[string]string{"priority": "0"}}

	got, err := selector.Pick(context.Background(), "mixed", model, cliproxyexecutor.Options{}, []*Auth{high, low})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "low" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "low")
	}
}

func TestRoundRobinSelectorPick_Concurrent(t *testing.T) {
	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	goroutines := 32
	iterations := 100
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if got == nil {
					select {
					case errCh <- errors.New("Pick() returned nil auth"):
					default:
					}
					return
				}
				if got.ID == "" {
					select {
					case errCh <- errors.New("Pick() returned auth with empty ID"):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent Pick() error = %v", err)
	default:
	}
}

func TestSelectorPick_AllCooldownReturnsModelCooldownError(t *testing.T) {
	t.Parallel()

	model := "test-model"
	now := time.Now()
	next := now.Add(60 * time.Second)
	auths := []*Auth{
		{
			ID: "a",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusActive,
					Unavailable:    true,
					NextRetryAfter: next,
					Quota: QuotaState{
						Exceeded:      true,
						NextRecoverAt: next,
					},
				},
			},
		},
		{
			ID: "b",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusActive,
					Unavailable:    true,
					NextRetryAfter: next,
					Quota: QuotaState{
						Exceeded:      true,
						NextRecoverAt: next,
					},
				},
			},
		},
	}

	t.Run("mixed provider redacts provider field", func(t *testing.T) {
		t.Parallel()

		selector := &FillFirstSelector{}
		_, err := selector.Pick(context.Background(), "mixed", model, cliproxyexecutor.Options{}, auths)
		if err == nil {
			t.Fatalf("Pick() error = nil")
		}

		var mce *modelCooldownError
		if !errors.As(err, &mce) {
			t.Fatalf("Pick() error = %T, want *modelCooldownError", err)
		}
		if mce.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("StatusCode() = %d, want %d", mce.StatusCode(), http.StatusTooManyRequests)
		}

		headers := mce.Headers()
		if got := headers.Get("Retry-After"); got == "" {
			t.Fatalf("Headers().Get(Retry-After) = empty")
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(mce.Error()), &payload); err != nil {
			t.Fatalf("json.Unmarshal(Error()) error = %v", err)
		}
		rawErr, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("Error() payload missing error object: %v", payload)
		}
		if got, _ := rawErr["code"].(string); got != "model_cooldown" {
			t.Fatalf("Error().error.code = %q, want %q", got, "model_cooldown")
		}
		if _, ok := rawErr["provider"]; ok {
			t.Fatalf("Error().error.provider exists for mixed provider: %v", rawErr["provider"])
		}
	})

	t.Run("non-mixed provider includes provider field", func(t *testing.T) {
		t.Parallel()

		selector := &FillFirstSelector{}
		_, err := selector.Pick(context.Background(), "gemini", model, cliproxyexecutor.Options{}, auths)
		if err == nil {
			t.Fatalf("Pick() error = nil")
		}

		var mce *modelCooldownError
		if !errors.As(err, &mce) {
			t.Fatalf("Pick() error = %T, want *modelCooldownError", err)
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(mce.Error()), &payload); err != nil {
			t.Fatalf("json.Unmarshal(Error()) error = %v", err)
		}
		rawErr, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("Error() payload missing error object: %v", payload)
		}
		if got, _ := rawErr["provider"].(string); got != "gemini" {
			t.Fatalf("Error().error.provider = %q, want %q", got, "gemini")
		}
	})
}

func TestIsAuthBlockedForModel_UnavailableWithoutNextRetryIsBlocked(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusActive,
				Unavailable: true,
				Quota: QuotaState{
					Exceeded: true,
				},
			},
		},
	}

	blocked, reason, next := isAuthBlockedForModel(auth, model, now)
	if !blocked {
		t.Fatalf("blocked = false, want true")
	}
	if reason != blockReasonOther {
		t.Fatalf("reason = %v, want %v", reason, blockReasonOther)
	}
	if !next.IsZero() {
		t.Fatalf("next = %v, want zero", next)
	}
}

func TestIsAuthBlockedForModel_AuthQuotaExceededWithoutRecoveryIsBlocked(t *testing.T) {
	t.Parallel()

	auth := &Auth{ID: "a", Quota: QuotaState{Exceeded: true}}
	for _, model := range []string{"", "test-model"} {
		blocked, reason, next := isAuthBlockedForModel(auth, model, time.Now())
		if !blocked || reason != blockReasonOther || !next.IsZero() {
			t.Fatalf("isAuthBlockedForModel(%q) = %v, %v, %v; want true, other, zero", model, blocked, reason, next)
		}
	}
}

func TestIsAuthBlockedForModel_ExpiredRecoveryIsAvailable(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auth := &Auth{
		ID:             "a",
		Unavailable:    true,
		NextRetryAfter: now.Add(-time.Minute),
		Quota: QuotaState{
			Exceeded:      true,
			NextRecoverAt: now.Add(-time.Second),
		},
	}
	blocked, reason, next := isAuthBlockedForModel(auth, "", now)
	if blocked || reason != blockReasonNone || !next.IsZero() {
		t.Fatalf("isAuthBlockedForModel() = %v, %v, %v; want false, none, zero", blocked, reason, next)
	}
}

func TestFillFirstSelectorPick_ThinkingSuffixFallsBackToBaseModelState(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	now := time.Now()

	baseModel := "test-model"
	requestedModel := "test-model(high)"

	high := &Auth{
		ID:         "high",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			baseModel: {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Quota: QuotaState{
					Exceeded: true,
				},
			},
		},
	}
	low := &Auth{
		ID:         "low",
		Attributes: map[string]string{"priority": "0"},
	}

	got, err := selector.Pick(context.Background(), "mixed", requestedModel, cliproxyexecutor.Options{}, []*Auth{high, low})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "low" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "low")
	}
}

func TestIsAuthBlockedForModel_ThinkingSuffixStatesBlockCanonicalModel(t *testing.T) {
	t.Parallel()

	now := time.Now()
	laterRetry := now.Add(2 * time.Hour)
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			"test-model(high)": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(time.Hour),
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: now.Add(time.Hour),
				},
			},
			"test-model(low)": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: laterRetry,
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: laterRetry,
				},
			},
		},
	}

	for _, model := range []string{"test-model", "test-model(medium)", "test-model(low)"} {
		blocked, reason, next := isAuthBlockedForModel(auth, model, now)
		if !blocked || reason != blockReasonCooldown || !next.Equal(laterRetry) {
			t.Fatalf("isAuthBlockedForModel(%q) = %v, %v, %v; want true, cooldown, %v", model, blocked, reason, next, laterRetry)
		}
	}
}

func TestRoundRobinSelectorPick_ThinkingSuffixSharesCursor(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
	}

	first, err := selector.Pick(context.Background(), "gemini", "test-model(high)", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	second, err := selector.Pick(context.Background(), "gemini", "test-model(low)", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if first == nil || second == nil {
		t.Fatalf("Pick() returned nil auth")
	}
	if first.ID != "a" {
		t.Fatalf("Pick() first auth.ID = %q, want %q", first.ID, "a")
	}
	if second.ID != "b" {
		t.Fatalf("Pick() second auth.ID = %q, want %q", second.ID, "b")
	}
}

func TestRoundRobinSelectorPick_CursorKeyCap(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{maxKeys: 2}
	auths := []*Auth{{ID: "a"}}

	_, _ = selector.Pick(context.Background(), "gemini", "m1", cliproxyexecutor.Options{}, auths)
	_, _ = selector.Pick(context.Background(), "gemini", "m2", cliproxyexecutor.Options{}, auths)
	_, _ = selector.Pick(context.Background(), "gemini", "m3", cliproxyexecutor.Options{}, auths)

	selector.mu.Lock()
	defer selector.mu.Unlock()

	if selector.cursors == nil {
		t.Fatalf("selector.cursors = nil")
	}
	if len(selector.cursors) != 1 {
		t.Fatalf("len(selector.cursors) = %d, want %d", len(selector.cursors), 1)
	}
	if _, ok := selector.cursors["gemini:m3"]; !ok {
		t.Fatalf("selector.cursors missing key %q", "gemini:m3")
	}
}

func TestExtractSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "valid_claude_code_format",
			payload: `{"metadata":{"user_id":"user_3f221fe75652cf9a89a31647f16274bb8036a9b85ac4dc226a4df0efec8dc04d_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`,
			want:    "claude:ac980658-63bd-4fb3-97ba-8da64cb1e344",
		},
		{
			name:    "json_user_id_with_session_id",
			payload: `{"metadata":{"user_id":"{\"device_id\":\"be82c3aee1e0c2d74535bacc85f9f559228f02dd8a17298cf522b71e6c375714\",\"account_uuid\":\"\",\"session_id\":\"e26d4046-0f88-4b09-bb5b-f863ab5fb24e\"}"}}`,
			want:    "claude:e26d4046-0f88-4b09-bb5b-f863ab5fb24e",
		},
		{
			name:    "json_user_id_without_session_id",
			payload: `{"metadata":{"user_id":"{\"device_id\":\"abc123\"}"}}`,
			want:    `user:{"device_id":"abc123"}`,
		},
		{
			name:    "no_session_but_user_id",
			payload: `{"metadata":{"user_id":"user_abc123"}}`,
			want:    "user:user_abc123",
		},
		{
			name:    "conversation_id",
			payload: `{"conversation_id":"conv-12345"}`,
			want:    "conv:conv-12345",
		},
		{
			name:    "no_metadata",
			payload: `{"model":"claude-3"}`,
			want:    "",
		},
		{
			name:    "empty_payload",
			payload: ``,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSessionID([]byte(tt.payload))
			if got != tt.want {
				t.Errorf("extractSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionAffinitySelector_SameSessionSameAuth(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelector(fallback)

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	// Use valid UUID format for session ID
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// Same session should always pick the same auth
	first, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if first == nil {
		t.Fatalf("Pick() returned nil")
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})

	// Verify consistency: same session, same auths -> same result
	for i := 0; i < 10; i++ {
		got, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got.ID != first.ID {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q (same session should pick same auth)", i, got.ID, first.ID)
		}
	}
}

func TestSessionAffinitySelector_ThinkingSuffixVariantsPreserveBindingAndRelease(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelector(fallback)
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	first, errFirst := selector.Pick(context.Background(), "anthropic", "claude-sonnet-4-5", opts, auths)
	if errFirst != nil {
		t.Fatalf("first Pick() error = %v", errFirst)
	}
	if first == nil {
		t.Fatalf("first Pick() returned nil")
	}

	// Suffix variant claude-sonnet-4-5(high) should reuse the exact same auth binding
	second, errSecond := selector.Pick(context.Background(), "anthropic", "claude-sonnet-4-5(high)", opts, auths)
	if errSecond != nil {
		t.Fatalf("second Pick() error = %v", errSecond)
	}
	if second.ID != first.ID {
		t.Fatalf("second Pick() auth.ID = %q, want %q (thinking suffix variant should keep session stickiness)", second.ID, first.ID)
	}

	// Third request with claude-sonnet-4-5(medium) should also reuse the same auth
	third, errThird := selector.Pick(context.Background(), "anthropic", "claude-sonnet-4-5(medium)", opts, auths)
	if errThird != nil {
		t.Fatalf("third Pick() error = %v", errThird)
	}
	if third.ID != first.ID {
		t.Fatalf("third Pick() auth.ID = %q, want %q (thinking suffix variant should keep session stickiness)", third.ID, first.ID)
	}

	// Failure on a thinking-suffix variant (with explicit metadata) should properly release the session binding
	optsWithMetadata := cliproxyexecutor.Options{
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "anthropic",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "claude-sonnet-4-5(high)",
		},
	}
	selector.OnResult(Result{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5(high)",
		AuthID:   first.ID,
		Success:  false,
		Error:    &Error{Code: "rate_limited", Message: "rate limited"},
		Options:  optsWithMetadata,
	})

	// After release, next pick should reselect using fallback selector
	next, errNext := selector.Pick(context.Background(), "anthropic", "claude-sonnet-4-5", opts, auths)
	if errNext != nil {
		t.Fatalf("next Pick() error = %v", errNext)
	}
	if next.ID == first.ID {
		t.Fatalf("next Pick() auth.ID = %q, should have reselected a different auth after failure release", next.ID)
	}
}

func TestSessionAffinitySelector_WeightedBindingRebindsAfterWeightBecomesZero(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&WeightedRoundRobinSelector{})
	defer selector.Stop()

	authA := &Auth{ID: "auth-a", Attributes: map[string]string{AttributeWeight: "1"}}
	authB := &Auth{ID: "auth-b", Attributes: map[string]string{AttributeWeight: "1"}}
	auths := []*Auth{authA, authB}
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_weight-change"}}`)}

	first, errFirst := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if errFirst != nil {
		t.Fatalf("first Pick() error = %v", errFirst)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})
	if first.ID != authA.ID {
		t.Fatalf("first Pick() auth.ID = %q, want %q", first.ID, authA.ID)
	}

	authA.Attributes[AttributeWeight] = "0"
	second, errSecond := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if errSecond != nil {
		t.Fatalf("Pick() after weight update error = %v", errSecond)
	}
	selector.OnResult(Result{AuthID: second.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})
	if second.ID != authB.ID {
		t.Fatalf("Pick() after weight update auth.ID = %q, want %q", second.ID, authB.ID)
	}

	authA.Attributes[AttributeWeight] = "10"
	third, errThird := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if errThird != nil {
		t.Fatalf("Pick() after rebind error = %v", errThird)
	}
	if third.ID != authB.ID {
		t.Fatalf("Pick() after rebind auth.ID = %q, want sticky auth %q", third.ID, authB.ID)
	}
}

func TestSessionAffinitySelector_WeightedNewSessionsResetAfterWeightChange(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&WeightedRoundRobinSelector{})
	defer selector.Stop()
	authA := &Auth{ID: "auth-a", Attributes: map[string]string{AttributeWeight: "1000000"}}
	authB := &Auth{ID: "auth-b", Attributes: map[string]string{AttributeWeight: "1"}}
	auths := []*Auth{authA, authB}
	pickSession := func(index int) *Auth {
		t.Helper()
		opts := cliproxyexecutor.Options{OriginalRequest: []byte(fmt.Sprintf(`{"session_id":"session-%d"}`, index))}
		picked, errPick := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
		if errPick != nil {
			t.Fatalf("Pick(session-%d) error = %v", index, errPick)
		}
		selector.OnResult(Result{AuthID: picked.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})
		return picked
	}
	for index := 0; index < 1000; index++ {
		pickSession(index)
	}

	authA.Attributes[AttributeWeight] = "1"
	counts := make(map[string]int)
	for index := 1000; index < 1020; index++ {
		counts[pickSession(index).ID]++
	}
	if counts[authA.ID] != 10 || counts[authB.ID] != 10 {
		t.Fatalf("new session picks after weight change = %#v, want 10 each", counts)
	}
}

func TestSessionAffinitySelector_NoSessionFallback(t *testing.T) {
	t.Parallel()

	fallback := &FillFirstSelector{}
	selector := NewSessionAffinitySelector(fallback)

	auths := []*Auth{
		{ID: "auth-b"},
		{ID: "auth-a"},
		{ID: "auth-c"},
	}

	// No session in payload, should fallback to FillFirstSelector (picks "auth-a" after sorting)
	payload := []byte(`{"model":"claude-3"}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	got, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "auth-a" {
		t.Fatalf("Pick() auth.ID = %q, want %q (should fallback to FillFirst)", got.ID, "auth-a")
	}
}

func TestSessionAffinitySelector_DifferentSessionsDifferentAuths(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelector(fallback)

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	// Use valid UUID format for session IDs
	session1 := []byte(`{"metadata":{"user_id":"user_xxx_account__session_11111111-1111-1111-1111-111111111111"}}`)
	session2 := []byte(`{"metadata":{"user_id":"user_xxx_account__session_22222222-2222-2222-2222-222222222222"}}`)

	opts1 := cliproxyexecutor.Options{OriginalRequest: session1}
	opts2 := cliproxyexecutor.Options{OriginalRequest: session2}

	auth1, _ := selector.Pick(context.Background(), "claude", "claude-3", opts1, auths)
	selector.OnResult(Result{AuthID: auth1.ID, Provider: "claude", Model: "claude-3", Options: opts1, Success: true})
	auth2, _ := selector.Pick(context.Background(), "claude", "claude-3", opts2, auths)
	selector.OnResult(Result{AuthID: auth2.ID, Provider: "claude", Model: "claude-3", Options: opts2, Success: true})

	// Different sessions may or may not pick different auths (depends on hash collision)
	// But each session should be consistent
	for i := 0; i < 5; i++ {
		got1, _ := selector.Pick(context.Background(), "claude", "claude-3", opts1, auths)
		got2, _ := selector.Pick(context.Background(), "claude", "claude-3", opts2, auths)
		if got1.ID != auth1.ID {
			t.Fatalf("session1 Pick() #%d inconsistent: got %q, want %q", i, got1.ID, auth1.ID)
		}
		if got2.ID != auth2.ID {
			t.Fatalf("session2 Pick() #%d inconsistent: got %q, want %q", i, got2.ID, auth2.ID)
		}
	}
}

func TestSessionAffinitySelector_FailoverWhenAuthUnavailable(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_failover-test-uuid"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// First pick establishes binding
	first, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})

	// Remove the bound auth from available list (simulating rate limit)
	availableWithoutFirst := make([]*Auth, 0, len(auths)-1)
	for _, a := range auths {
		if a.ID != first.ID {
			availableWithoutFirst = append(availableWithoutFirst, a)
		}
	}

	// With failover enabled, should pick a new auth
	second, err := selector.Pick(context.Background(), "claude", "claude-3", opts, availableWithoutFirst)
	if err != nil {
		t.Fatalf("Pick() after failover error = %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("Pick() after failover returned same auth %q, expected different", first.ID)
	}
	selector.OnResult(Result{AuthID: second.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})

	// Subsequent picks should consistently return the new binding
	for i := 0; i < 5; i++ {
		got, _ := selector.Pick(context.Background(), "claude", "claude-3", opts, availableWithoutFirst)
		if got.ID != second.ID {
			t.Fatalf("Pick() #%d after failover inconsistent: got %q, want %q", i, got.ID, second.ID)
		}
	}
}

func TestExtractSessionID_ClaudeCodePriorityOverHeader(t *testing.T) {
	t.Parallel()

	// Claude Code metadata.user_id remains higher priority than a generic X-Session-ID header.
	headers := make(http.Header)
	headers.Set("X-Session-ID", "header-session-id")

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)

	got := ExtractSessionID(headers, payload, nil)
	want := "claude:ac980658-63bd-4fb3-97ba-8da64cb1e344"
	if got != want {
		t.Errorf("ExtractSessionID() = %q, want %q (Claude Code should have highest priority over header)", got, want)
	}
}

func TestExtractSessionID_ClaudeCodePriorityOverIdempotencyKey(t *testing.T) {
	t.Parallel()

	// Claude Code metadata.user_id should have highest priority, even when idempotency_key is present
	metadata := map[string]any{"idempotency_key": "idem-12345"}
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)

	got := ExtractSessionID(nil, payload, metadata)
	want := "claude:ac980658-63bd-4fb3-97ba-8da64cb1e344"
	if got != want {
		t.Errorf("ExtractSessionID() = %q, want %q (Claude Code should have highest priority over idempotency_key)", got, want)
	}
}

func TestExtractSessionID_Headers(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	headers.Set("X-Session-ID", "my-explicit-session")

	got := ExtractSessionID(headers, nil, nil)
	want := "header:my-explicit-session"
	if got != want {
		t.Errorf("ExtractSessionID() with header = %q, want %q", got, want)
	}
}

func TestExtractSessionID_CodexSessionIDHeader(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	headers.Set("Session_id", "codex-session-123")

	got := ExtractSessionID(headers, nil, nil)
	want := "codex:codex-session-123"
	if got != want {
		t.Errorf("ExtractSessionID() with Session_id = %q, want %q", got, want)
	}
}

func TestExtractSessionID_ClientRequestIDHeader(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	headers.Set("X-Client-Request-Id", "pi-session-123")

	got := ExtractSessionID(headers, nil, nil)
	want := "clientreq:pi-session-123"
	if got != want {
		t.Errorf("ExtractSessionID() with X-Client-Request-Id = %q, want %q", got, want)
	}
}

func TestExtractSessionID_CodexSessionIDPriorityOverClientRequestID(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	headers.Set("X-Client-Request-Id", "pi-session-123")
	headers.Set("Session_id", "codex-session-456")

	got := ExtractSessionID(headers, nil, nil)
	want := "codex:codex-session-456"
	if got != want {
		t.Errorf("ExtractSessionID() = %q, want %q (Session_id should take priority over X-Client-Request-Id)", got, want)
	}
}

// TestExtractSessionID_IdempotencyKey verifies that idempotency_key is intentionally
// ignored for session affinity (it's auto-generated per-request, causing cache misses).
func TestExtractSessionID_IdempotencyKey(t *testing.T) {
	t.Parallel()

	metadata := map[string]any{"idempotency_key": "idem-12345"}

	got := ExtractSessionID(nil, nil, metadata)
	// idempotency_key is disabled - should return empty (no payload to hash)
	if got != "" {
		t.Errorf("ExtractSessionID() with idempotency_key = %q, want empty (idempotency_key is disabled)", got)
	}
}

func TestExtractSessionID_DerivedSessionAndExplicitPriority(t *testing.T) {
	t.Parallel()

	metadata := map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:derived-root"}
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	if got := ExtractSessionID(nil, payload, metadata); got != "derived:ctx:v1:derived-root" {
		t.Fatalf("ExtractSessionID() = %q, want derived identity", got)
	}

	executionMetadata := map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "execution-session",
		cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:derived-root",
	}
	if got := ExtractSessionID(nil, payload, executionMetadata); got != "execution:execution-session" {
		t.Fatalf("ExtractSessionID() = %q, want explicit execution session", got)
	}

	explicitPayload := []byte(`{"session_id":"explicit-session","prompt_cache_key":"explicit-cache","messages":[{"role":"user","content":"hello"}]}`)
	if got := ExtractSessionID(nil, explicitPayload, metadata); got != "session:explicit-session" {
		t.Fatalf("ExtractSessionID() = %q, want explicit body session", got)
	}

	userPayload := []byte(`{"metadata":{"user_id":"explicit-user"},"conversation_id":"explicit-conversation","messages":[{"role":"user","content":"hello"}]}`)
	if got := ExtractSessionID(nil, userPayload, metadata); got != "user:explicit-user" {
		t.Fatalf("ExtractSessionID() = %q, want explicit metadata.user_id", got)
	}

	lowercaseHeaders := http.Header{"x-session-id": []string{" lowercase-session "}}
	if got := ExtractSessionID(lowercaseHeaders, payload, metadata); got != "header:lowercase-session" {
		t.Fatalf("ExtractSessionID() = %q, want case-insensitive trimmed header session", got)
	}

	headers := make(http.Header)
	headers.Set("X-Session-ID", "header-session")
	if got := ExtractSessionID(headers, explicitPayload, metadata); got != "header:header-session" {
		t.Fatalf("ExtractSessionID() = %q, want explicit header session", got)
	}
}

func TestExtractSessionID_MessageHashFallback(t *testing.T) {
	t.Parallel()

	// First request (user only) generates short hash
	firstRequestPayload := []byte(`{"messages":[{"role":"user","content":"Hello world"}]}`)
	shortHash := ExtractSessionID(nil, firstRequestPayload, nil)
	if shortHash == "" {
		t.Error("ExtractSessionID() first request should return short hash")
	}
	if !strings.HasPrefix(shortHash, "msg:") {
		t.Errorf("ExtractSessionID() = %q, want prefix 'msg:'", shortHash)
	}

	// Multi-turn with assistant generates full hash (different from short hash)
	multiTurnPayload := []byte(`{"messages":[
		{"role":"user","content":"Hello world"},
		{"role":"assistant","content":"Hi! How can I help?"},
		{"role":"user","content":"Tell me a joke"}
	]}`)
	fullHash := ExtractSessionID(nil, multiTurnPayload, nil)
	if fullHash == "" {
		t.Error("ExtractSessionID() multi-turn should return full hash")
	}
	if fullHash == shortHash {
		t.Error("Full hash should differ from short hash (includes assistant)")
	}

	// Same multi-turn payload should produce same hash
	fullHash2 := ExtractSessionID(nil, multiTurnPayload, nil)
	if fullHash != fullHash2 {
		t.Errorf("ExtractSessionID() not stable: got %q then %q", fullHash, fullHash2)
	}
}

func TestExtractSessionID_ClaudeAPITopLevelSystem(t *testing.T) {
	t.Parallel()

	// Claude API: system prompt in top-level "system" field (array format)
	arraySystem := []byte(`{
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"system": [{"type": "text", "text": "You are Claude Code"}]
	}`)
	got1 := ExtractSessionID(nil, arraySystem, nil)
	if got1 == "" || !strings.HasPrefix(got1, "msg:") {
		t.Errorf("ExtractSessionID() with array system = %q, want msg:* prefix", got1)
	}

	// Claude API: system prompt in top-level "system" field (string format)
	stringSystem := []byte(`{
		"messages": [{"role": "user", "content": "Hello"}],
		"system": "You are Claude Code"
	}`)
	got2 := ExtractSessionID(nil, stringSystem, nil)
	if got2 == "" || !strings.HasPrefix(got2, "msg:") {
		t.Errorf("ExtractSessionID() with string system = %q, want msg:* prefix", got2)
	}

	// Multi-turn with top-level system should produce stable hash
	multiTurn := []byte(`{
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi!"},
			{"role": "user", "content": "Help me"}
		],
		"system": "You are Claude Code"
	}`)
	got3 := ExtractSessionID(nil, multiTurn, nil)
	if got3 == "" {
		t.Error("ExtractSessionID() multi-turn with top-level system should return hash")
	}
	if got3 == got2 {
		t.Error("Multi-turn hash should differ from first-turn hash (includes assistant)")
	}
}

func TestExtractSessionID_GeminiFormat(t *testing.T) {
	t.Parallel()

	// Gemini format with systemInstruction and contents
	payload := []byte(`{
		"systemInstruction": {"parts": [{"text": "You are a helpful assistant."}]},
		"contents": [
			{"role": "user", "parts": [{"text": "Hello Gemini"}]},
			{"role": "model", "parts": [{"text": "Hi there!"}]}
		]
	}`)

	got := ExtractSessionID(nil, payload, nil)
	if got == "" {
		t.Error("ExtractSessionID() with Gemini format should return hash-based session ID")
	}
	if !strings.HasPrefix(got, "msg:") {
		t.Errorf("ExtractSessionID() = %q, want prefix 'msg:'", got)
	}

	// Same payload should produce same hash
	got2 := ExtractSessionID(nil, payload, nil)
	if got != got2 {
		t.Errorf("ExtractSessionID() not stable: got %q then %q", got, got2)
	}

	// Different user message should produce different hash
	differentPayload := []byte(`{
		"systemInstruction": {"parts": [{"text": "You are a helpful assistant."}]},
		"contents": [
			{"role": "user", "parts": [{"text": "Hello different"}]},
			{"role": "model", "parts": [{"text": "Hi there!"}]}
		]
	}`)
	got3 := ExtractSessionID(nil, differentPayload, nil)
	if got == got3 {
		t.Errorf("ExtractSessionID() should produce different hash for different user message")
	}
}

func TestExtractSessionID_OpenAIResponsesAPI(t *testing.T) {
	t.Parallel()

	firstTurn := []byte(`{
		"instructions": "You are Codex, based on GPT-5.",
		"input": [
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "system instructions"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}
		]
	}`)

	got1 := ExtractSessionID(nil, firstTurn, nil)
	if got1 == "" {
		t.Error("ExtractSessionID() should return hash for OpenAI Responses API format")
	}
	if !strings.HasPrefix(got1, "msg:") {
		t.Errorf("ExtractSessionID() = %q, want prefix 'msg:'", got1)
	}

	secondTurn := []byte(`{
		"instructions": "You are Codex, based on GPT-5.",
		"input": [
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "system instructions"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "thinking..."}], "encrypted_content": "xxx"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Hello!"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "what can you do"}]}
		]
	}`)

	got2 := ExtractSessionID(nil, secondTurn, nil)
	if got2 == "" {
		t.Error("ExtractSessionID() should return hash for second turn")
	}

	if got1 == got2 {
		t.Log("First turn and second turn have different hashes (expected: second includes assistant)")
	}

	thirdTurn := []byte(`{
		"instructions": "You are Codex, based on GPT-5.",
		"input": [
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "system instructions"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "thinking..."}], "encrypted_content": "xxx"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Hello!"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "what can you do"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "I can help with..."}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "thanks"}]}
		]
	}`)

	got3 := ExtractSessionID(nil, thirdTurn, nil)
	if got2 != got3 {
		t.Errorf("Second and third turn should have same hash (same first assistant): got %q vs %q", got2, got3)
	}
}

func TestSessionCache_RestoreAliasesIfAbsent_IndependentRestoration(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()

	// Initial fallback group with 3 aliases bound to auth-b
	cache.SetAliases("auth-b", "fallbackKey", "reboundAlias", "stillAbsentAlias")

	// Fallback group gets deleted during merge attempt
	removed := cache.CompareAndDeleteGroup("fallbackKey", "auth-b", 1, []string{"fallbackKey", "reboundAlias", "stillAbsentAlias"})
	if len(removed) == 0 {
		t.Fatalf("CompareAndDeleteGroup failed")
	}

	// Concurrent writer rebinds reboundAlias to auth-x
	cache.SetAliases("auth-x", "reboundAlias")

	// Merge exhaustion attempts to restore remaining aliases
	restored := cache.RestoreAliasesIfAbsent("auth-b", removed...)
	if !restored {
		t.Fatalf("RestoreAliasesIfAbsent should return true when some aliases are still absent")
	}

	// reboundAlias must remain bound to auth-x
	if got, ok := cache.Get("reboundAlias"); !ok || got != "auth-x" {
		t.Fatalf("reboundAlias must remain auth-x, got %q, %v", got, ok)
	}

	// fallbackKey and stillAbsentAlias must be restored to auth-b
	if got, ok := cache.Get("fallbackKey"); !ok || got != "auth-b" {
		t.Fatalf("fallbackKey must be restored to auth-b, got %q, %v", got, ok)
	}
	if got, ok := cache.Get("stillAbsentAlias"); !ok || got != "auth-b" {
		t.Fatalf("stillAbsentAlias must be restored to auth-b, got %q, %v", got, ok)
	}
}

func TestSessionAffinitySelector_ThreeScenarios(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}, {ID: "auth-c"}}

	testCases := []struct {
		name     string
		scenario string
		payload  []byte
	}{
		{
			name:     "OpenAI_Scenario1_NewRequest",
			scenario: "new",
			payload:  []byte(`{"messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Hello"}]}`),
		},
		{
			name:     "OpenAI_Scenario2_SecondTurn",
			scenario: "second",
			payload:  []byte(`{"messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi there!"},{"role":"user","content":"Help me"}]}`),
		},
		{
			name:     "OpenAI_Scenario3_ManyTurns",
			scenario: "many",
			payload:  []byte(`{"messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi there!"},{"role":"user","content":"Help me"},{"role":"assistant","content":"Sure!"},{"role":"user","content":"Thanks"}]}`),
		},
		{
			name:     "Gemini_Scenario1_NewRequest",
			scenario: "new",
			payload:  []byte(`{"systemInstruction":{"parts":[{"text":"You are helpful"}]},"contents":[{"role":"user","parts":[{"text":"Hello Gemini"}]}]}`),
		},
		{
			name:     "Gemini_Scenario2_SecondTurn",
			scenario: "second",
			payload:  []byte(`{"systemInstruction":{"parts":[{"text":"You are helpful"}]},"contents":[{"role":"user","parts":[{"text":"Hello Gemini"}]},{"role":"model","parts":[{"text":"Hi!"}]},{"role":"user","parts":[{"text":"Help"}]}]}`),
		},
		{
			name:     "Gemini_Scenario3_ManyTurns",
			scenario: "many",
			payload:  []byte(`{"systemInstruction":{"parts":[{"text":"You are helpful"}]},"contents":[{"role":"user","parts":[{"text":"Hello Gemini"}]},{"role":"model","parts":[{"text":"Hi!"}]},{"role":"user","parts":[{"text":"Help"}]},{"role":"model","parts":[{"text":"Sure!"}]},{"role":"user","parts":[{"text":"Thanks"}]}]}`),
		},
		{
			name:     "Claude_Scenario1_NewRequest",
			scenario: "new",
			payload:  []byte(`{"messages":[{"role":"user","content":"Hello Claude"}]}`),
		},
		{
			name:     "Claude_Scenario2_SecondTurn",
			scenario: "second",
			payload:  []byte(`{"messages":[{"role":"user","content":"Hello Claude"},{"role":"assistant","content":"Hello!"},{"role":"user","content":"Help me"}]}`),
		},
		{
			name:     "Claude_Scenario3_ManyTurns",
			scenario: "many",
			payload:  []byte(`{"messages":[{"role":"user","content":"Hello Claude"},{"role":"assistant","content":"Hello!"},{"role":"user","content":"Help"},{"role":"assistant","content":"Sure!"},{"role":"user","content":"Thanks"}]}`),
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			opts := cliproxyexecutor.Options{OriginalRequest: tc.payload}
			picked, err := selector.Pick(context.Background(), "provider", "model", opts, auths)
			if err != nil {
				t.Fatalf("Pick() error = %v", err)
			}
			if picked == nil {
				t.Fatal("Pick() returned nil")
			}
			t.Logf("%s: picked %s", tc.name, picked.ID)
		})
	}

	t.Run("Scenario2And3_SameAuth", func(t *testing.T) {
		openaiS2 := []byte(`{"messages":[{"role":"system","content":"Stable test"},{"role":"user","content":"First msg"},{"role":"assistant","content":"Response"},{"role":"user","content":"Second"}]}`)
		openaiS3 := []byte(`{"messages":[{"role":"system","content":"Stable test"},{"role":"user","content":"First msg"},{"role":"assistant","content":"Response"},{"role":"user","content":"Second"},{"role":"assistant","content":"More"},{"role":"user","content":"Third"}]}`)

		opts2 := cliproxyexecutor.Options{OriginalRequest: openaiS2}
		opts3 := cliproxyexecutor.Options{OriginalRequest: openaiS3}

		picked2, _ := selector.Pick(context.Background(), "test", "model", opts2, auths)
		selector.OnResult(Result{AuthID: picked2.ID, Provider: "test", Model: "model", Options: opts2, Success: true})
		picked3, _ := selector.Pick(context.Background(), "test", "model", opts3, auths)

		if picked2.ID != picked3.ID {
			t.Errorf("Scenario2 and Scenario3 should pick same auth: got %s vs %s", picked2.ID, picked3.ID)
		}
	})

	t.Run("Scenario1To2_InheritBinding", func(t *testing.T) {
		s1 := []byte(`{"messages":[{"role":"system","content":"Inherit test"},{"role":"user","content":"Initial"}]}`)
		s2 := []byte(`{"messages":[{"role":"system","content":"Inherit test"},{"role":"user","content":"Initial"},{"role":"assistant","content":"Reply"},{"role":"user","content":"Continue"}]}`)

		opts1 := cliproxyexecutor.Options{OriginalRequest: s1}
		opts2 := cliproxyexecutor.Options{OriginalRequest: s2}

		picked1, _ := selector.Pick(context.Background(), "inherit", "model", opts1, auths)
		selector.OnResult(Result{AuthID: picked1.ID, Provider: "inherit", Model: "model", Options: opts1, Success: true})
		picked2, _ := selector.Pick(context.Background(), "inherit", "model", opts2, auths)

		if picked1.ID != picked2.ID {
			t.Errorf("Scenario2 should inherit Scenario1 binding: got %s vs %s", picked1.ID, picked2.ID)
		}
	})
}

func TestSessionAffinitySelectorBodyIdentifierTransitionsPreserveBinding(t *testing.T) {
	t.Parallel()

	bothPayload := []byte(`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`)
	primaryID, fallbackID := extractSessionIDs(nil, bothPayload, nil)
	if primaryID != "pck:shared-cache-bucket" || fallbackID != "conv:conversation-session" {
		t.Fatalf("extractSessionIDs() = (%q, %q), want prompt-cache primary with conversation fallback", primaryID, fallbackID)
	}

	for _, tt := range []struct {
		name         string
		firstPayload []byte
	}{
		{name: "prompt cache first", firstPayload: []byte(`{"prompt_cache_key":"shared-cache-bucket"}`)},
		{name: "conversation first", firstPayload: []byte(`{"conversation":{"id":"conversation-session"}}`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback: &RoundRobinSelector{},
				TTL:      time.Minute,
			})
			defer selector.Stop()
			auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
			provider := "responses-transition-" + tt.name

			first, err := selector.Pick(context.Background(), provider, "gpt-test", cliproxyexecutor.Options{OriginalRequest: tt.firstPayload}, auths)
			if err != nil {
				t.Fatalf("first Pick() error = %v", err)
			}
			selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: "gpt-test", Options: cliproxyexecutor.Options{OriginalRequest: tt.firstPayload}, Success: true})
			second, err := selector.Pick(context.Background(), provider, "gpt-test", cliproxyexecutor.Options{OriginalRequest: bothPayload}, auths)
			if err != nil {
				t.Fatalf("combined-identifier Pick() error = %v", err)
			}
			if second.ID != first.ID {
				t.Fatalf("combined identifiers changed auth from %q to %q", first.ID, second.ID)
			}
		})
	}
}

func TestSessionAffinitySelectorCombinedIdentifiersBindConversationFallback(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	provider := "responses-combined-to-conversation"

	combined := []byte(`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`)
	conversationOnly := []byte(`{"conversation":{"id":"conversation-session"}}`)
	first, err := selector.Pick(context.Background(), provider, "gpt-test", cliproxyexecutor.Options{OriginalRequest: combined}, auths)
	if err != nil {
		t.Fatalf("combined-identifier Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: "gpt-test", Options: cliproxyexecutor.Options{OriginalRequest: combined}, Success: true})
	second, err := selector.Pick(context.Background(), provider, "gpt-test", cliproxyexecutor.Options{OriginalRequest: conversationOnly}, auths)
	if err != nil {
		t.Fatalf("conversation-only Pick() error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("dropping prompt_cache_key changed auth from %q to %q", first.ID, second.ID)
	}
}

func TestSessionAffinitySelectorFailureQuarantinesAllAliases(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	provider := "responses-alias-group-failure"
	model := "gpt-test"

	combined := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`), Metadata: map[string]any{}}
	first, err := selector.Pick(context.Background(), provider, model, combined, auths)
	if err != nil {
		t.Fatalf("combined-identifier Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: model, Options: combined, Success: true})

	promptOnly := cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"shared-cache-bucket"}`), Metadata: map[string]any{}}
	failed, err := selector.Pick(context.Background(), provider, model, promptOnly, auths)
	if err != nil {
		t.Fatalf("prompt-only Pick() error = %v", err)
	}
	if failed.ID != first.ID {
		t.Fatalf("prompt-only alias selected %q, want %q", failed.ID, first.ID)
	}
	selector.OnResult(Result{AuthID: failed.ID, Provider: provider, Model: model, Options: promptOnly, Error: &Error{Code: "upstream_failed", Message: "upstream failed", Retryable: true}})

	conversationOnly := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-session"}}`), Metadata: map[string]any{}}
	next, err := selector.Pick(context.Background(), provider, model, conversationOnly, auths)
	if err != nil {
		t.Fatalf("conversation-only Pick() error = %v", err)
	}
	if next.ID == failed.ID {
		t.Fatalf("conversation alias reused failed auth %q", failed.ID)
	}
}

func TestSessionCacheCompareAndReplaceAliasesPreservesNewerBinding(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()
	cache.SetAliases("auth-a", "prompt", "conversation")
	_, gen1, aliases1, _ := cache.GetWithGeneration("prompt")
	cache.SetAliases("auth-b", "prompt", "conversation")

	if replaced := cache.CompareAndReplaceAliases("auth-a", gen1, aliases1, "auth-c"); replaced {
		t.Fatal("CompareAndReplaceAliases() succeeded for stale auth, want false")
	}
	for _, key := range []string{"prompt", "conversation"} {
		if got, ok := cache.Get(key); !ok || got != "auth-b" {
			t.Fatalf("cache.Get(%q) = %q, %v; want auth-b, true", key, got, ok)
		}
	}
}

func TestSessionAffinitySelectorUnavailableCachedAuthRebindsFullAliasGroup(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	// Three auths so the fallback replacement is stable and never collides with the stale cached auth.
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}, {ID: "auth-c"}}
	provider := "responses-alias-group-recovery"
	model := "gpt-test"

	combined := []byte(`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`)
	combinedOpts := cliproxyexecutor.Options{OriginalRequest: combined}
	first, err := selector.Pick(context.Background(), provider, model, combinedOpts, auths)
	if err != nil {
		t.Fatalf("combined Pick() error = %v", err)
	}
	bound := first.ID
	selector.OnResult(Result{AuthID: bound, Provider: provider, Model: model, Options: combinedOpts, Success: true})

	// Make the bound auth unavailable so it leaves the fallback reselect candidate set.
	for _, a := range auths {
		if a.ID == bound {
			a.Unavailable = true
			a.NextRetryAfter = time.Now().Add(time.Hour)
		}
	}

	// Prompt-cache-only request triggers the stale-primary recovery path.
	promptOpts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"shared-cache-bucket"}`)}
	replacement, err := selector.Pick(context.Background(), provider, model, promptOpts, auths)
	if err != nil {
		t.Fatalf("prompt-only recovery Pick() error = %v", err)
	}
	if replacement.ID == bound {
		t.Fatalf("recovery reused unavailable cached auth %q", bound)
	}

	// The full alias group must now point at the replacement auth: a conversation-only request
	// (touching the sibling alias the cache key likely chose as primary) must also recover to it.
	conversationOpts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-session"}}`)}
	for i := 0; i < 3; i++ {
		picked, errPick := selector.Pick(context.Background(), provider, model, conversationOpts, auths)
		if errPick != nil {
			t.Fatalf("conversation-only Pick() #%d error = %v", i, errPick)
		}
		if picked.ID != replacement.ID {
			t.Fatalf("conversation alias did not inherit replacement auth %q: got %q", replacement.ID, picked.ID)
		}
	}
}

func TestSessionAffinitySelectorCachedAuthUnavailableRebindsWholeAliasGroup(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	provider := "responses-cache-rebound"
	model := "gpt-test"

	combined := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`)}
	first, err := selector.Pick(context.Background(), provider, model, combined, auths)
	if err != nil {
		t.Fatalf("Pick() combined error = %v", err)
	}
	if first.ID != "auth-a" {
		t.Fatalf("first Pick() = %q, want auth-a", first.ID)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: model, Options: combined, Success: true})

	// The bound auth leaves the candidate set entirely, mirroring an unavailable cached auth.
	availableWithoutFirst := []*Auth{{ID: "auth-b"}}
	for _, payload := range []struct {
		name    string
		request []byte
	}{
		{name: "primary prompt", request: []byte(`{"prompt_cache_key":"shared-cache-bucket"}`)},
		{name: "fallback conversation", request: []byte(`{"conversation":{"id":"conversation-session"}}`)},
	} {
		opts := cliproxyexecutor.Options{OriginalRequest: payload.request}
		picked, errPick := selector.Pick(context.Background(), provider, model, opts, availableWithoutFirst)
		if errPick != nil {
			t.Fatalf("%s Pick() error = %v", payload.name, errPick)
		}
		if picked.ID != "auth-b" {
			t.Fatalf("%s alias selected %q, want rebound %q", payload.name, picked.ID, "auth-b")
		}
		selector.OnResult(Result{AuthID: picked.ID, Provider: provider, Model: model, Options: opts, Success: true})
	}
}

func TestSessionAffinitySelectorCachedAuthUnavailableRebindsSharedPromptGroup(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	provider := "responses-cache-rebound-shared"
	model := "gpt-test"

	combinedA := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-a"},"prompt_cache_key":"shared-cache-bucket"}`)}
	combinedB := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-b"},"prompt_cache_key":"shared-cache-bucket"}`)}
	first, err := selector.Pick(context.Background(), provider, model, combinedA, auths)
	if err != nil {
		t.Fatalf("Pick() A error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: model, Options: combinedA, Success: true})
	if first.ID != "auth-a" {
		t.Fatalf("first Pick() = %q, want auth-a", first.ID)
	}
	second, err := selector.Pick(context.Background(), provider, model, combinedB, auths)
	if err != nil {
		t.Fatalf("Pick() B error = %v", err)
	}
	selector.OnResult(Result{AuthID: second.ID, Provider: provider, Model: model, Options: combinedB, Success: true})
	if second.ID != first.ID {
		t.Fatalf("shared prompt key changed auth from %q to %q", first.ID, second.ID)
	}

	availableWithoutFirst := []*Auth{{ID: "auth-b"}}
	for _, payload := range []struct {
		name    string
		request []byte
	}{
		{name: "conversation A", request: []byte(`{"conversation":{"id":"conversation-a"}}`)},
		{name: "conversation B", request: []byte(`{"conversation":{"id":"conversation-b"}}`)},
	} {
		opts := cliproxyexecutor.Options{OriginalRequest: payload.request}
		picked, errPick := selector.Pick(context.Background(), provider, model, opts, availableWithoutFirst)
		if errPick != nil {
			t.Fatalf("%s Pick() error = %v", payload.name, errPick)
		}
		if picked.ID != "auth-b" {
			t.Fatalf("%s alias selected %q, want rebound %q", payload.name, picked.ID, "auth-b")
		}
		selector.OnResult(Result{AuthID: picked.ID, Provider: provider, Model: model, Options: opts, Success: true})
	}
}

func TestSessionAffinitySelectorCachedAuthUnavailableConcurrencyNewerBinding(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	provider := "responses-cache-rebound-concurrent"
	model := "gpt-test"

	combined := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`)}
	first, err := selector.Pick(context.Background(), provider, model, combined, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: model, Options: combined, Success: true})

	// A concurrent request sees the bound auth unavailable and rebounds the group to auth-b.
	availableWithoutFirst := []*Auth{{ID: "auth-b"}}
	picked, err := selector.Pick(context.Background(), provider, model, combined, availableWithoutFirst)
	if err != nil {
		t.Fatalf("unavailable Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: picked.ID, Provider: provider, Model: model, Options: combined, Success: true})
	if picked.ID != "auth-b" {
		t.Fatalf("unavailable Pick() = %q, want auth-b", picked.ID)
	}

	// The newer binding must survive subsequent requests without reverting to auth-a.
	for i := 0; i < 5; i++ {
		got, errPick := selector.Pick(context.Background(), provider, model, combined, availableWithoutFirst)
		if errPick != nil {
			t.Fatalf("concurrent Pick() #%d error = %v", i, errPick)
		}
		if got.ID != "auth-b" {
			t.Fatalf("concurrent Pick() #%d = %q, want auth-b (newer binding preserved)", i, got.ID)
		}
	}
}

type interceptingFallbackSelector struct {
	inner  Selector
	onPick func()
}

func (s *interceptingFallbackSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if s.onPick != nil {
		s.onPick()
	}
	return s.inner.Pick(ctx, provider, model, opts, auths)
}

func TestSessionAffinitySelector_CachedAuthUnavailableConcurrencyFallbackGroupRebind(t *testing.T) {
	fallback := &interceptingFallbackSelector{inner: &RoundRobinSelector{}}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	provider := "responses-fallback-rebound-concurrent"
	model := "gpt-test"

	// 1. Initial request with conversation ID only (bound to auth-a)
	convOpts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-session"}}`)}
	first, err := selector.Pick(context.Background(), provider, model, convOpts, auths)
	if err != nil {
		t.Fatalf("first Pick() error = %v", err)
	}
	if first.ID != "auth-a" {
		t.Fatalf("first Pick() = %q, want auth-a", first.ID)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: model, Options: convOpts, Success: true})

	// 2. Prepare rebind with prompt_cache_key (primary) + conversation.id (fallback).
	// Primary key is absent in cache; fallback key is present.
	// When fallback.Pick is invoked (after selector observes the fallback key),
	// simulate concurrent modification of the fallback group in cache to invalidate expectedGen.
	combinedOpts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`)}
	fallbackKey := provider + "::conv:conversation-session::" + model

	fallback.onPick = func() {
		authID, gen, aliases, ok := selector.cache.GetWithGeneration(fallbackKey)
		if !ok {
			t.Fatalf("expected fallbackKey %q in cache", fallbackKey)
		}
		// Bump generation concurrently
		if !selector.cache.CompareAndReplaceAliases(authID, gen, aliases, authID, fallbackKey) {
			t.Fatalf("CompareAndReplaceAliases failed in onPick")
		}
	}

	availableWithoutFirst := []*Auth{{ID: "auth-b"}}
	picked, err := selector.Pick(context.Background(), provider, model, combinedOpts, availableWithoutFirst)
	if err != nil {
		t.Fatalf("rebind Pick() error = %v", err)
	}
	if picked.ID != "auth-b" {
		t.Fatalf("rebind Pick() = %q, want auth-b", picked.ID)
	}
	selector.OnResult(Result{AuthID: picked.ID, Provider: provider, Model: model, Options: combinedOpts, Success: true})

	// 3. Subsequent request with conversation only must route to rebound auth-b
	fallback.onPick = nil
	convQueryOpts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conversation-session"}}`)}
	queryPicked, err := selector.Pick(context.Background(), provider, model, convQueryOpts, auths)
	if err != nil {
		t.Fatalf("subsequent conversation Pick() error = %v", err)
	}
	if queryPicked.ID != "auth-b" {
		t.Fatalf("subsequent conversation Pick() = %q, want rebound auth-b", queryPicked.ID)
	}
}

func TestSessionAffinitySelector_ABAMutationCycleRejected(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()

	sessionA := "openai::conv:session-aba::gpt-test"
	promptA := "openai::pck:prompt-aba::gpt-test"

	// 1. Initial binding: auth-a (Generation 1)
	cache.SetAliases("auth-a", sessionA, promptA)
	authID, gen1, aliases1, ok := cache.GetWithGeneration(sessionA)
	if !ok || authID != "auth-a" || gen1 == 0 {
		t.Fatalf("initial binding failed: authID=%q, gen=%d, ok=%v", authID, gen1, ok)
	}

	// 2. Mutation to auth-b (Generation 2)
	cache.SetAliases("auth-b", sessionA, promptA)
	authID2, gen2, _, ok2 := cache.GetWithGeneration(sessionA)
	if !ok2 || authID2 != "auth-b" || gen2 <= gen1 {
		t.Fatalf("second binding failed: authID=%q, gen=%d", authID2, gen2)
	}

	// 3. Mutation back to auth-a (Generation 3 - ABA cycle)
	cache.SetAliases("auth-a", sessionA, promptA)
	authID3, gen3, _, ok3 := cache.GetWithGeneration(sessionA)
	if !ok3 || authID3 != "auth-a" || gen3 <= gen2 {
		t.Fatalf("third binding failed: authID=%q, gen=%d", authID3, gen3)
	}

	// 4. Stale CAS attempting to replace gen1 auth-a with auth-c MUST fail
	casSuccess := cache.CompareAndReplaceAliases("auth-a", gen1, aliases1, "auth-c")
	if casSuccess {
		t.Fatal("ABA CAS replacement succeeded unexpectedly with stale generation token")
	}

	// Verify cache still retains gen3 auth-a intact
	currentAuth, currentGen, _, okCurrent := cache.GetWithGeneration(sessionA)
	if !okCurrent || currentAuth != "auth-a" || currentGen != gen3 {
		t.Fatalf("cache was corrupted by failed ABA CAS: auth=%q gen=%d", currentAuth, currentGen)
	}
}

func TestSessionAffinitySelector_PartialGroupSplitAbortsWithoutMutation(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()

	k1 := "openai::conv:session-split-1::gpt-test"
	k2 := "openai::conv:session-split-2::gpt-test"

	// 1. Bind group {k1, k2} to auth-a
	cache.SetAliases("auth-a", k1, k2)
	_, gen1, aliases1, ok := cache.GetWithGeneration(k1)
	if !ok {
		t.Fatal("initial SetAliases failed")
	}

	// 2. Invalidate k1 (splits group, k2 gets updated aliases and gen2)
	cache.Invalidate(k1)

	// 3. Stale CAS expecting {k1, k2} at gen1 must fail because k1 is gone and k2 generation/aliases changed
	casSuccess := cache.CompareAndReplaceAliases("auth-a", gen1, aliases1, "auth-b")
	if casSuccess {
		t.Fatal("partial group split CAS succeeded unexpectedly")
	}

	// k2 must remain bound to auth-a with its updated group
	authK2, _, aliasesK2, okK2 := cache.GetWithGeneration(k2)
	if !okK2 || authK2 != "auth-a" || len(aliasesK2) != 1 || aliasesK2[0] != k2 {
		t.Fatalf("k2 corrupted after aborted CAS: ok=%v auth=%q aliases=%v", okK2, authK2, aliasesK2)
	}
}

func TestSessionAffinitySelector_AdditionalAliasBelongsToOtherActiveGroupAbortsCAS(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()

	g1Session := "openai::conv:group1::gpt-test"
	g2Session := "openai::conv:group2::gpt-test"

	// Group 1 -> auth-a
	cache.SetAliases("auth-a", g1Session)
	_, gen1, aliases1, _ := cache.GetWithGeneration(g1Session)

	// Group 2 -> auth-b
	cache.SetAliases("auth-b", g2Session)

	// CAS attempting to rebind group 1 to auth-c while adding g2Session (which belongs to live group 2) must abort
	casSuccess := cache.CompareAndReplaceAliases("auth-a", gen1, aliases1, "auth-c", g2Session)
	if casSuccess {
		t.Fatal("CAS with conflicting foreign active alias succeeded unexpectedly")
	}

	// Verify group 2 remains intact on auth-b
	authG2, _ := cache.Get(g2Session)
	if authG2 != "auth-b" {
		t.Fatalf("group 2 was corrupted: auth=%q, want auth-b", authG2)
	}
}

type failingTestSelector struct{}

func (f *failingTestSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return nil, fmt.Errorf("fallback selection failed")
}

func TestSessionAffinitySelector_FallbackFailureKeepsOriginalGroupIntact(t *testing.T) {
	failingFallback := &failingTestSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: failingFallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	provider := "responses-fallback-failure"
	model := "gpt-test"
	sessionID := "conv:fallback-fail-test"
	cacheKey := provider + "::" + sessionID + "::" + model

	// Pre-seed cache with auth-a
	selector.cache.Set(cacheKey, "auth-a")
	authBefore, okBefore := selector.cache.Get(cacheKey)
	if !okBefore || authBefore != "auth-a" {
		t.Fatalf("failed to seed cache: got %q, %v", authBefore, okBefore)
	}

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"conversation":{"id":"fallback-fail-test"}}`),
	}

	// Request with only auth-b available (auth-a unavailable).
	// Fallback selector will fail with error.
	availableAuths := []*Auth{{ID: "auth-b"}}
	picked, err := selector.Pick(context.Background(), provider, model, opts, availableAuths)
	if err == nil {
		t.Fatalf("expected error from failing fallback selector, got auth=%v", picked)
	}

	// Cache MUST still contain auth-a (no eager delete).
	authAfter, okAfter := selector.cache.Get(cacheKey)
	if !okAfter || authAfter != "auth-a" {
		t.Fatalf("cache was eagerly deleted or corrupted on fallback failure: got %q, %v", authAfter, okAfter)
	}
}

func TestSessionAffinitySelectorPrimaryTrafficKeepsConversationAliasAlive(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	provider := "responses-active-primary-alias"
	model := "gpt-test"
	combined := []byte(`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`)
	promptOnly := []byte(`{"prompt_cache_key":"shared-cache-bucket"}`)
	conversationOnly := []byte(`{"conversation":{"id":"conversation-session"}}`)

	first, err := selector.Pick(context.Background(), provider, model, cliproxyexecutor.Options{OriginalRequest: combined}, auths)
	if err != nil {
		t.Fatalf("combined Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: model, Options: cliproxyexecutor.Options{OriginalRequest: combined}, Success: true})
	conversationKey := provider + "::conv:conversation-session::" + model
	selector.cache.mu.Lock()
	conversationEntry := selector.cache.entries[conversationKey]
	conversationEntry.expiresAt = time.Now().Add(-time.Second)
	selector.cache.entries[conversationKey] = conversationEntry
	selector.cache.mu.Unlock()

	primary, err := selector.Pick(context.Background(), provider, model, cliproxyexecutor.Options{OriginalRequest: promptOnly}, auths)
	if err != nil {
		t.Fatalf("prompt-only Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: primary.ID, Provider: provider, Model: model, Options: cliproxyexecutor.Options{OriginalRequest: promptOnly}, Success: true})
	if primary.ID != first.ID {
		t.Fatalf("prompt-only auth = %q, want %q", primary.ID, first.ID)
	}
	fallback, err := selector.Pick(context.Background(), provider, model, cliproxyexecutor.Options{OriginalRequest: conversationOnly}, auths)
	if err != nil {
		t.Fatalf("conversation-only Pick() error = %v", err)
	}
	if fallback.ID != first.ID {
		t.Fatalf("conversation alias expired during active primary traffic: got %q, want %q", fallback.ID, first.ID)
	}
}

func TestSessionAffinitySelectorSharedPromptKeyPreservesConversationAliases(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	provider := "responses-shared-prompt-key"
	model := "gpt-test"

	combinedA := []byte(`{"conversation":{"id":"conversation-a"},"prompt_cache_key":"shared-cache-bucket"}`)
	combinedB := []byte(`{"conversation":{"id":"conversation-b"},"prompt_cache_key":"shared-cache-bucket"}`)
	conversationA := []byte(`{"conversation":{"id":"conversation-a"}}`)
	conversationB := []byte(`{"conversation":{"id":"conversation-b"}}`)

	first, err := selector.Pick(context.Background(), provider, model, cliproxyexecutor.Options{OriginalRequest: combinedA}, auths)
	if err != nil {
		t.Fatalf("conversation A combined Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: model, Options: cliproxyexecutor.Options{OriginalRequest: combinedA}, Success: true})
	second, err := selector.Pick(context.Background(), provider, model, cliproxyexecutor.Options{OriginalRequest: combinedB}, auths)
	if err != nil {
		t.Fatalf("conversation B combined Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: second.ID, Provider: provider, Model: model, Options: cliproxyexecutor.Options{OriginalRequest: combinedB}, Success: true})
	if second.ID != first.ID {
		t.Fatalf("shared prompt key changed auth from %q to %q", first.ID, second.ID)
	}
	for name, payload := range map[string][]byte{"conversation A": conversationA, "conversation B": conversationB} {
		picked, errPick := selector.Pick(context.Background(), provider, model, cliproxyexecutor.Options{OriginalRequest: payload}, auths)
		if errPick != nil {
			t.Fatalf("%s Pick() error = %v", name, errPick)
		}
		if picked.ID != first.ID {
			t.Fatalf("%s alias selected %q, want %q", name, picked.ID, first.ID)
		}
	}
}

func TestSessionAffinitySelectorConversationIDContainingPromptMarkerRemainsStable(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	provider := "responses-opaque-conversation"
	model := "gpt-test"
	combined := []byte(`{"conversation":{"id":"a::pck:b"},"prompt_cache_key":"shared-cache-bucket"}`)
	conversationOnly := []byte(`{"conversation":{"id":"a::pck:b"}}`)

	first, err := selector.Pick(context.Background(), provider, model, cliproxyexecutor.Options{OriginalRequest: combined}, auths)
	if err != nil {
		t.Fatalf("combined Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: provider, Model: model, Options: cliproxyexecutor.Options{OriginalRequest: combined}, Success: true})
	second, err := selector.Pick(context.Background(), provider, model, cliproxyexecutor.Options{OriginalRequest: conversationOnly}, auths)
	if err != nil {
		t.Fatalf("conversation-only Pick() error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("opaque conversation alias selected %q, want %q", second.ID, first.ID)
	}
}

func TestSessionCacheSharedPromptKeyCapsStableAliasesByRecency(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()
	const promptKey = "openai::pck:shared-cache-bucket::gpt-test"
	for index := 0; index < 128; index++ {
		conversation := fmt.Sprintf("openai::conv:conversation-%03d::gpt-test", index)
		cache.SetAliases("auth-a", promptKey, conversation)
	}

	cache.mu.RLock()
	defer cache.mu.RUnlock()
	if len(cache.entries) > 65 {
		t.Fatalf("cache entries = %d, want one prompt key plus at most 64 stable aliases", len(cache.entries))
	}
	if _, ok := cache.entries["openai::conv:conversation-127::gpt-test"]; !ok {
		t.Fatal("newest conversation alias was not retained")
	}
	if _, ok := cache.entries["openai::conv:conversation-000::gpt-test"]; ok {
		t.Fatal("oldest conversation alias was retained after stable-alias cap")
	}
}

func TestSessionCacheRotatingPrimaryEvictsObsoleteAliases(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()

	const fallback = "openai::conv:conversation-session::gpt-test"
	for index := 0; index < 16; index++ {
		primary := fmt.Sprintf("openai::pck:cache-%02d::gpt-test", index)
		cache.SetAliases("auth-a", primary, fallback)
	}
	latest := "openai::pck:cache-15::gpt-test"
	oldest := "openai::pck:cache-00::gpt-test"

	cache.mu.RLock()
	defer cache.mu.RUnlock()
	if len(cache.entries) != 2 {
		t.Fatalf("cache entries = %d, want only latest primary and fallback", len(cache.entries))
	}
	if _, ok := cache.entries[latest]; !ok {
		t.Fatalf("latest primary %q was not retained", latest)
	}
	if _, ok := cache.entries[fallback]; !ok {
		t.Fatalf("fallback %q was not retained", fallback)
	}
	if _, ok := cache.entries[oldest]; ok {
		t.Fatalf("obsolete primary %q was retained", oldest)
	}
	if aliases := cache.entries[fallback].aliases; len(aliases) != 2 {
		t.Fatalf("fallback alias group = %#v, want exactly two active identifiers", aliases)
	}
}

func TestSessionAffinitySelector_MultiModelSession(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	// auth-a supports only model-a, auth-b supports only model-b
	authA := &Auth{ID: "auth-a"}
	authB := &Auth{ID: "auth-b"}

	// Same session ID for all requests
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_multi-model-test"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// Request model-a with only auth-a available for that model
	authsForModelA := []*Auth{authA}
	pickedA, err := selector.Pick(context.Background(), "provider", "model-a", opts, authsForModelA)
	if err != nil {
		t.Fatalf("Pick() for model-a error = %v", err)
	}
	if pickedA.ID != "auth-a" {
		t.Fatalf("Pick() for model-a = %q, want auth-a", pickedA.ID)
	}
	selector.OnResult(Result{AuthID: pickedA.ID, Provider: "provider", Model: "model-a", Options: opts, Success: true})

	// Request model-b with only auth-b available for that model
	authsForModelB := []*Auth{authB}
	pickedB, err := selector.Pick(context.Background(), "provider", "model-b", opts, authsForModelB)
	if err != nil {
		t.Fatalf("Pick() for model-b error = %v", err)
	}
	if pickedB.ID != "auth-b" {
		t.Fatalf("Pick() for model-b = %q, want auth-b", pickedB.ID)
	}
	selector.OnResult(Result{AuthID: pickedB.ID, Provider: "provider", Model: "model-b", Options: opts, Success: true})

	// Switch back to model-a - should still get auth-a (separate binding per model)
	pickedA2, err := selector.Pick(context.Background(), "provider", "model-a", opts, authsForModelA)
	if err != nil {
		t.Fatalf("Pick() for model-a (2nd) error = %v", err)
	}
	if pickedA2.ID != "auth-a" {
		t.Fatalf("Pick() for model-a (2nd) = %q, want auth-a", pickedA2.ID)
	}

	// Verify bindings are stable for multiple calls
	for i := 0; i < 5; i++ {
		gotA, _ := selector.Pick(context.Background(), "provider", "model-a", opts, authsForModelA)
		gotB, _ := selector.Pick(context.Background(), "provider", "model-b", opts, authsForModelB)
		if gotA.ID != "auth-a" {
			t.Fatalf("Pick() #%d for model-a = %q, want auth-a", i, gotA.ID)
		}
		if gotB.ID != "auth-b" {
			t.Fatalf("Pick() #%d for model-b = %q, want auth-b", i, gotB.ID)
		}
	}
}

func TestExtractSessionID_MultimodalContent(t *testing.T) {
	t.Parallel()

	// First request generates short hash
	firstRequestPayload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"Hello world"},{"type":"image","source":{"data":"..."}}]}]}`)
	shortHash := ExtractSessionID(nil, firstRequestPayload, nil)
	if shortHash == "" {
		t.Error("ExtractSessionID() first request should return short hash")
	}
	if !strings.HasPrefix(shortHash, "msg:") {
		t.Errorf("ExtractSessionID() = %q, want prefix 'msg:'", shortHash)
	}

	// Multi-turn generates full hash
	multiTurnPayload := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"Hello world"},{"type":"image","source":{"data":"..."}}]},
		{"role":"assistant","content":"I see an image!"},
		{"role":"user","content":"What is it?"}
	]}`)
	fullHash := ExtractSessionID(nil, multiTurnPayload, nil)
	if fullHash == "" {
		t.Error("ExtractSessionID() multimodal multi-turn should return full hash")
	}
	if fullHash == shortHash {
		t.Error("Full hash should differ from short hash")
	}

	// Different user content produces different hash
	differentPayload := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"Different content"}]},
		{"role":"assistant","content":"I see something different!"}
	]}`)
	differentHash := ExtractSessionID(nil, differentPayload, nil)
	if fullHash == differentHash {
		t.Errorf("ExtractSessionID() should produce different hash for different content")
	}
}

func TestSessionAffinitySelector_CrossProviderIsolation(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	authClaude := &Auth{ID: "auth-claude"}
	authGemini := &Auth{ID: "auth-gemini"}

	// Same session ID for both providers
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_cross-provider-test"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// Request via claude provider
	pickedClaude, err := selector.Pick(context.Background(), "claude", "claude-3", opts, []*Auth{authClaude})
	if err != nil {
		t.Fatalf("Pick() for claude error = %v", err)
	}
	if pickedClaude.ID != "auth-claude" {
		t.Fatalf("Pick() for claude = %q, want auth-claude", pickedClaude.ID)
	}
	selector.OnResult(Result{AuthID: pickedClaude.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})

	// Same session but via gemini provider should get different auth
	pickedGemini, err := selector.Pick(context.Background(), "gemini", "gemini-2.5-pro", opts, []*Auth{authGemini})
	if err != nil {
		t.Fatalf("Pick() for gemini error = %v", err)
	}
	if pickedGemini.ID != "auth-gemini" {
		t.Fatalf("Pick() for gemini = %q, want auth-gemini", pickedGemini.ID)
	}
	selector.OnResult(Result{AuthID: pickedGemini.ID, Provider: "gemini", Model: "gemini-2.5-pro", Options: opts, Success: true})

	// Verify both bindings remain stable
	for i := 0; i < 5; i++ {
		gotC, _ := selector.Pick(context.Background(), "claude", "claude-3", opts, []*Auth{authClaude})
		gotG, _ := selector.Pick(context.Background(), "gemini", "gemini-2.5-pro", opts, []*Auth{authGemini})
		if gotC.ID != "auth-claude" {
			t.Fatalf("Pick() #%d for claude = %q, want auth-claude", i, gotC.ID)
		}
		if gotG.ID != "auth-gemini" {
			t.Fatalf("Pick() #%d for gemini = %q, want auth-gemini", i, gotG.ID)
		}
	}
}

func TestSessionCache_GetAndRefresh(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(100 * time.Millisecond)
	defer cache.Stop()

	cache.Set("session1", "auth1")

	// Verify initial value
	got, ok := cache.GetAndRefresh("session1")
	if !ok || got != "auth1" {
		t.Fatalf("GetAndRefresh() = %q, %v, want auth1, true", got, ok)
	}

	// Wait half TTL and access again (should refresh)
	time.Sleep(60 * time.Millisecond)
	got, ok = cache.GetAndRefresh("session1")
	if !ok || got != "auth1" {
		t.Fatalf("GetAndRefresh() after 60ms = %q, %v, want auth1, true", got, ok)
	}

	// Wait another 60ms (total 120ms from original, but TTL refreshed at 60ms)
	// Entry should still be valid because TTL was refreshed
	time.Sleep(60 * time.Millisecond)
	got, ok = cache.GetAndRefresh("session1")
	if !ok || got != "auth1" {
		t.Fatalf("GetAndRefresh() after refresh = %q, %v, want auth1, true (TTL should have been refreshed)", got, ok)
	}

	// Now wait full TTL without access
	time.Sleep(110 * time.Millisecond)
	got, ok = cache.GetAndRefresh("session1")
	if ok {
		t.Fatalf("GetAndRefresh() after expiry = %q, %v, want '', false", got, ok)
	}
}

func TestSessionAffinitySelector_RoundRobinDistribution(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	sessionCount := 12
	counts := make(map[string]int)
	for i := 0; i < sessionCount; i++ {
		payload := []byte(fmt.Sprintf(`{"metadata":{"user_id":"user_xxx_account__session_%08d-0000-0000-0000-000000000000"}}`, i))
		opts := cliproxyexecutor.Options{OriginalRequest: payload}
		got, err := selector.Pick(context.Background(), "provider", "model", opts, auths)
		if err != nil {
			t.Fatalf("Pick() session %d error = %v", i, err)
		}
		counts[got.ID]++
	}

	expected := sessionCount / len(auths)
	for _, auth := range auths {
		got := counts[auth.ID]
		if got != expected {
			t.Errorf("auth %s got %d sessions, want %d (round-robin should distribute evenly)", auth.ID, got, expected)
		}
	}
}

func TestSessionAffinitySelector_Concurrent(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_concurrent-test"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// First pick to establish binding
	first, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Initial Pick() error = %v", err)
	}
	expectedID := first.ID
	selector.OnResult(Result{AuthID: first.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	goroutines := 32
	iterations := 50
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				got, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if got.ID != expectedID {
					select {
					case errCh <- fmt.Errorf("concurrent Pick() returned %q, want %q", got.ID, expectedID):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent Pick() error = %v", err)
	default:
	}
}

func TestSessionAffinitySelector_ConcurrentCacheMissBindsOneAuth(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}, {ID: "auth-c"}}
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_concurrent-cache-miss"}}`)}

	const goroutines = 64
	start := make(chan struct{})
	results := make(chan string, goroutines)
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			auth, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
			if err != nil {
				errs <- err
				return
			}
			results <- auth.ID
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent cache-miss Pick() error = %v", err)
	}
	var expected string
	for authID := range results {
		if expected == "" {
			expected = authID
		}
		if authID != expected {
			t.Fatalf("concurrent cache-miss Pick() returned %q after %q was bound", authID, expected)
		}
	}
	if expected == "" {
		t.Fatal("concurrent cache-miss Pick() returned no auth")
	}
}

func TestExtractSessionIDNativeSignals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers http.Header
		payload string
		want    string
	}{
		{
			name:    "claude code header",
			headers: http.Header{"X-Claude-Code-Session-Id": []string{"claude-session"}},
			want:    "claude:claude-session",
		},
		{
			name:    "lowercase claude code header",
			headers: http.Header{"x-claude-code-session-id": []string{"lowercase-session"}},
			want:    "claude:lowercase-session",
		},
		{
			name:    "codex hyphen header",
			headers: http.Header{"Session-Id": []string{"codex-session"}},
			want:    "codex:codex-session",
		},
		{
			name:    "codex underscore header",
			headers: http.Header{"Session_id": []string{"legacy-codex-session"}},
			want:    "codex:legacy-codex-session",
		},
		{
			name:    "open code session affinity",
			headers: http.Header{"X-Session-Affinity": []string{"ses_opencode"}},
			want:    "affinity:ses_opencode",
		},
		{
			name:    "prompt cache key",
			payload: `{"prompt_cache_key":"prompt-session"}`,
			want:    "pck:prompt-session",
		},
		{
			name:    "responses conversation object",
			payload: `{"conversation":{"id":"conv-object"}}`,
			want:    "conv:conv-object",
		},
		{
			name:    "responses conversation string",
			payload: `{"conversation":"conv-string"}`,
			want:    "conv:conv-string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ExtractSessionID(tt.headers, []byte(tt.payload), nil); got != tt.want {
				t.Fatalf("ExtractSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSessionIDNativeSignalPriority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers http.Header
		payload string
		want    string
	}{
		{
			name: "claude header beats metadata",
			headers: http.Header{
				"X-Claude-Code-Session-Id": []string{"header-session"},
			},
			payload: `{"metadata":{"user_id":"user_hash_account__session_22222222-2222-4222-8222-222222222222"}}`,
			want:    "claude:header-session",
		},
		{
			name: "claude metadata beats codex header",
			headers: http.Header{
				"Session-Id": []string{"codex-session"},
			},
			payload: `{"metadata":{"user_id":"user_hash_account__session_22222222-2222-4222-8222-222222222222"}}`,
			want:    "claude:22222222-2222-4222-8222-222222222222",
		},
		{
			name: "codex header beats x session id and prompt key",
			headers: http.Header{
				"Session-Id":   []string{"codex-session"},
				"X-Session-Id": []string{"generic-session"},
			},
			payload: `{"prompt_cache_key":"prompt-session"}`,
			want:    "codex:codex-session",
		},
		{
			name: "x session id beats affinity",
			headers: http.Header{
				"X-Session-Id":       []string{"generic-session"},
				"X-Session-Affinity": []string{"affinity-session"},
			},
			want: "header:generic-session",
		},
		{
			name:    "prompt cache key beats conversation id",
			payload: `{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`,
			want:    "pck:shared-cache-bucket",
		},
		{
			name: "client request id beats body fallbacks",
			headers: http.Header{
				"X-Client-Request-Id": []string{"client-session"},
			},
			payload: `{"prompt_cache_key":"prompt-session","conversation":{"id":"conversation-session"}}`,
			want:    "clientreq:client-session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ExtractSessionID(tt.headers, []byte(tt.payload), nil); got != tt.want {
				t.Fatalf("ExtractSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSessionIDRejectsInvalidExplicitSignals(t *testing.T) {
	t.Parallel()
	tooLong := strings.Repeat("a", 257)
	tests := []struct {
		name    string
		headers http.Header
		payload string
		want    string
	}{
		{
			name:    "whitespace",
			headers: http.Header{"X-Claude-Code-Session-Id": []string{"   "}},
			want:    "",
		},
		{
			name:    "newline",
			headers: http.Header{"X-Session-Id": []string{"bad\nsession"}},
			want:    "",
		},
		{
			name:    "control character",
			headers: http.Header{"Session-Id": []string{"bad\x00session"}},
			want:    "",
		},
		{
			name:    "too long",
			headers: http.Header{"X-Client-Request-Id": []string{tooLong}},
			want:    "",
		},
		{
			name: "invalid stronger signal falls through",
			headers: http.Header{
				"X-Claude-Code-Session-Id": []string{"bad\nsession"},
				"Session-Id":               []string{"valid-codex"},
			},
			want: "codex:valid-codex",
		},
		{
			name:    "invalid prompt key falls through to conversation",
			payload: `{"prompt_cache_key":"   ","conversation":{"id":"valid-conversation"}}`,
			want:    "conv:valid-conversation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ExtractSessionID(tt.headers, []byte(tt.payload), nil); got != tt.want {
				t.Fatalf("ExtractSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSessionIDClaudeMetadataParsesBeforeBoundingSessionID(t *testing.T) {
	t.Parallel()
	const sessionID = "11111111-1111-4111-8111-111111111111"
	metadata := map[string]string{
		"device_id":         strings.Repeat("d", 64),
		"account_uuid":      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"session_id":        sessionID,
		"organization_uuid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"email":             "user@example.com",
	}

	for _, tt := range []struct {
		name   string
		encode func(any) ([]byte, error)
	}{
		{name: "rich compact json", encode: json.Marshal},
		{name: "pretty printed json", encode: func(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			userID, errMarshal := tt.encode(metadata)
			if errMarshal != nil {
				t.Fatalf("marshal metadata: %v", errMarshal)
			}
			payload, errPayload := json.Marshal(map[string]any{
				"metadata": map[string]string{"user_id": string(userID)},
			})
			if errPayload != nil {
				t.Fatalf("marshal payload: %v", errPayload)
			}
			if got := ExtractSessionID(nil, payload, nil); got != "claude:"+sessionID {
				t.Fatalf("ExtractSessionID() = %q, want %q", got, "claude:"+sessionID)
			}
		})
	}
}

func TestSessionAffinitySelectorUsesRequestPayloadWhenOriginalRequestMissing(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	request := cliproxyexecutor.Request{
		Model:   "gpt-test",
		Payload: []byte(`{"conversation":{"id":"request-only-conversation"},"input":"hello"}`),
	}
	_, opts := cliproxysession.Enrich(request, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	first, errFirst := selector.Pick(context.Background(), "openai", request.Model, opts, auths)
	if errFirst != nil {
		t.Fatalf("first Pick() error = %v", errFirst)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: "openai", Model: request.Model, Options: opts, Success: true})
	second, errSecond := selector.Pick(context.Background(), "openai", request.Model, opts, auths)
	if errSecond != nil {
		t.Fatalf("second Pick() error = %v", errSecond)
	}
	if second.ID != first.ID {
		t.Fatalf("request-only conversation changed auth from %q to %q", first.ID, second.ID)
	}
}

// recordingFallbackSelector wraps a Selector and records the auth IDs passed to
// each Pick call, so tests can assert the fallback only receives available auths.
type recordingFallbackSelector struct {
	inner Selector
	mu    sync.Mutex
	last  []string
}

func (r *recordingFallbackSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	r.mu.Lock()
	r.last = make([]string, 0, len(auths))
	for _, a := range auths {
		r.last = append(r.last, a.ID)
	}
	r.mu.Unlock()
	return r.inner.Pick(ctx, provider, model, opts, auths)
}

func (r *recordingFallbackSelector) lastPick() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.last))
	copy(out, r.last)
	return out
}

// TestSessionAffinitySelector_FallbackReselectReceivesOnlyAvailable is a
// regression test for the fallback reselect path: when the cached auth is no
// longer available, the fallback must only receive the available auths, not the
// full list (which could include the now-unavailable cached auth).
func TestSessionAffinitySelector_FallbackReselectReceivesOnlyAvailable(t *testing.T) {
	t.Parallel()

	rec := &recordingFallbackSelector{inner: &RoundRobinSelector{}}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: rec,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_fallback-reselect-test"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// First pick establishes the binding (fallback receives the full list here).
	first, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})

	// Make the bound auth unavailable so it is filtered out of `available`.
	bound := first.ID
	for _, a := range auths {
		if a.ID == bound {
			a.Unavailable = true
			a.NextRetryAfter = time.Now().Add(time.Hour)
		}
	}

	// Second pick with the SAME full auths list: the cached auth is now filtered
	// out of available, so the fallback reselect path fires. It must receive
	// only the two still-available auths, not the full list.
	if _, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths); err != nil {
		t.Fatalf("Pick() after failover error = %v", err)
	}

	got := rec.lastPick()
	if len(got) != 2 {
		t.Fatalf("fallback reselect received %d auths, want 2 (only available); got %v", len(got), got)
	}
	for _, id := range got {
		if id == bound {
			t.Fatalf("fallback reselect received unavailable cached auth %q; received %v", bound, got)
		}
	}
}

// TestSessionAffinitySelector_RequestScopedExclusionBreaksCarousel is a
// regression test for the live bug where an auth that just failed (429/5xx/empty)
// was re-picked for the SAME request repeatedly. In home/mixed mode per-attempt
// failures never update local availability (reportHomeResult does not set a local
// cooldown), so `getAvailableAuths` kept reporting the freshly-failed auth as
// available and the session-affinity cache returned it on every retry. The fix
// threads the request-scoped set of failed auth IDs through to the selector so a
// failed auth is never re-picked for the remainder of that request, even though
// it is still locally "available".
func TestSessionAffinitySelector_RequestScopedExclusionBreaksCarousel(t *testing.T) {
	t.Parallel()

	rec := &recordingFallbackSelector{inner: &RoundRobinSelector{}}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: rec,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_carousel-test"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	first, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})

	failed := first.ID
	opts.Metadata = map[string]any{
		cliproxyexecutor.ExcludedAuthIDsMetadataKey: map[string]struct{}{failed: {}},
	}

	for attempt := 0; attempt < 20; attempt++ {
		got, errPick := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
		if errPick != nil {
			t.Fatalf("Pick() attempt %d error = %v", attempt, errPick)
		}
		if got.ID == failed {
			t.Fatalf("attempt %d re-picked auth %q that already failed in this request; want a different auth", attempt, failed)
		}
	}
}

func TestSessionCache_StopConcurrent(t *testing.T) {
	t.Parallel()
	for iter := 0; iter < 100; iter++ {
		cache := NewSessionCache(time.Minute)
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cache.Stop()
			}()
		}
		wg.Wait()
	}
}

type mockStoppableSelector struct {
	stopped bool
}

func (m *mockStoppableSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return nil, nil
}

func (m *mockStoppableSelector) Stop() {
	m.stopped = true
}

func TestManagerSetSelectorStopsReplacedStoppableSelector(t *testing.T) {
	t.Parallel()
	mockSelector := &mockStoppableSelector{}
	manager := NewManager(nil, mockSelector, nil)

	manager.SetSelector(&RoundRobinSelector{})

	if !mockSelector.stopped {
		t.Fatal("expected previous StoppableSelector to be stopped when replaced via SetSelector")
	}
}

type zeroSizeSelectorA struct {
	stopped *bool
}

func (z zeroSizeSelectorA) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return nil, nil
}

func (z zeroSizeSelectorA) Stop() {
	if z.stopped != nil {
		*z.stopped = true
	}
}

type zeroSizeSelectorB struct{}

func (z zeroSizeSelectorB) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return nil, nil
}

func TestManagerSetSelectorDifferentZeroSizedSelectors(t *testing.T) {
	t.Parallel()
	stoppedA := false
	selA := zeroSizeSelectorA{stopped: &stoppedA}
	selB := zeroSizeSelectorB{}

	manager := NewManager(nil, selA, nil)
	manager.SetSelector(selB)

	if !stoppedA {
		t.Fatal("expected zeroSizeSelectorA to be stopped when replaced by zeroSizeSelectorB")
	}
	if manager.Selector() != selB {
		t.Fatalf("expected manager selector to be selB, got %#v", manager.Selector())
	}
}

type uncomparableSelector struct {
	fn func()
}

func (u uncomparableSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return nil, nil
}

func TestManagerSetSelectorUncomparableTypes(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)

	sel1 := uncomparableSelector{fn: func() {}}
	sel2 := uncomparableSelector{fn: func() {}}

	// Setting uncomparable types must not panic
	manager.SetSelector(sel1)
	manager.SetSelector(sel2)
	manager.SetSelector(nil)
}

func TestManagerSetSelectorSameInstanceDoesNotStop(t *testing.T) {
	t.Parallel()
	mockSelector := &mockStoppableSelector{}
	manager := NewManager(nil, mockSelector, nil)

	// Setting the same instance should be a no-op and not call Stop
	manager.SetSelector(mockSelector)
	if mockSelector.stopped {
		t.Fatal("setting the same selector instance unexpectedly called Stop")
	}
}

func TestManagerSetSelectorConcurrent(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				sel := &mockStoppableSelector{}
				manager.SetSelector(sel)
			}
		}()
	}
	wg.Wait()
}
