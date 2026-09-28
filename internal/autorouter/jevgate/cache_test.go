package jevgate

import "testing"

func TestCacheRoundTrip(t *testing.T) {
	c := NewCache(8)
	st := BuildState(StateInput{LatestUserText: "hi"})
	key := c.Key("openai", "r1", st, "jev-1.13.0", QuestionHash())

	if _, hit := c.Get(key); hit {
		t.Fatal("empty cache reported a hit")
	}
	c.Put(key, Verdict{Choice: "simple", Confidence: 0.9, Verdict: VerdictAccepted})
	got, hit := c.Get(key)
	if !hit {
		t.Fatal("expected a hit after Put")
	}
	if got.Choice != "simple" || got.Confidence != 0.9 {
		t.Errorf("got %+v", got)
	}
}

func TestCacheKeyVariesWithEveryInput(t *testing.T) {
	c := NewCache(8)
	st := BuildState(StateInput{LatestUserText: "hi"})
	base := c.Key("openai", "r1", st, "jev-1.13.0", QuestionHash())

	cases := map[string]string{
		"format":       c.Key("claude", "r1", st, "jev-1.13.0", QuestionHash()),
		"routerID":     c.Key("openai", "r2", st, "jev-1.13.0", QuestionHash()),
		"model":        c.Key("openai", "r1", st, "jev-preview", QuestionHash()),
		"questionHash": c.Key("openai", "r1", st, "jev-1.13.0", "different"),
		"state":        c.Key("openai", "r1", BuildState(StateInput{LatestUserText: "bye"}), "jev-1.13.0", QuestionHash()),
	}
	for name, key := range cases {
		if key == base {
			t.Errorf("cache key did not change with %s", name)
		}
	}
}

func TestCacheEvictsOldestAtCapacity(t *testing.T) {
	c := NewCache(2)
	k1 := c.Key("openai", "r", BuildState(StateInput{LatestUserText: "a"}), "m", "q")
	k2 := c.Key("openai", "r", BuildState(StateInput{LatestUserText: "b"}), "m", "q")
	k3 := c.Key("openai", "r", BuildState(StateInput{LatestUserText: "c"}), "m", "q")

	c.Put(k1, Verdict{Choice: "simple"})
	c.Put(k2, Verdict{Choice: "simple"})
	c.Put(k3, Verdict{Choice: "simple"}) // evicts k1

	if _, hit := c.Get(k1); hit {
		t.Error("k1 should have been evicted")
	}
	if _, hit := c.Get(k3); !hit {
		t.Error("k3 should be present")
	}
}

func TestCacheKeyDoesNotCollideAcrossFields(t *testing.T) {
	// "ab"+"c" must not hash the same as "a"+"bc": the separator writes are what
	// prevent that, so this guards the delimiter.
	c := NewCache(8)
	st := BuildState(StateInput{LatestUserText: "x"})
	left := c.Key("ab", "c", st, "m", "q")
	right := c.Key("a", "bc", st, "m", "q")
	if left == right {
		t.Error("cache key is ambiguous across field boundaries")
	}
}

func TestNewCacheDefaultsCapacity(t *testing.T) {
	c := NewCache(0)
	for i := 0; i < defaultCacheCapacity+10; i++ {
		key := c.Key("f", "r", BuildState(StateInput{LatestUserText: string(rune('a'+i%26)) + string(rune('A'+i/26))}), "m", "q")
		c.Put(key, Verdict{})
	}
	if c.len() > defaultCacheCapacity {
		t.Errorf("len = %d, want <= %d", c.len(), defaultCacheCapacity)
	}
}
