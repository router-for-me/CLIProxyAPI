package auth

import (
	"bytes"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestApplyOAuthModelAliasWithThinking_ThinkingEnabled(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"antigravity": {{
			Name:          "claude-sonnet-4-5-thinking",
			Alias:         "claude-sonnet-4-5",
			ToThinking:    "claude-sonnet-4-5-thinking",
			ToNonThinking: "claude-sonnet-4-5",
		}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := createAuthForChannel("antigravity")

	result := mgr.applyOAuthModelAliasWithThinking(auth, "claude-sonnet-4-5", true)
	if result.UpstreamModel != "claude-sonnet-4-5-thinking" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "claude-sonnet-4-5-thinking")
	}
	if result.StripThinkingResponse {
		t.Error("StripThinkingResponse should be false for thinking request")
	}
}

func TestApplyOAuthModelAliasWithThinking_NonThinkingEnabled(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"antigravity": {{
			Name:          "claude-sonnet-4-5-thinking",
			Alias:         "claude-sonnet-4-5",
			ToThinking:    "claude-sonnet-4-5-thinking",
			ToNonThinking: "claude-sonnet-4-5",
		}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := createAuthForChannel("antigravity")

	result := mgr.applyOAuthModelAliasWithThinking(auth, "claude-sonnet-4-5", false)
	if result.UpstreamModel != "claude-sonnet-4-5" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "claude-sonnet-4-5")
	}
}

func TestApplyOAuthModelAliasWithThinking_StripThinking(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"antigravity": {{
			Name:                  "claude-opus-4-5-thinking",
			Alias:                 "claude-opus-4-5",
			ToThinking:            "claude-opus-4-5-thinking",
			ToNonThinking:         "claude-opus-4-5-thinking",
			StripThinkingResponse: true,
		}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := createAuthForChannel("antigravity")

	result := mgr.applyOAuthModelAliasWithThinking(auth, "claude-opus-4-5", false)
	if result.UpstreamModel != "claude-opus-4-5-thinking" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "claude-opus-4-5-thinking")
	}
	if !result.StripThinkingResponse {
		t.Error("StripThinkingResponse should be true for non-thinking request with strip config")
	}
}

func TestStreamRewriter_StripsThinkingEvents(t *testing.T) {
	t.Parallel()

	rewriter := NewStreamRewriter(StreamRewriteOptions{StripThinking: true})

	chunk := []byte("event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")

	out := rewriter.RewriteChunk(chunk)

	if bytes.Contains(out, []byte(`"index":0`)) || bytes.Contains(out, []byte(`"thinking"`)) {
		t.Fatalf("expected thinking block to be stripped, got output: %s", string(out))
	}
	if !bytes.Contains(out, []byte(`"index":1`)) || !bytes.Contains(out, []byte(`"text"`)) {
		t.Fatalf("expected non-thinking block to remain, got output: %s", string(out))
	}
}

func TestStripThinkingBlocksFromResponse(t *testing.T) {
	t.Parallel()

	data := []byte(`{"content":[{"type":"thinking","thinking":"test"},{"type":"text","text":"hello"}]}`)
	result := stripThinkingBlocksFromResponse(data)

	if bytes.Contains(result, []byte(`"thinking"`)) {
		t.Fatalf("expected thinking block to be stripped, got: %s", string(result))
	}
	if !bytes.Contains(result, []byte(`"text"`)) {
		t.Fatalf("expected text block to remain, got: %s", string(result))
	}
}
