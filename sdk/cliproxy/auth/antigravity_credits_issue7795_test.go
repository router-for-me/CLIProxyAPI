package auth

import "testing"

func TestAntigravityCreditsHintDoesNotCrossRegistrationEpoch(t *testing.T) {
	const authID = "antigravity-issue7795-hint-reuse.json"
	antigravityCreditsHintByAuth.Delete(newAntigravityCreditsHintCacheScope(authID, antigravityCreditsRegistrationScope(1)))
	antigravityCreditsHintByAuth.Delete(newAntigravityCreditsHintCacheScope(authID, antigravityCreditsRegistrationScope(2)))
	t.Cleanup(func() {
		antigravityCreditsHintByAuth.Delete(newAntigravityCreditsHintCacheScope(authID, antigravityCreditsRegistrationScope(1)))
		antigravityCreditsHintByAuth.Delete(newAntigravityCreditsHintCacheScope(authID, antigravityCreditsRegistrationScope(2)))
	})

	SetAntigravityCreditsHint(authID, AntigravityCreditsHint{Known: true, Available: false}, 1)
	if hint, ok := GetAntigravityCreditsHint(authID, 1); !ok || !hint.Known || hint.Available {
		t.Fatalf("epoch 1 hint = %#v (ok=%v), want known unavailable hint", hint, ok)
	}
	if hint, ok := GetAntigravityCreditsHint(authID, 2); ok || hint.Known {
		t.Fatalf("epoch 2 inherited hint = %#v (ok=%v)", hint, ok)
	}
	if key1, key2 := antigravityCreditsHintKey(authID, antigravityCreditsRegistrationScope(1)), antigravityCreditsHintKey(authID, antigravityCreditsRegistrationScope(2)); key1 == key2 {
		t.Fatalf("Home KV hint keys collide across epochs: %s", key1)
	}

}
