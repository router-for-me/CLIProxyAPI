package auth

import "testing"

func expectedFillFirstAuthID(seed uint64, auths []*Auth) string {
	var bestID string
	var bestRank uint64
	found := false
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		rank := fillFirstShuffleRank(seed, auth.ID)
		if !found || rank < bestRank || (rank == bestRank && auth.ID < bestID) {
			bestRank = rank
			bestID = auth.ID
			found = true
		}
	}
	return bestID
}

func TestFillFirstShuffleRankStable(t *testing.T) {
	const seed uint64 = 123456789
	const authID = "auth-1"

	got1 := fillFirstShuffleRank(seed, authID)
	got2 := fillFirstShuffleRank(seed, authID)
	if got1 != got2 {
		t.Fatalf("expected stable rank, got %d and %d", got1, got2)
	}
}

func TestFillFirstShuffleRankDiffersForDifferentSeeds(t *testing.T) {
	const authID = "auth-1"

	got1 := fillFirstShuffleRank(1, authID)
	got2 := fillFirstShuffleRank(2, authID)
	if got1 == got2 {
		t.Fatalf("expected different ranks for different seeds, got %d", got1)
	}
}

func TestFillFirstShuffleSeedIsNonZeroAndStable(t *testing.T) {
	seed1 := fillFirstShuffleSeed()
	seed2 := fillFirstShuffleSeed()
	if seed1 == 0 {
		t.Fatal("expected non-zero seed")
	}
	if seed1 != seed2 {
		t.Fatalf("expected stable process-local seed, got %d and %d", seed1, seed2)
	}
}
