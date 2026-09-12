package cache

import "testing"

func TestBoundedLRUEvictsLeastRecentlyUsed(t *testing.T) {
	var evicted []string
	cache := NewBoundedLRU[string, string](2, func(key, value string) {
		evicted = append(evicted, key+"="+value)
	})

	if got := cache.GetOrAdd("a", func() string { return "A" }); got != "A" {
		t.Fatalf("first value = %q, want A", got)
	}
	cache.GetOrAdd("b", func() string { return "B" })
	if got, found := cache.Get("a"); !found || got != "A" {
		t.Fatalf("Get(a) = %q/%t, want A/true", got, found)
	}
	cache.GetOrAdd("c", func() string { return "C" })

	if _, found := cache.Get("b"); found {
		t.Fatal("least recently used entry b was not evicted")
	}
	if got := cache.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	if len(evicted) != 1 || evicted[0] != "b=B" {
		t.Fatalf("evicted = %v, want [b=B]", evicted)
	}
}

func TestBoundedLRUCreatesOneValuePerKeyConcurrently(t *testing.T) {
	cache := NewBoundedLRU[string, int](2, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	results := make(chan int, 2)
	creates := make(chan struct{}, 2)

	create := func() int {
		creates <- struct{}{}
		close(started)
		<-release
		return 42
	}
	go func() { results <- cache.GetOrAdd("key", create) }()
	<-started
	go func() { results <- cache.GetOrAdd("key", func() int { creates <- struct{}{}; return 7 }) }()
	close(release)

	for range 2 {
		if got := <-results; got != 42 {
			t.Fatalf("cached value = %d, want 42", got)
		}
	}
	if got := len(creates); got != 1 {
		t.Fatalf("create calls = %d, want 1", got)
	}
}
