# Auto Router F1 — Scorer v2 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Improve Auto Router tier-classification accuracy with structure-aware (code fences) and role-aware (user-turn token share) heuristics, and cut scorer allocations with a single text-normalization pass — all additive inside `internal/autorouter`, with `Score`/`ScoreWithProfile` signatures and the `DecisionSnapshot` shape unchanged.

**Architecture:** `extractText` is extended to return structured extraction (raw text + per-role text + fence content) instead of a single lowercase string. `scoreDimensions` consumes that structure: code-fence content folds into `FieldCode`, user-turn token share drives `FieldTokens` (history contribution capped at 20%). Text normalization for keyword matching happens exactly once in `ScoreWithProfile`. Keyword rules, profiles, thresholds, and all persistence keep their current semantics.

**Tech Stack:** Go 1.26, `gjson` (already used), stdlib only. Tests with plain `testing`.

**Working directory:** `/home/bilfid/projects/nixllm/.worktrees/autorouter-f1` (branch `feature/autorouter-scorer-v2`).

**Baseline notes (verified):**
- `go test ./internal/autorouter/...` passes.
- Pre-existing failures on main, unrelated to this work: 3 executor tests in `internal/runtime/executor/claude_executor_test.go` (`TestApplyClaudeHeaders_DisableDeviceProfileStabilization`, `TestApplyClaudeHeaders_LegacyModePreservesConfiguredUserAgentOverrideForClaudeClients`, `TestClaudeExecutor_NonClaudeRequestUsesClaudeCode220CLIFingerprint`). Ignore them; do not fix here.
- `internal/dashboardasset/dist/` is an untracked build artifact; if `internal/api` tests fail on a missing embed, copy it from the main checkout.
- Repo rules (AGENTS.md): `gofmt -w` after every Go change; verify compile with `go build -o test-output ./cmd/server && rm test-output`; comments in English; no `log.Fatal`.

**Design doc:** `docs/plans/2026-09-08-auto-router-enhancements-design.md` (Part 1).

---

### Task 1: Structured extraction type — `extractedRequest`

**Files:**
- Modify: `internal/autorouter/scorer.go` (add type + builder near `extractText`)
- Test: `internal/autorouter/scorer_test.go`

**Step 1: Write the failing test**

Add to `scorer_test.go`:

```go
func TestExtractRequestStructure(t *testing.T) {
	body := `{"model":"x","messages":[
		{"role":"system","content":"You are a coding agent. Follow instructions."},
		{"role":"user","content":"hi there"},
		{"role":"assistant","content":"sure"},
		{"role":"user","content":"please summarize this:\n\`\`\`go\nfunc main() { a := 1 }\n\`\`\`\nthanks"}
	]}`
	ext := extractRequest([]byte(body), "openai")
	if len(ext.LatestUserText) == 0 {
		t.Fatal("expected latest user text to be captured")
	}
	if !strings.Contains(ext.LatestUserText, "summarize") {
		t.Fatalf("latest user text = %q", ext.LatestUserText)
	}
	if ext.CodeFenceTokens == 0 {
		t.Fatal("expected code fence tokens > 0")
	}
	if ext.FlatText == "" {
		t.Fatal("expected flat text to be populated")
	}
	// Flat text covers system + history + latest turn.
	if !strings.Contains(ext.FlatText, "coding agent") {
		t.Fatal("expected flat text to include system prompt")
	}
}
```

(Add `"strings"` to the test file imports.)

**Step 2: Run test to verify it fails**

Run: `go test ./internal/autorouter/ -run TestExtractRequestStructure -v`
Expected: FAIL — `undefined: extractRequest`

**Step 3: Write minimal implementation**

In `scorer.go`, add:

```go
// extractedRequest is the structured result of parsing one request body:
// everything the scorer needs, extracted once. It replaces the single
// lowercased flat string that extractText used to return.
type extractedRequest struct {
	// FlatText is the whitespace-joined, lowercased text of every message plus
	// system instructions (the historical extractText output).
	FlatText string
	// LatestUserText is the lowercased text of the final message whose role is
	// "user" (empty when the format has no notion of roles, e.g. Gemini).
	LatestUserText string
	// HistoryTokens is the lowercased word count of everything except the
	// latest user turn (system + prior turns + assistant replies).
	HistoryTokens int
	// CodeFenceTokens is the raw (non-lowercased) whitespace-token count of
	// fenced code blocks (``` … ```) and indented JSON/XML-looking literals
	// found anywhere in message text.
	CodeFenceTokens int
}
```

And a builder that reuses the existing per-format walkers (`extractMessagesAndSystem`, `extractGeminiText` gain variant functions that walk messages once, tracking role and text). Keep the existing `extractText` untouched for now (it will be replaced in Task 4).

Implementation sketch (concrete):

```go
// extractRequest parses the raw body into an extractedRequest according to the
// entry protocol format, mirroring the format dispatch of extractText.
func extractRequest(rawJSON []byte, format string) extractedRequest {
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case constant.OpenAI, constant.OpenaiResponse, constant.Claude:
		return extractChatRequest(rawJSON)
	case constant.Gemini, constant.GeminiInteractions, constant.Interactions:
		return extractGeminiRequest(rawJSON)
	default:
		if gjson.GetBytes(rawJSON, "contents").Exists() {
			return extractGeminiRequest(rawJSON)
		}
		if gjson.GetBytes(rawJSON, "messages").Exists() || gjson.GetBytes(rawJSON, "input").Exists() {
			return extractChatRequest(rawJSON)
		}
		flat := strings.ToLower(gjson.GetBytes(rawJSON, "prompt").String())
		return extractedRequest{FlatText: flat, LatestUserText: flat}
	}
}
```

`extractChatRequest` walks `system`, `instructions`, `messages[]`, `input[]` exactly like `extractMessagesAndSystem` does today, but per message: lowercased text is appended to a flat builder; when `role == "user"` the text is also recorded into `latestUser` (overwriting, so the last user turn wins) and excluded from the history count; other roles' word counts accumulate into `HistoryTokens`. When the final user turn is recorded, all of its words count toward neither history (they are `LatestUserText`'s own tokens). After the walk, run `countFenceTokens` over the raw per-message texts.

`extractGeminiRequest` mirrors `extractGeminiText` but parts text is tracked the same way; Gemini has no roles for contents[].parts in practice, so `LatestUserText` stays empty for it and ALL text counts as history — the token dimension then behaves as before (see Task 2's handling).

`countFenceTokens(rawText string) int`: scan the raw (non-lowercased) text for ``` fences — count whitespace tokens inside fence regions; also detect `{\n`/`{\r`-style multi-line JSON blocks (a `{` or `[` line followed by ≥2 indented lines and a matching closer) the same way. Keep it simple and deterministic; single-line `{}` in prose must not count.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/autorouter/ -run TestExtractRequestStructure -v`
Expected: PASS

**Step 5: Commit**

```bash
gofmt -w internal/autorouter/
git add internal/autorouter/scorer.go internal/autorouter/scorer_test.go
git commit -m "feat(autorouter): structured request extraction (fences, roles)"
```

---

### Task 2: Role-aware `FieldTokens` — user turns lead, history capped

**Files:**
- Modify: `internal/autorouter/scorer.go` (`scoreDimensions` signature + token computation)
- Test: `internal/autorouter/scorer_test.go`

**Step 1: Write the failing test**

```go
// A short user request wrapped in a huge agent system prompt must not be pushed
// to a hard tier by token count alone.
func TestScoreLongSystemPromptShortUserTurn(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"model":"x","messages":[{"role":"system","content":"`)
	for i := 0; i < 3000; i++ {
		sb.WriteString("instruction word ")
	}
	sb.WriteString(`"},{"role":"user","content":"hi what is your name?"}]}`)
	s := Score([]byte(sb.String()), "openai")
	if s.Tier != TierSimple {
		t.Fatalf("expected SIMPLE despite huge system prompt, got %q (total=%v tokens=%v)", s.Tier, s.Total, s.Fields[FieldTokens])
	}
}

// A genuinely long user request still scores high on tokens.
func TestScoreLongUserTurnTokens(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"model":"x","messages":[{"role":"user","content":"`)
	for i := 0; i < 4000; i++ {
		sb.WriteString("word ")
	}
	sb.WriteString(`}]}`)
	s := Score([]byte(sb.String()), "openai")
	if s.Fields[FieldTokens] < 0.5 {
		t.Fatalf("expected high token sub-score for long user turn, got %v", s.Fields[FieldTokens])
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/autorouter/ -run 'TestScoreLongSystemPromptShortUserTurn|TestScoreLongUserTurnTokens' -v`
Expected: First FAIL (system prompt inflates tokens → tier too high); second likely already passes (prose tokens still counted) — record both results.

**Step 3: Write minimal implementation**

Change `scoreDimensions(text string)` to `scoreDimensions(ext extractedRequest)`:

- `FieldTokens = clamp01((userWords + 0.2*historyWords) / 1200.0)` where `userWords = len(strings.Fields(ext.LatestUserText))`, `historyWords = ext.HistoryTokens`. When `LatestUserText` is empty (Gemini, `prompt` fallback), fall back to the old behavior: `clamp01(len(strings.Fields(ext.FlatText)) / 1200.0)` — history cap does not apply to formats without roles.
- All other dimensions continue to operate on `ext.FlatText` exactly as before (this task only moves the token computation).

**Step 4: Run tests**

Run: `go test ./internal/autorouter/ -v`
Expected: All PASS. **If any pre-existing scorer test changes tier** (e.g. `TestScoreMedium` relies on token contribution from a single short user message — verify: it does not, the message is the user turn itself), fix the *implementation*, not the test. The historical tests' requests all carry their content in the final user message, so their token sub-score is unchanged by this task.

**Step 5: Commit**

```bash
gofmt -w internal/autorouter/
git add internal/autorouter/scorer.go internal/autorouter/scorer_test.go
git commit -m "feat(autorouter): role-aware token scoring (user turns lead, history capped 20%)"
```

---

### Task 3: Structure-aware `FieldCode` — fence content counts directly

**Files:**
- Modify: `internal/autorouter/scorer.go` (`scoreDimensions` code computation)
- Test: `internal/autorouter/scorer_test.go`

**Step 1: Write the failing test**

```go
// A large fenced code payload surrounded by prose must score strongly on the
// code dimension even though code-like tokens are a minority of the prose.
func TestScoreCodeFencePayload(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"model":"x","messages":[{"role":"user","content":"please summarize what this file does\n\`\`\`go\n`)
	for i := 0; i < 200; i++ {
		sb.WriteString("handler_with_id := buildHandler(param_one, PARAM_TWO)\n")
	}
	sb.WriteString("\n```\"}]}")
	s := Score([]byte(sb.String()), "openai")
	if s.Fields[FieldCode] < 0.5 {
		t.Fatalf("expected high code sub-score from fenced payload, got %v", s.Fields[FieldCode])
	}
	if s.Tier != TierSimple {
		t.Fatalf("summarize + fence must stay low-tier overall, got %q (total=%v)", s.Tier, s.Total)
	}
}
```

Note the second assertion pins the design intent: fence content lifts `FieldCode` (accuracy signal) without lifting the total into a hard tier by itself — `FieldCode`'s weight (0.25) plus the summarize request's other dimensions keep it low. If the total lands above SIMPLE in practice, adjust the test's expectation only after confirming with the operator (do not silently weaken the fence signal).

**Step 2: Run test to verify it fails**

Run: `go test ./internal/autorouter/ -run TestScoreCodeFencePayload -v`
Expected: FAIL on the first assertion (`FieldCode` far below 0.5 — density of code-like tokens among 200+ prose words is diluted).

**Step 3: Write minimal implementation**

In `scoreDimensions`, after the existing density computation:

```go
// Fenced/literal code blocks count directly toward the code dimension: a
// request whose payload is mostly source inside fences is a code request even
// when the surrounding prose is code-free. Density is scaled so a fully-fenced
// body saturates at the same point as the token density formula.
fenceScore := 0.0
if wordCount > 0 {
	fenceScore = clamp01((float64(ext.CodeFenceTokens) / float64(wordCount)) * 1.5)
}
fields[FieldCode] = clamp01(max(fields[FieldCode], fenceScore))
```

`max` on floats is available since Go 1.21 — use the builtin. The 1.5 factor means fences matching ~2/3 of the body saturate `FieldCode` at 1.0.

**Step 4: Run tests**

Run: `go test ./internal/autorouter/ -v`
Expected: All PASS. The dimension-range test (`TestScoreDimensionsInRange`) must still pass — `FieldCode` stays within [0,1] by construction.

**Step 5: Commit**

```bash
gofmt -w internal/autorouter/
git add internal/autorouter/scorer.go internal/autorouter/scorer_test.go
git commit -m "feat(autorouter): code-fence content folds into code dimension"
```

---

### Task 4: Single normalization pass — `ScoreWithProfile` wires it together

**Files:**
- Modify: `internal/autorouter/scorer.go` (`ScoreWithProfile`, `Score`; remove/replace `extractText`)
- Modify: `internal/autorouter/profile.go` (`matchedKeywordRules` accepts pre-normalized text)
- Test: `internal/autorouter/scorer_test.go`

**Step 1: Write the failing test**

```go
// matchedKeywordRules must work from pre-normalized text and normalization must
// happen exactly once per score. Guard the contract: a rule with a punctuation
// phrase still matches a request whose punctuation differs.
func TestKeywordMatchOnPreNormalizedText(t *testing.T) {
	rules := []KeywordTierRule{{ID: "db", Tier: TierComplex, Keywords: []string{"migrate the database"}}}
	// Text as produced by normalizeKeywordText: punctuation already collapsed.
	text := "please migrate the database to postgres 16 this weekend"
	matched := matchedKeywordRules(text, rules)
	if len(matched) != 1 || matched[0].ID != "db" {
		t.Fatalf("matched = %+v", matched)
	}
}
```

Also add a counter guard (simple approach: a package-level test-only hook is overkill — instead assert the *behavioral* equivalence below in the benchmark task; this test only pins the new contract).

**Step 2: Run test to verify it fails**

Run: `go test ./internal/autorouter/ -run TestKeywordMatchOnPreNormalizedText -v`
Expected: FAIL — the current `matchedKeywordRules` normalizes its input, and `"migrate the database"` (a keyword) is normalized to `"migrate the database"` which *does* match... The current function also works on raw text. The point of this task is to stop double-normalizing the whole text; this test pins the new signature contract. If the test passes against the old signature, proceed — the real verification is Task 4's refactor compiling with a changed signature (old signature took raw text; new takes pre-normalized; both must yield the same matches).

**Step 3: Refactor**

1. In `profile.go`, change `matchedKeywordRules(text string, rules []KeywordTierRule)` so it **assumes `text` is already normalized** (drop the leading `normalizeKeywordText(text)` call; keep the surrounding-space padding and per-keyword normalization — keywords are short, that cost is fine).
2. In `scorer.go`:
   - `ScoreWithProfile` calls `extractRequest` once; passes `ext.FlatText` to `scoreDimensions`; computes `normalizedText := normalizeKeywordText(ext.FlatText)` exactly once and passes it to `matchedKeywordRules`.
   - `extractText` and `extractMessagesAndSystem`/`extractGeminiText` are deleted; their logic lives in the Task 1 builders. Keep the doc comments' content (move them).
   - `Score(rawJSON, format)` keeps working via `ScoreWithProfile(rawJSON, format, nil).Score`.

**Step 4: Run tests**

Run: `go test ./internal/autorouter/ -v`
Expected: All PASS, including `TestScoreWithCustomProfileAndKeywordOverride` and `TestKeywordRulesChooseHighestTierRegardlessOfOrder` (they must be untouched).

**Step 5: Verify compile + full package tests + commit**

```bash
go build -o test-output ./cmd/server && rm test-output
go test ./internal/autorouter/ ./internal/store/ -v 2>&1 | grep -E "^(FAIL|ok)"
git add internal/autorouter/scorer.go internal/autorouter/profile.go internal/autorouter/scorer_test.go
git commit -m "refactor(autorouter): single text-normalization pass in scorer"
```

Expected: `ok` for both packages (`internal/store` touches autorouter via profile store; no behavior change).

---

### Task 5: DecisionSnapshot golden test + scorer contract test

**Files:**
- Create: `internal/autorouter/decision_golden_test.go`
- Test: same file

**Step 1: Write the failing test**

The snapshot shape is the persistence contract (jsonb in `usage_events`). Pin it:

```go
func TestDecisionSnapshotJSONShape(t *testing.T) {
	snap := DecisionSnapshot{
		ProfileVersion:   1,
		ProfileHash:      "sha256:abc",
		ProfileSnapshot:  DefaultProfileConfig(),
		ScoreTotal:       0.42,
		ScoreFields:      map[ScoreField]float64{FieldTokens: 0.1},
		ReasoningMarkers: 1,
		ScoredTier:       TierMedium,
		EffectiveTier:    TierComplex,
		DecisionCause:    DecisionCauseKeywordMatch,
		MatchedRules:     []MatchedKeywordRule{{ID: "r", Tier: TierComplex, Keywords: []string{"k"}}},
		MappingTier:      TierComplex,
		FallbackChain:    []Tier{TierReasoning, TierComplex},
		TargetModel:      "claude-sonnet-4-5",
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"profile_version", "profile_hash", "profile_snapshot", "score_total",
		"score_fields", "reasoning_markers", "scored_tier", "effective_tier",
		"decision_cause", "matched_rules", "mapping_tier", "fallback_chain", "target_model",
	} {
		if !gjson.GetBytes(raw, key).Exists() {
			t.Errorf("decision snapshot JSON missing key %q (got %s)", key, raw)
		}
	}
}
```

(Imports: `encoding/json`, `testing`, `github.com/tidwall/gjson`.)

**Step 2: Run test to verify it passes**

Run: `go test ./internal/autorouter/ -run TestDecisionSnapshotJSONShape -v`
Expected: PASS immediately (this is a guard test — it pins the contract that Tasks 1–4 must not have broken). If it FAILS, Tasks 1–4 broke the snapshot contract: fix the implementation.

**Step 3: Commit**

```bash
git add internal/autorouter/decision_golden_test.go
git commit -m "test(autorouter): pin DecisionSnapshot JSON shape"
```

---

### Task 6: End-to-end scorer parity + allocation benchmark

**Files:**
- Create: `internal/autorouter/scorer_bench_test.go`

**Step 1: Write the benchmark and parity test**

```go
// Parity: for the historic test corpus, tier classification must not regress
// for the requests the old scorer already classified correctly. The corpus is
// the set of realistic bodies from scorer_test.go; each must keep its tier.
func TestScorerParityOnCorpus(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Tier
	}{
		{"greeting", `{"messages":[{"role":"user","content":"hi, what is your name?"}]}`, TierSimple},
		{"summarize", `{"messages":[{"role":"user","content":"please summarize the main points of this meeting notes document"}]}`, TierMedium},
		{"proof", `{"messages":[{"role":"user","content":"explain why the algorithm is correct and analyze its time complexity. prove the trade-off between memory and speed."}]}`, TierReasoning},
		{"gemini", `{"contents":[{"parts":[{"text":"summarize this short article in a few bullet points"}]}]}`, TierSimple},
		{"empty", `{"model":"x"}`, TierSimple},
	}
	for _, c := range cases {
		if got := Score([]byte(c.body), "openai").Tier; got != c.want {
			t.Errorf("%s: tier = %q, want %q", c.name, got, c.want)
		}
	}
}

func buildLargeAgentBody() []byte {
	var sb strings.Builder
	sb.WriteString(`{"model":"x","messages":[{"role":"system","content":"`)
	for i := 0; i < 40000; i++ {
		sb.WriteString("agent system instruction line with some technical words api json cache\n")
	}
	sb.WriteString(`"},{"role":"user","content":"add a retry with backoff to the fetch helper, then implement tests for it"}]}`)
	return []byte(sb.String())
}

func BenchmarkScoreLargeAgentBody(b *testing.B) {
	raw := buildLargeAgentBody()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Score(raw, "openai")
	}
}
```

**Step 2: Run the parity test**

Run: `go test ./internal/autorouter/ -run TestScorerParityOnCorpus -v`
Expected: PASS. Any FAIL means Tasks 1–3 changed a tier on the historic corpus — reconcile with the operator before weakening (the intent is: same tiers for these, better tiers for system-prompt-heavy and fenced bodies).

**Step 3: Record the benchmark before/after**

Run: `go test ./internal/autorouter/ -bench BenchmarkScoreLargeAgentBody -benchmem -run xxx`
Record the `allocs/op` number in the commit message. Compare against a stash-based baseline if useful:
`git stash && go test -bench ... && git stash pop` (report both numbers).

**Step 4: Commit**

```bash
git add internal/autorouter/scorer_bench_test.go
git commit -m "test(autorouter): scorer parity corpus + large-body benchmark"
```

---

### Task 7: Full verification + gofmt + compile check

**Step 1: Format**

Run: `gofmt -w internal/autorouter/`
Expected: no diff (should already be formatted).

**Step 2: Full autorouter test suite**

Run: `go test ./internal/autorouter/ -v`
Expected: all PASS.

**Step 3: Compile verification (repo rule)**

Run: `go build -o test-output ./cmd/server && rm test-output`
Expected: silent success.

**Step 4: Broader regression sweep**

Run: `go test ./internal/... ./sdk/... 2>&1 | grep -E "^(FAIL|--- FAIL)"`
Expected: only the 3 pre-existing executor failures listed in the header. Anything else is a regression caused by this branch — investigate before proceeding.

**Step 5: Update plan doc status**

Run:
```bash
git add -f docs/plans/2026-09-08-auto-router-enhancements-design.md
git commit -m "docs: mark F1 (scorer v2) implemented" --allow-empty -m "F1 tasks 1-7 complete; parity corpus green; benchmark recorded."
```

(If the design doc needs no content change, `--allow-empty` records the milestone; alternatively append a short "Status: F1 implemented on feature/autorouter-scorer-v2" line under **Context**.)

---

## Out of scope for F1 (explicit)

- Score cache, CompiledProfile, windowing (F2).
- Any change to `ProfileConfig`, thresholds, weights, keyword-rule semantics.
- Vision bridge, resolution/fallback logic.
- The pre-existing executor test failures.
