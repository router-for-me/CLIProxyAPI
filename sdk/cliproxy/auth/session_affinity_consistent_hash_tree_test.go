package auth

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Subagent trees and conversation aliases under consistent hashing.

// A subagent spawned by a subagent (X-Claude-Code-Parent-Agent-Id) should land on the
// root session's credential, as it does with the per-process binding cache.
func TestSessionAffinityConsistentHashNestedClaudeSubagentFollowsRoot(t *testing.T) {
	auths := consistentHashAuths("claude-a", "claude-b", "claude-c", "claude-d")
	nestedOpts := func(root string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{
			Headers: http.Header{
				"X-Claude-Code-Session-Id":      []string{root},
				"X-Claude-Code-Agent-Id":        []string{"nested"},
				"X-Claude-Code-Parent-Agent-Id": []string{"sub"},
			},
			OriginalRequest: []byte(`{"messages":[{"role":"user","content":"nested"}]}`),
			Metadata:        map[string]any{},
		}
	}
	subOpts := func(root string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{
			Headers: http.Header{
				"X-Claude-Code-Session-Id": []string{root},
				"X-Claude-Code-Agent-Id":   []string{"sub"},
			},
			OriginalRequest: []byte(`{"messages":[{"role":"user","content":"sub"}]}`),
			Metadata:        map[string]any{},
		}
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("consistentHash=%v", enabled), func(t *testing.T) {
			selector := newConsistentHashReplica(t, &RoundRobinSelector{}, enabled)
			mismatch := 0
			for i := 0; i < 40; i++ {
				root := fmt.Sprintf("nested-root-%d", i)
				rootAuth := pickConsistentHashAuth(t, selector, claudeSessionOpts(root), auths)
				subAuth := pickConsistentHashAuth(t, selector, subOpts(root), auths)
				nestedAuth := pickConsistentHashAuth(t, selector, nestedOpts(root), auths)
				if subAuth != rootAuth || nestedAuth != rootAuth {
					mismatch++
				}
			}
			t.Logf("consistentHash=%v: %d/40 roots where sub or nested subagent left the root credential", enabled, mismatch)
			if mismatch > 0 {
				t.Errorf("nested subagents left the root credential in %d/40 sessions", mismatch)
			}
		})
	}
}

// A thread spawned by a spawned thread names its immediate parent, but Session_id still names
// the root session; every thread of the tree should land with the root.
func TestSessionAffinityConsistentHashNestedCodexThreadFollowsRoot(t *testing.T) {
	auths := consistentHashAuths("codex-a", "codex-b", "codex-c", "codex-d")
	codexOpts := func(root, thread, parent string) cliproxyexecutor.Options {
		headers := http.Header{"Session_id": []string{root}}
		if thread != "" {
			headers.Set("Thread_id", thread)
		}
		if parent != "" {
			headers.Set("X-Codex-Parent-Thread-Id", parent)
		}
		return cliproxyexecutor.Options{
			Headers:         headers,
			OriginalRequest: []byte(`{"input":[{"role":"user","content":"task"}]}`),
			Metadata:        map[string]any{},
		}
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("consistentHash=%v", enabled), func(t *testing.T) {
			selector := newConsistentHashReplica(t, &RoundRobinSelector{}, enabled)
			mismatch := 0
			for i := 0; i < 40; i++ {
				root := fmt.Sprintf("codex-root-%d", i)
				rootAuth := pickConsistentHashAuth(t, selector, codexOpts(root, "", ""), auths)
				childAuth := pickConsistentHashAuth(t, selector, codexOpts(root, root+"-a", root), auths)
				nestedAuth := pickConsistentHashAuth(t, selector, codexOpts(root, root+"-b", root+"-a"), auths)
				if childAuth != rootAuth || nestedAuth != rootAuth {
					mismatch++
				}
			}
			if mismatch > 0 {
				t.Errorf("nested Codex threads left the root credential in %d/40 sessions", mismatch)
			}
		})
	}
}

// A conversation whose first request carries prompt_cache_key and a conversation ID, and whose
// later requests carry only the conversation, stays on one credential, on any replica.
func TestSessionAffinityConsistentHashConversationAliasLinksRequests(t *testing.T) {
	auths := consistentHashAuths("oa-a", "oa-b", "oa-c", "oa-d")
	selector := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	other := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	for i := 0; i < 40; i++ {
		both := cliproxyexecutor.Options{
			OriginalRequest: []byte(fmt.Sprintf(`{"prompt_cache_key":"pck-%d","conversation":"conv-%d","input":[{"role":"user","content":"hi"}]}`, i, i)),
			Metadata:        map[string]any{},
		}
		only := cliproxyexecutor.Options{
			OriginalRequest: []byte(fmt.Sprintf(`{"conversation":"conv-%d","input":[{"role":"user","content":"hi"}]}`, i)),
			Metadata:        map[string]any{},
		}
		first := pickConsistentHashAuth(t, selector, both, auths)
		if later := pickConsistentHashAuth(t, other, only, auths); later != first {
			t.Fatalf("conversation %d: %s with prompt_cache_key, %s without", i, first, later)
		}
	}
}

func treeClaudeOpts(root, agent, parent string) cliproxyexecutor.Options {
	h := http.Header{"X-Claude-Code-Session-Id": []string{root}}
	if agent != "" {
		h["X-Claude-Code-Agent-Id"] = []string{agent}
	}
	if parent != "" {
		h["X-Claude-Code-Parent-Agent-Id"] = []string{parent}
	}
	return cliproxyexecutor.Options{Headers: h, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"x"}]}`), Metadata: map[string]any{}}
}

func treeCodexOpts(sid, tid, parent, agentName string) cliproxyexecutor.Options {
	h := http.Header{"Session_id": []string{sid}}
	if tid != "" {
		h["Thread_id"] = []string{tid}
	}
	if parent != "" || agentName != "" {
		meta := fmt.Sprintf(`{"parent_thread_id":%q,"subagent_kind":"thread_spawn"`, parent)
		if agentName != "" {
			meta += fmt.Sprintf(`,"agent_name":%q`, agentName)
		}
		h["X-Codex-Turn-Metadata"] = []string{meta + "}"}
	}
	return cliproxyexecutor.Options{Headers: h, OriginalRequest: []byte(`{"input":"x"}`), Metadata: map[string]any{}}
}

// Deeper trees, each request picked on a different replica and on one replica: Claude agents
// three levels deep, Codex threads spawned through turn metadata, and Codex named agents.
func TestSessionAffinityConsistentHashDeepTreesFollowRootAcrossReplicas(t *testing.T) {
	auths := consistentHashAuths("t-a", "t-b", "t-c", "t-d")
	trees := map[string]func(i int) []cliproxyexecutor.Options{
		"claude": func(i int) []cliproxyexecutor.Options {
			r := fmt.Sprintf("ctree-%d", i)
			return []cliproxyexecutor.Options{treeClaudeOpts(r, "", ""), treeClaudeOpts(r, "sub", ""), treeClaudeOpts(r, "nested", "sub"), treeClaudeOpts(r, "deeper", "nested")}
		},
		"codex-threads": func(i int) []cliproxyexecutor.Options {
			r := fmt.Sprintf("xtree-%d", i)
			return []cliproxyexecutor.Options{treeCodexOpts(r, "", "", ""), treeCodexOpts(r, r+"-A", r, ""), treeCodexOpts(r, r+"-B", r+"-A", ""), treeCodexOpts(r, r+"-C", r+"-B", "")}
		},
		"codex-named": func(i int) []cliproxyexecutor.Options {
			r := fmt.Sprintf("ntree-%d", i)
			return []cliproxyexecutor.Options{treeCodexOpts(r, "", "", ""), treeCodexOpts(r, r+"-A", r, "worker"), treeCodexOpts(r, r+"-B", r+"-A", "helper")}
		},
	}
	for name, requests := range trees {
		for _, replicaCount := range []int{1, 4} {
			t.Run(fmt.Sprintf("%s/replicas=%d", name, replicaCount), func(t *testing.T) {
				replicas := make([]*SessionAffinitySelector, replicaCount)
				for k := range replicas {
					replicas[k] = newConsistentHashReplica(t, &RoundRobinSelector{}, true)
				}
				for i := 0; i < 40; i++ {
					root := ""
					for k, opts := range requests(i) {
						got := pickConsistentHashAuth(t, replicas[k%replicaCount], opts, auths)
						if k == 0 {
							root = got
						} else if got != root {
							t.Fatalf("tree %d: request %d picked %s, root picked %s", i, k, got, root)
						}
					}
				}
			})
		}
	}
}

// A Claude Code session S that names a parent session P (in the body, or as parent_session_id
// in metadata.user_id) is ranked under P, and so are S's agents and their nested agents.
func TestSessionAffinityConsistentHashNamedParentSessionIsTheRoot(t *testing.T) {
	auths := consistentHashAuths("p-a", "p-b", "p-c", "p-d")
	bodyParent := func(sid, agent, parentAgent, parent string) cliproxyexecutor.Options {
		opts := treeClaudeOpts(sid, agent, parentAgent)
		opts.OriginalRequest = []byte(fmt.Sprintf(`{"extra_body":{"parent_session":%q},"messages":[{"role":"user","content":"x"}]}`, parent))
		return opts
	}
	metadataParent := func(sid, agent, parentAgent, parent string) cliproxyexecutor.Options {
		h := http.Header{}
		if agent != "" {
			h["X-Claude-Code-Agent-Id"] = []string{agent}
		}
		if parentAgent != "" {
			h["X-Claude-Code-Parent-Agent-Id"] = []string{parentAgent}
		}
		userID := fmt.Sprintf(`{"session_id":%q,"parent_session_id":%q}`, sid, parent)
		body := fmt.Sprintf(`{"metadata":{"user_id":%s},"messages":[{"role":"user","content":"x"}]}`, strconv.Quote(userID))
		return cliproxyexecutor.Options{Headers: h, OriginalRequest: []byte(body), Metadata: map[string]any{}}
	}
	for name, build := range map[string]func(sid, agent, parentAgent, parent string) cliproxyexecutor.Options{
		"body": bodyParent, "metadata.user_id": metadataParent,
	} {
		t.Run(name, func(t *testing.T) {
			first := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
			second := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
			for i := 0; i < 40; i++ {
				parent, sid := fmt.Sprintf("P-%d", i), fmt.Sprintf("S-%d", i)
				want := pickConsistentHashAuth(t, first, treeClaudeOpts(parent, "", ""), auths)
				for k, opts := range []cliproxyexecutor.Options{
					build(sid, "", "", parent), build(sid, "sub", "", parent), build(sid, "nested", "sub", parent),
				} {
					if got := pickConsistentHashAuth(t, second, opts, auths); got != want {
						t.Fatalf("session %d request %d picked %s, parent session picked %s", i, k, got, want)
					}
				}
			}
		})
	}
}
