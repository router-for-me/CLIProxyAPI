package cache

import (
	"bytes"
	"context"
	"testing"
)

func useFakeClaudeThinkingReplayKVClient(t *testing.T, client *fakeKimiThinkingReplayKVClient) {
	t.Helper()
	previous := currentClaudeThinkingReplayKVClient
	currentClaudeThinkingReplayKVClient = func() (kimiThinkingReplayKVClient, bool, error) {
		return client, true, nil
	}
	t.Cleanup(func() {
		currentClaudeThinkingReplayKVClient = previous
	})
}

func TestClaudeThinkingReplayAppendsAssistantTurns(t *testing.T) {
	client := newFakeKimiThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client)

	const modelFamily = "claude:auth:model"
	const sessionKey = "execution:multi-turn"
	first := []byte(`[{"type":"thinking","thinking":"first","signature":"sig-1"},{"type":"tool_use","id":"toolu-1","name":"Read","input":{"path":"one"}}]`)
	second := []byte(`[{"type":"thinking","thinking":"second","signature":"sig-2"},{"type":"tool_use","id":"toolu-2","name":"Read","input":{"path":"two"}}]`)

	if !CacheClaudeThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, first) {
		t.Fatal("failed to seed first Claude replay turn")
	}
	_, snapshot, found, errGet := GetClaudeThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found {
		t.Fatalf("initial Claude replay read = found %v, error %v", found, errGet)
	}
	replaced, errReplace := ReplaceClaudeThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snapshot, second)
	if errReplace != nil || !replaced {
		t.Fatalf("append Claude replay turn = replaced %v, error %v", replaced, errReplace)
	}

	contents, found, errGet := GetClaudeThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found || len(contents) != 2 {
		t.Fatalf("Claude replay contents = %d, found %v, error %v; want two turns", len(contents), found, errGet)
	}
	if !bytes.Equal(contents[0], first) || !bytes.Equal(contents[1], second) {
		t.Fatalf("Claude replay contents lost ordering: got %s / %s", contents[0], contents[1])
	}
}

func TestClaudeThinkingReplayClearDoesNotClearKimiState(t *testing.T) {
	previousClaudeClient := currentClaudeThinkingReplayKVClient
	previousKimiClient := currentKimiThinkingReplayKVClient
	currentClaudeThinkingReplayKVClient = func() (kimiThinkingReplayKVClient, bool, error) {
		return nil, false, nil
	}
	currentKimiThinkingReplayKVClient = func() (kimiThinkingReplayKVClient, bool, error) {
		return nil, false, nil
	}
	t.Cleanup(func() {
		currentClaudeThinkingReplayKVClient = previousClaudeClient
		currentKimiThinkingReplayKVClient = previousKimiClient
	})
	ClearClaudeThinkingReplayCache()
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearClaudeThinkingReplayCache)
	t.Cleanup(ClearKimiThinkingReplayCache)

	const modelFamily = "shared-model"
	const sessionKey = "execution:shared-session"
	kimiContent := []byte(`[{"type":"thinking","signature":"kimi"}]`)
	claudeContent := []byte(`[{"type":"thinking","signature":"claude"}]`)
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, kimiContent) {
		t.Fatal("failed to seed Kimi replay state")
	}
	if !CacheClaudeThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, claudeContent) {
		t.Fatal("failed to seed Claude replay state")
	}

	ClearClaudeThinkingReplayCache()

	gotKimi, foundKimi, errKimi := GetKimiThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errKimi != nil || !foundKimi || !bytes.Equal(gotKimi, kimiContent) {
		t.Fatalf("Kimi replay after Claude clear = %s, found %v, error %v; want preserved state", gotKimi, foundKimi, errKimi)
	}
	gotClaude, foundClaude, errClaude := GetClaudeThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errClaude != nil || foundClaude || len(gotClaude) != 0 {
		t.Fatalf("Claude replay after Claude clear = %d turns, found %v, error %v; want cleared state", len(gotClaude), foundClaude, errClaude)
	}
}
