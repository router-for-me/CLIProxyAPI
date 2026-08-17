package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/pow"
)

// ComputePow solves a DeepSeekHashV1 PoW challenge using the pure-Go solver.
func ComputePow(ctx context.Context, challenge map[string]any) (int64, error) {
	algo, _ := challenge["algorithm"].(string)
	if algo != "DeepSeekHashV1" {
		return 0, errors.New("unsupported algorithm")
	}
	challengeStr, _ := challenge["challenge"].(string)
	salt, _ := challenge["salt"].(string)
	expireAt := toInt64(challenge["expire_at"], 1680000000)
	difficulty := toInt64FromFloat(challenge["difficulty"], 144000)

	return pow.SolvePow(ctx, challengeStr, salt, expireAt, difficulty)
}

// BuildPowHeader serializes {algorithm,challenge,salt,answer,signature,target_path}
// as base64(JSON). This is the map-based version used by the client layer; the
// pow package also has a struct-based BuildPowHeader for callers that prefer it.
func BuildPowHeader(challenge map[string]any, answer int64) (string, error) {
	payload := map[string]any{
		"algorithm":   challenge["algorithm"],
		"challenge":   challenge["challenge"],
		"salt":        challenge["salt"],
		"answer":      answer,
		"signature":   challenge["signature"],
		"target_path": challenge["target_path"],
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// powPrefetchFreshnessMargin is how many seconds before expire_at a cached
// challenge is considered stale.
const powPrefetchFreshnessMargin = 30

type powChallengeEntry struct {
	challenge map[string]any
}

type powChallengeCache struct {
	mu      sync.Mutex
	entries map[string]powChallengeEntry
}

func newPowChallengeCache() *powChallengeCache {
	return &powChallengeCache{entries: map[string]powChallengeEntry{}}
}

func (c *powChallengeCache) key(accountID, targetPath string) string {
	return accountID + ":" + targetPath
}

func (c *powChallengeCache) get(accountID, targetPath string) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[c.key(accountID, targetPath)]
	if !ok {
		return nil, false
	}
	if !isFreshChallenge(entry.challenge) {
		delete(c.entries, c.key(accountID, targetPath))
		return nil, false
	}
	delete(c.entries, c.key(accountID, targetPath))
	return entry.challenge, true
}

func (c *powChallengeCache) set(accountID, targetPath string, challenge map[string]any) {
	if challenge == nil || !isFreshChallenge(challenge) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[c.key(accountID, targetPath)] = powChallengeEntry{challenge: challenge}
}

func isFreshChallenge(challenge map[string]any) bool {
	if challenge == nil {
		return false
	}
	expireAt := toInt64(challenge["expire_at"], 0)
	if expireAt == 0 {
		return false
	}
	return expireAt > time.Now().Unix()+powPrefetchFreshnessMargin
}

func toFloat64(v any, d float64) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return d
	}
}

func toInt64(v any, d int64) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	default:
		return d
	}
}

// toInt64FromFloat is identical to toInt64; named separately for clarity of intent.
func toInt64FromFloat(v any, d int64) int64 {
	return toInt64(v, d)
}
