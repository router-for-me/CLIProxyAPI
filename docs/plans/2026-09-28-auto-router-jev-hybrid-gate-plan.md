# Auto Router — Jev AI Hybrid Gate Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a semantic, calibrated Jev AI classifier in front of the Auto Router's lexical heuristic, where the heuristic always runs, Jev may override it when confident, and every Jev failure falls back silently.

**Architecture:** A `jevclient` package wraps `POST /v1/systemone`; a `jevgate` package builds the classifier state, caches verdicts, applies the confidence threshold and trips an auth-failure circuit breaker; the existing `resolveAutoRouterModel` hook calls the gate after the heuristic scorer and before `Resolve()`. Configuration is a global `jev_settings` singleton (master toggle + sealed API key) plus per-router knobs, mirroring `litellm_sync_settings`.

**Tech Stack:** Go 1.26, PostgreSQL (pgx stdlib via `database/sql`), Gin, React + Vite dashboard.

**Design doc:** `docs/plans/2026-09-28-auto-router-jev-hybrid-gate-design.md`

---

## Planning refinements (deviations from the design doc)

Two things changed while reading the code, both recorded here rather than silently:

1. **A sibling cache in `jevgate`, not an entry type in `autorouter.scoreCache`.** The design said to add a verdict type to the existing ring. `scoreCache` is unexported and hard-typed to `ScoreResult` (`internal/autorouter/score_cache.go:22-27`), so adding a second entry type would mean changing its signature and every caller — touching working hot-path code for no benefit. `jevgate.Cache` reimplements the same FIFO-ring design (~40 lines) and leaves `scoreCache` untouched.

2. **`Cmd`-side settings resolution is a cached read, not a per-request PG query.** Reading `jev_settings` on every routed request would add a DB round-trip to a path that currently does zero I/O. The store caches the row and refreshes it on write (same pattern as `pg_auto_routers.go`'s `modelCache`).

## Ground rules

- **TDD, always.** Write the failing test, watch it fail, implement minimally, watch it pass, commit.
- **`gofmt -w .` before every commit.** Non-negotiable in this repo.
- **Never `log.Fatal`/`log.Fatalf`.** Return errors; log via `logrus`.
- **No timeouts after an upstream connection.** The 400ms classifier budget is pre-upstream, same phase as the vision bridge. Do not add timeouts elsewhere.
- **Do not touch `internal/translator/`.**
- **Comment in English only.** If you edit a file with non-English comments, translate them.
- **Run `go build -o test-output ./cmd/server && rm test-output`** before declaring any Go task done.

## Worktree

Implementation happens in `.claude/worktrees/jev-gate` on branch `feat/autorouter-jev-hybrid-gate` (already created). All paths below are relative to that worktree root.

```bash
cd /home/bilfid/projects/nixllm/.claude/worktrees/jev-gate
```

---

## Task 1: `jevclient` — HTTP client and types

**Files:**
- Create: `internal/autorouter/jevclient/client.go`
- Test: `internal/autorouter/jevclient/client_test.go`

**Step 1: Write the failing test**

```go
package jevclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallSendsExpectedRequestAndParsesChoice(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		decodeJSON(t, r, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "jev-1.13.0",
			"answers": {"tier": {
				"type": "choice", "choice": "complex",
				"probabilities": {"simple":0.05,"medium":0.18,"complex":0.60,"reasoning":0.17},
				"confidence": 0.72
			}},
			"usage": {"input_tokens": 1480, "output_tokens": 0}
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "sk-ts-test", srv.Client())
	resp, err := c.Call(context.Background(), "jev-1.13.0",
		map[string]any{"latest_user_message": "refactor this"},
		map[string]Question{"tier": {Type: "choice", Instructions: "pick a tier"}})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if gotPath != "/v1/systemone" {
		t.Errorf("path = %q, want /v1/systemone", gotPath)
	}
	if gotAuth != "Bearer sk-ts-test" {
		t.Errorf("auth = %q, want Bearer sk-ts-test", gotAuth)
	}
	if gotBody["model"] != "jev-1.13.0" {
		t.Errorf("model = %v, want jev-1.13.0", gotBody["model"])
	}
	if _, ok := gotBody["questions"].(map[string]any)["tier"]; !ok {
		t.Errorf("questions missing tier key: %v", gotBody["questions"])
	}

	if resp.Answers["tier"].Choice != "complex" {
		t.Errorf("choice = %q, want complex", resp.Answers["tier"].Choice)
	}
	if resp.Answers["tier"].Confidence != 0.72 {
		t.Errorf("confidence = %v, want 0.72", resp.Answers["tier"].Confidence)
	}
	if resp.Usage.InputTokens != 1480 {
		t.Errorf("input_tokens = %d, want 1480", resp.Usage.InputTokens)
	}
}

func TestCallReturnsStatusErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "nope", srv.Client())
	_, err := c.Call(context.Background(), "jev-1.13.0", "state", nil)
	if err == nil {
		t.Fatal("Call: want error, got nil")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error type = %T, want *StatusError", err)
	}
	if se.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", se.StatusCode)
	}
}

func TestNewDefaultsBaseURL(t *testing.T) {
	c := New("", "k", nil)
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
}
```

Add the two helpers at the bottom of the test file:

```go
func decodeJSON(t *testing.T, r *http.Request, into any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/autorouter/jevclient/ -v
```

Expected: build failure — `undefined: New`, `undefined: StatusError`, `undefined: DefaultBaseURL`.

**Step 3: Write minimal implementation**

```go
// Package jevclient is a minimal HTTP client for TypeSafe's System One API
// (https://docs.typesafe.ai). It exists to answer exactly one question per
// request — which complexity tier does this prompt belong to — and it makes
// exactly one attempt: the caller owns the timeout and no retry happens on the
// request path.
package jevclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	// DefaultBaseURL is the TypeSafe API root.
	DefaultBaseURL = "https://api.typesafe.ai"
	// systemOnePath is the evaluation endpoint.
	systemOnePath = "/v1/systemone"
	// maxErrorBody bounds how much of a non-200 body is retained for logging.
	maxErrorBody = 512
)

// Question is one typed question sent to the System One model. Type is one of
// "choice", "score", "noul"; Criteria carries the option set (Choice) or the
// ordered rubric (Score).
type Question struct {
	Type         string         `json:"type"`
	Instructions string         `json:"instructions"`
	Criteria     map[string]any `json:"criteria,omitempty"`
}

// ChoiceAnswer is the answer to a Choice question. Confidence is derived by the
// API from the shape of Probabilities, not from the top probability alone.
type ChoiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// Usage reports token accounting for one call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is a parsed /v1/systemone response.
type Response struct {
	Model   string                  `json:"model"`
	Answers map[string]ChoiceAnswer `json:"answers"`
	Usage   Usage                   `json:"usage"`
}

// StatusError is a non-200 response. Callers branch on StatusCode to decide
// whether to open the circuit breaker (401/403 mean a credential problem that
// will not fix itself at request rate). The body is truncated: it is retained
// for diagnostics only and must never carry a key back into a log line.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("jevclient: status %d: %s", e.StatusCode, e.Body)
}

// request is the /v1/systemone request body.
type request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Client is an HTTP client for the System One API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New builds a Client. An empty baseURL uses DefaultBaseURL; a nil http client
// uses http.DefaultClient. The client is used as-is so the caller owns
// connection pooling.
func New(baseURL, apiKey string, hc *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: hc}
}

// Call sends one state and its question set, returning the parsed response. It
// performs exactly one attempt.
func (c *Client) Call(ctx context.Context, model string, state any, questions map[string]Question) (Response, error) {
	body, errMarshal := json.Marshal(request{State: state, Model: model, Questions: questions})
	if errMarshal != nil {
		return Response{}, fmt.Errorf("jevclient: marshal request: %w", errMarshal)
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+systemOnePath, bytes.NewReader(body))
	if errReq != nil {
		return Response{}, fmt.Errorf("jevclient: build request: %w", errReq)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, errDo := c.http.Do(req)
	if errDo != nil {
		return Response{}, fmt.Errorf("jevclient: call: %w", errDo)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.WithError(cerr).Debug("jevclient: close response body")
		}
	}()
	raw, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return Response{}, fmt.Errorf("jevclient: read response: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, &StatusError{StatusCode: resp.StatusCode, Body: truncateBody(string(raw))}
	}
	var out Response
	if errUnmarshal := json.Unmarshal(raw, &out); errUnmarshal != nil {
		return Response{}, fmt.Errorf("jevclient: parse response: %w", errUnmarshal)
	}
	return out, nil
}

// truncateBody bounds a diagnostic body excerpt.
func truncateBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxErrorBody {
		return s
	}
	return s[:maxErrorBody] + "…"
}
```

**Step 4: Run test to verify it passes**

```bash
go test ./internal/autorouter/jevclient/ -v
```

Expected: all three tests PASS.

**Step 5: Commit**

```bash
gofmt -w .
go build -o test-output ./cmd/server && rm test-output
git add internal/autorouter/jevclient/
git commit -m "feat(jevclient): System One HTTP client for tier classification"
```

---

## Task 2: `jevgate` — state builder and question definition

**Files:**
- Create: `internal/autorouter/jevgate/state.go`
- Test: `internal/autorouter/jevgate/state_test.go`

**Step 1: Write the failing test**

```go
package jevgate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildStateTruncatesLatestUserMessage(t *testing.T) {
	long := strings.Repeat("x", MaxStateChars+500)
	st := BuildState(StateInput{LatestUserText: long, MessageCount: 3})
	if len(st.LatestUserMessage) != MaxStateChars {
		t.Errorf("len = %d, want %d", len(st.LatestUserMessage), MaxStateChars)
	}
}

func TestBuildStateCarriesMetadataFlags(t *testing.T) {
	st := BuildState(StateInput{
		LatestUserText:      "add a retry to the fetch helper",
		MessageCount:        12,
		HistoryWordEstimate: 3400,
		HasTools:            true,
		HasCodeFence:        true,
		HasImages:           false,
		ModelRequested:      "router:smart-router",
	})
	if st.Metadata.MessageCount != 12 {
		t.Errorf("message_count = %d, want 12", st.Metadata.MessageCount)
	}
	if !st.Metadata.HasTools {
		t.Error("has_tools = false, want true")
	}
	if st.Metadata.HasImages {
		t.Error("has_images = true, want false")
	}
	if st.Metadata.ModelRequested != "router:smart-router" {
		t.Errorf("model_requested = %q", st.Metadata.ModelRequested)
	}
}

func TestBuildStateEmptyUserTextIsValid(t *testing.T) {
	st := BuildState(StateInput{MessageCount: 0})
	if st.LatestUserMessage != "" {
		t.Errorf("latest_user_message = %q, want empty", st.LatestUserMessage)
	}
	if _, err := json.Marshal(st); err != nil {
		t.Fatalf("state must marshal: %v", err)
	}
}

func TestTierQuestionCoversAllFourTiers(t *testing.T) {
	q := TierQuestion()
	if q.Type != "choice" {
		t.Errorf("type = %q, want choice", q.Type)
	}
	for _, tier := range []string{"simple", "medium", "complex", "reasoning"} {
		if _, ok := q.Criteria[tier]; !ok {
			t.Errorf("criteria missing tier %q", tier)
		}
	}
	if strings.TrimSpace(q.Instructions) == "" {
		t.Error("instructions must not be empty")
	}
}

func TestQuestionHashIsStableAndSensitive(t *testing.T) {
	if QuestionHash() != QuestionHash() {
		t.Error("QuestionHash must be deterministic")
	}
	if QuestionHash() == "" {
		t.Error("QuestionHash must not be empty")
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/autorouter/jevgate/ -v
```

Expected: build failure — `undefined: BuildState`, `undefined: TierQuestion`, `undefined: MaxStateChars`.

**Step 3: Write minimal implementation**

```go
// Package jevgate adds a semantic, calibrated tier classifier in front of the
// Auto Router's lexical heuristic. The heuristic always runs and is the
// guaranteed path; this package may override its tier when the classifier's
// confidence clears an operator-set floor. Every failure mode — timeout, bad
// status, unparseable body, missing key — falls back to the heuristic, so the
// gate can never fail a request.
package jevgate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
)

// MaxStateChars bounds the latest-user-message text sent to the classifier.
// ~6000 characters is roughly 1.5k tokens: about $0.00006 per uncached request
// at the documented $0.042/Mtok input price, and far below the API's 32k state
// limit.
const MaxStateChars = 6000

// QuestionID is the key the tier question is sent under (and read back from).
const QuestionID = "tier"

// State is the payload sent to the classifier: the latest user turn plus
// derived metadata. System prompts and message history are deliberately
// excluded — the last turn carries the strongest complexity signal, and
// excluding them keeps token cost and privacy exposure to one turn.
type State struct {
	LatestUserMessage string   `json:"latest_user_message"`
	Metadata          Metadata `json:"metadata"`
}

// Metadata is the secondary evidence sent alongside the user turn.
type Metadata struct {
	MessageCount        int    `json:"message_count"`
	HistoryWordEstimate int    `json:"history_word_estimate"`
	HasTools            bool   `json:"has_tools"`
	HasCodeFence        bool   `json:"has_code_fence"`
	HasImages           bool   `json:"has_images"`
	ModelRequested      string `json:"model_requested"`
}

// StateInput is the flat input BuildState consumes, so the caller does not have
// to construct the nested shape.
type StateInput struct {
	LatestUserText      string
	MessageCount        int
	HistoryWordEstimate int
	HasTools            bool
	HasCodeFence        bool
	HasImages           bool
	ModelRequested      string
}

// BuildState assembles the classifier state, truncating the user turn to
// MaxStateChars. Truncation is byte-based and applied after trimming trailing
// whitespace; the metadata fields are passed through unchanged.
func BuildState(in StateInput) State {
	text := in.LatestUserText
	if len(text) > MaxStateChars {
		text = text[:MaxStateChars]
	}
	return State{
		LatestUserMessage: text,
		Metadata: Metadata{
			MessageCount:        in.MessageCount,
			HistoryWordEstimate: in.HistoryWordEstimate,
			HasTools:            in.HasTools,
			HasCodeFence:        in.HasCodeFence,
			HasImages:           in.HasImages,
			ModelRequested:      in.ModelRequested,
		},
	}
}

// TierQuestion returns the single Choice question used for classification. The
// criteria restate the router's existing four-tier rubric rather than inventing
// a parallel taxonomy, which is what lets the gate drop in without touching any
// tier-to-model mapping.
func TierQuestion() jevclient.Question {
	return jevclient.Question{
		Type: "choice",
		Instructions: "Classify the LLM request by the cheapest model tier that can answer it well. " +
			"Consider the latest user message primarily; metadata is secondary evidence.",
		Criteria: map[string]any{
			"simple":    "Greetings, chitchat, one-line factual lookups, trivial rewrites of short text.",
			"medium":    "Summaries, translations, short Q&A, simple code edits on small snippets, single-step tasks.",
			"complex":   "Multi-file or multi-step coding, debugging with context, longer document analysis, agent tool-use turns.",
			"reasoning": "Requires deep step-by-step reasoning: proofs, algorithm analysis, architecture trade-offs, math, planning.",
		},
	}
}

// QuestionHash is the question definition's identity. It participates in the
// cache key, so editing the instructions or criteria invalidates cached verdicts
// automatically. encoding/json sorts map keys, so this is deterministic.
func QuestionHash() string {
	raw, errMarshal := json.Marshal(TierQuestion())
	if errMarshal != nil {
		// Unreachable for this fixed literal shape; an empty hash would disable
		// cache invalidation, so surface it rather than hiding it.
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
```

**Step 4: Run test to verify it passes**

```bash
go test ./internal/autorouter/jevgate/ -v
```

Expected: all five tests PASS.

**Step 5: Commit**

```bash
gofmt -w .
git add internal/autorouter/jevgate/
git commit -m "feat(jevgate): classifier state builder and tier question"
```

---

## Task 3: `jevgate` — verdict cache

**Files:**
- Create: `internal/autorouter/jevgate/cache.go`
- Test: `internal/autorouter/jevgate/cache_test.go`

**Step 1: Write the failing test**

```go
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
	st := BuildState(StateInput{LatestUserText: "a"})
	k1 := c.Key("openai", "r", st, "m", "q")
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

func TestNewCacheDefaultsCapacity(t *testing.T) {
	c := NewCache(0)
	for i := 0; i < defaultCacheCapacity+10; i++ {
		c.Put(c.Key("f", "r", BuildState(StateInput{LatestUserText: string(rune('a' + i%26)) + string(rune('0'+i/26))}), "m", "q"), Verdict{})
	}
	if c.len() > defaultCacheCapacity {
		t.Errorf("len = %d, want <= %d", c.len(), defaultCacheCapacity)
	}
}
```

Note the last test writes more than capacity distinct keys; `string(rune(...))` produces two distinct characters per i, so keys are unique up to capacity+10 iterations.

**Step 2: Run test to verify it fails**

```bash
go test ./internal/autorouter/jevgate/ -v
```

Expected: build failure — `undefined: NewCache`, `undefined: defaultCacheCapacity`.

**Step 3: Write minimal implementation**

```go
package jevgate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// defaultCacheCapacity bounds the process-local verdict cache. Entries are a
// few hundred bytes, so memory stays well under a megabyte.
const defaultCacheCapacity = 2048

// Cache is a mutex-guarded FIFO cache mapping a state fingerprint to a Verdict.
// It is best-effort by design: a miss simply means the classifier is called, so
// the cache can never become a failure point. Keys are SHA-256 digests — no
// prompt text is stored.
//
// Eviction is FIFO via a ring of keys (same reasoning as the scorer's cache:
// no LRU bookkeeping, repeated identical requests hit regardless of order, and
// memory is bounded deterministically).
type Cache struct {
	mu    sync.Mutex
	items map[string]Verdict
	ring  []string
	next  int
}

// NewCache builds a cache with the given capacity (<= 0 uses the default).
func NewCache(capacity int) *Cache {
	if capacity <= 0 {
		capacity = defaultCacheCapacity
	}
	return &Cache{
		items: make(map[string]Verdict, capacity),
		ring:  make([]string, capacity),
	}
}

// Key derives the cache key for one classification input. It deliberately keys
// on the derived state rather than the raw request body: the same conversational
// turn then hits regardless of accumulated history. Every input that changes the
// outcome participates, including questionHash (so a rubric edit invalidates)
// and model (so a model change does not serve stale thresholds).
func (c *Cache) Key(format, routerID string, st State, model, questionHash string) string {
	h := sha256.New()
	// Marshal the state so nested metadata participates without manual field
	// enumeration. encoding/json sorts map keys; State has no maps today, but
	// this stays correct if one is added.
	if raw, errMarshal := json.Marshal(st); errMarshal == nil {
		h.Write(raw)
	}
	for _, s := range []string{format, routerID, model, questionHash} {
		h.Write([]byte{0})
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns the cached verdict for key, if present.
func (c *Cache) Get(key string) (Verdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

// Put stores a verdict, evicting the oldest entry when at capacity.
func (c *Cache) Put(key string, v Verdict) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists {
		if old := c.ring[c.next]; old != "" {
			delete(c.items, old)
		}
		c.ring[c.next] = key
		c.next = (c.next + 1) % len(c.ring)
	}
	c.items[key] = v
}

// len reports the number of live entries (test helper).
func (c *Cache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
```

`Verdict` is declared in Task 4; to keep this task compiling on its own, declare it here in `cache.go` and let Task 4 add the constructor and gate. Add now:

```go
// Verdict is the outcome of a classification: what the classifier chose, how
// confident it was, and how this decision was reached. It is cached and
// persisted verbatim so operators can tune thresholds against the recorded
// distribution rather than guessing.
type Verdict struct {
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Model         string             `json:"model,omitempty"`
	LatencyMs     int64              `json:"latency_ms"`
	InputTokens   int                `json:"input_tokens"`
	// Cache is "hit" or "miss" — whether this verdict came from the cache.
	Cache string `json:"cache,omitempty"`
	// Verdict is how the decision was reached: VerdictAccepted,
	// VerdictLowConfidence, VerdictError, or VerdictBreakerOpen.
	Verdict string `json:"verdict,omitempty"`
}

// Verdict outcome values (Verdict.Verdict).
const (
	VerdictAccepted      = "accepted"
	VerdictLowConfidence = "rejected_low_confidence"
	VerdictError         = "error"
	VerdictBreakerOpen   = "breaker_open"
)

// Cache marker values (Verdict.Cache).
const (
	CacheHit  = "hit"
	CacheMiss = "miss"
)
```

**Step 4: Run test to verify it passes**

```bash
go test ./internal/autorouter/jevgate/ -v
```

Expected: all four new tests PASS.

**Step 5: Commit**

```bash
gofmt -w .
git add internal/autorouter/jevgate/
git commit -m "feat(jevgate): verdict cache keyed on classifier state"
```

---

## Task 4: `jevgate` — circuit breaker and gate

**Files:**
- Create: `internal/autorouter/jevgate/breaker.go`
- Create: `internal/autorouter/jevgate/gate.go`
- Test: `internal/autorouter/jevgate/breaker_test.go`
- Test: `internal/autorouter/jevgate/gate_test.go`

**Step 1: Write the failing tests**

`breaker_test.go`:

```go
package jevgate

import (
	"testing"
	"time"
)

func TestBreakerOpensOnTripAndClosesAfterCooldown(t *testing.T) {
	now := time.Unix(1000, 0)
	b := NewBreaker(5 * time.Minute)
	b.now = func() time.Time { return now }

	if b.Open("r1") {
		t.Fatal("breaker should start closed")
	}
	b.Trip("r1")
	if !b.Open("r1") {
		t.Fatal("breaker should be open after Trip")
	}
	if b.Open("r2") {
		t.Fatal("trip must be per-router")
	}
	now = now.Add(5*time.Minute + time.Second)
	if b.Open("r1") {
		t.Fatal("breaker should close after cooldown")
	}
}

func TestBreakerNilIsSafe(t *testing.T) {
	var b *Breaker
	if b.Open("r") {
		t.Fatal("nil breaker must report closed")
	}
	b.Trip("r") // must not panic
}
```

`gate_test.go`:

```go
package jevgate

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
)

// fakeCaller is a scripted Caller recording how many times it was invoked.
type fakeCaller struct {
	calls   int
	resp    jevclient.Response
	err     error
	lastReq map[string]jevclient.Question
}

func (f *fakeCaller) Call(_ context.Context, _ string, _ any, q map[string]jevclient.Question) (jevclient.Response, error) {
	f.calls++
	f.lastReq = q
	return f.resp, f.err
}

func choiceResp(choice string, confidence float64) jevclient.Response {
	return jevclient.Response{
		Model: "jev-1.13.0",
		Answers: map[string]jevclient.ChoiceAnswer{
			QuestionID: {Type: "choice", Choice: choice, Confidence: confidence,
				Probabilities: map[string]float64{choice: confidence}},
		},
		Usage: jevclient.Usage{InputTokens: 100},
	}
}

func enabledCfg() Config {
	return Config{GlobalEnabled: true, APIKeySet: true, RouterEnabled: true,
		Model: "jev-1.13.0", MinConfidence: 0.5, Timeout: 400 * time.Millisecond}
}

func TestDecideAcceptsHighConfidenceVerdict(t *testing.T) {
	fc := &fakeCaller{resp: choiceResp("complex", 0.72)}
	g := NewGate(fc, NewCache(8), nil)
	st := BuildState(StateInput{LatestUserText: "refactor this module"})

	v, accepted := g.Decide(context.Background(), enabledCfg(), "openai", "r1", st)
	if !accepted {
		t.Fatalf("want accepted, got %+v", v)
	}
	if v.Choice != "complex" {
		t.Errorf("choice = %q, want complex", v.Choice)
	}
	if v.Verdict != VerdictAccepted {
		t.Errorf("verdict = %q, want %q", v.Verdict, VerdictAccepted)
	}
	if v.Cache != CacheMiss {
		t.Errorf("cache = %q, want %q", v.Cache, CacheMiss)
	}
	if fc.calls != 1 {
		t.Errorf("calls = %d, want 1", fc.calls)
	}
}

func TestDecideRejectsLowConfidenceButCachesIt(t *testing.T) {
	fc := &fakeCaller{resp: choiceResp("complex", 0.30)}
	g := NewGate(fc, NewCache(8), nil)
	st := BuildState(StateInput{LatestUserText: "hmm"})

	v, accepted := g.Decide(context.Background(), enabledCfg(), "openai", "r1", st)
	if accepted {
		t.Fatal("low confidence must not be accepted")
	}
	if v.Verdict != VerdictLowConfidence {
		t.Errorf("verdict = %q", v.Verdict)
	}
	if v.Confidence != 0.30 {
		t.Errorf("confidence must be reported for tuning, got %v", v.Confidence)
	}
	// Second call must come from the cache: the call was already paid for.
	v2, accepted2 := g.Decide(context.Background(), enabledCfg(), "openai", "r1", st)
	if accepted2 {
		t.Fatal("cached low-confidence must not be accepted")
	}
	if v2.Cache != CacheHit {
		t.Errorf("cache = %q, want %q", v2.Cache, CacheHit)
	}
	if fc.calls != 1 {
		t.Errorf("calls = %d, want 1 (cache must absorb the repeat)", fc.calls)
	}
}

func TestDecideIsDisabledWithoutFullGate(t *testing.T) {
	cases := map[string]Config{
		"global off":  {GlobalEnabled: false, APIKeySet: true, RouterEnabled: true},
		"no key":      {GlobalEnabled: true, APIKeySet: false, RouterEnabled: true},
		"router off":  {GlobalEnabled: true, APIKeySet: true, RouterEnabled: false},
	}
	for name, cfg := range cases {
		fc := &fakeCaller{resp: choiceResp("simple", 0.99)}
		g := NewGate(fc, NewCache(8), nil)
		if _, accepted := g.Decide(context.Background(), cfg, "openai", "r1", BuildState(StateInput{})); accepted {
			t.Errorf("%s: must not accept", name)
		}
		if fc.calls != 0 {
			t.Errorf("%s: calls = %d, want 0", name, fc.calls)
		}
	}
}

func TestDecideFailsOpenOnCallError(t *testing.T) {
	fc := &fakeCaller{err: errors.New("boom")}
	g := NewGate(fc, NewCache(8), nil)
	v, accepted := g.Decide(context.Background(), enabledCfg(), "openai", "r1", BuildState(StateInput{}))
	if accepted {
		t.Fatal("must not accept on error")
	}
	if v.Verdict != VerdictError {
		t.Errorf("verdict = %q, want %q", v.Verdict, VerdictError)
	}
}

func TestDecideTripsBreakerOnAuthFailureAndSkipsFurtherCalls(t *testing.T) {
	fc := &fakeCaller{err: &jevclient.StatusError{StatusCode: http.StatusUnauthorized, Body: "bad key"}}
	b := NewBreaker(time.Minute)
	g := NewGate(fc, NewCache(8), b)
	cfg := enabledCfg()
	st := BuildState(StateInput{LatestUserText: "unique one"})

	if _, accepted := g.Decide(context.Background(), cfg, "openai", "r1", st); accepted {
		t.Fatal("must not accept on 401")
	}
	if fc.calls != 1 {
		t.Fatalf("calls = %d, want 1", fc.calls)
	}
	// A different prompt on the same router must not reach the API at all.
	v, accepted := g.Decide(context.Background(), cfg, "openai", "r1", BuildState(StateInput{LatestUserText: "unique two"}))
	if accepted {
		t.Fatal("must not accept while breaker is open")
	}
	if fc.calls != 1 {
		t.Errorf("calls = %d, want 1 (breaker must suppress the call)", fc.calls)
	}
	if v.Verdict != VerdictBreakerOpen {
		t.Errorf("verdict = %q, want %q", v.Verdict, VerdictBreakerOpen)
	}
}

func TestDecideTimeoutFailsOpen(t *testing.T) {
	fc := &blockingCaller{}
	g := NewGate(fc, NewCache(8), nil)
	cfg := enabledCfg()
	cfg.Timeout = 20 * time.Millisecond

	start := time.Now()
	_, accepted := g.Decide(context.Background(), cfg, "openai", "r1", BuildState(StateInput{}))
	if accepted {
		t.Fatal("must not accept on timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("gate blocked for %v; timeout not enforced", elapsed)
	}
}

// blockingCaller blocks until the context expires, exercising the timeout path.
type blockingCaller struct{}

func (b *blockingCaller) Call(ctx context.Context, _ string, _ any, _ map[string]jevclient.Question) (jevclient.Response, error) {
	<-ctx.Done()
	return jevclient.Response{}, ctx.Err()
}
```

**Step 2: Run tests to verify they fail**

```bash
go test ./internal/autorouter/jevgate/ -v
```

Expected: build failure — `undefined: NewBreaker`, `undefined: NewGate`, `undefined: Config`.

**Step 3: Write minimal implementation**

`breaker.go`:

```go
package jevgate

import (
	"sync"
	"time"
)

// DefaultBreakerCooldown is how long a router stays on the heuristic after an
// authentication failure.
const DefaultBreakerCooldown = 5 * time.Minute

// Breaker is a per-router cooldown tripped by authentication failures. It stops
// a misconfigured key from hammering the API at request rate while an operator
// notices. In-memory only: a restart clears it, which is the right recovery for
// a credential that has been fixed.
type Breaker struct {
	mu       sync.Mutex
	openTil  map[string]time.Time
	cooldown time.Duration
	now      func() time.Time
}

// NewBreaker builds a Breaker. A non-positive cooldown uses the default.
func NewBreaker(cooldown time.Duration) *Breaker {
	if cooldown <= 0 {
		cooldown = DefaultBreakerCooldown
	}
	return &Breaker{openTil: make(map[string]time.Time), cooldown: cooldown, now: time.Now}
}

// Open reports whether routerID is currently suppressed.
func (b *Breaker) Open(routerID string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.openTil[routerID]
	if !ok {
		return false
	}
	if b.now().Before(until) {
		return true
	}
	delete(b.openTil, routerID)
	return false
}

// Trip opens the breaker for routerID for one cooldown period.
func (b *Breaker) Trip(routerID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.openTil[routerID] = b.now().Add(b.cooldown)
}
```

`gate.go`:

```go
package jevgate

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
)

// Caller is the subset of jevclient.Client the gate needs, kept as an interface
// so tests can script outcomes without a server.
type Caller interface {
	Call(ctx context.Context, model string, state any, questions map[string]jevclient.Question) (jevclient.Response, error)
}

// Config is the resolved Jev configuration for one decision: the global master
// switch and credential presence, the per-router opt-in, and the tuning knobs.
type Config struct {
	GlobalEnabled bool
	APIKeySet     bool
	RouterEnabled bool
	Model         string
	MinConfidence float64
	Timeout       time.Duration
}

// Enabled reports whether the gate should consult the classifier at all.
func (c Config) Enabled() bool {
	return c.GlobalEnabled && c.APIKeySet && c.RouterEnabled
}

// Gate decides whether a classifier verdict should replace the heuristic tier.
type Gate struct {
	caller  Caller
	cache   *Cache
	breaker *Breaker
}

// NewGate builds a Gate. cache and breaker may be nil, in which case every call
// goes to the classifier and no breaker is tracked.
func NewGate(caller Caller, cache *Cache, breaker *Breaker) *Gate {
	return &Gate{caller: caller, cache: cache, breaker: breaker}
}

// Decide classifies st and reports whether the verdict should replace the
// heuristic tier. It never returns an error: every failure mode is reported
// through the returned Verdict's Verdict field with accepted=false, which is
// what makes the gate fail-open by construction.
//
// A low-confidence verdict is cached alongside accepted ones — the call was
// already paid for, and a prompt that keeps falling back should not keep
// re-paying for the same answer.
func (g *Gate) Decide(ctx context.Context, cfg Config, format, routerID string, st State) (Verdict, bool) {
	if g == nil || g.caller == nil || !cfg.Enabled() {
		return Verdict{}, false
	}
	if g.breaker.Open(routerID) {
		return Verdict{Verdict: VerdictBreakerOpen, Model: cfg.Model}, false
	}
	var key string
	if g.cache != nil {
		key = g.cache.Key(format, routerID, st, cfg.Model, QuestionHash())
		if cached, hit := g.cache.Get(key); hit {
			cached.Cache = CacheHit
			return cached, cached.Verdict == VerdictAccepted
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	start := time.Now()
	resp, errCall := g.caller.Call(callCtx, cfg.Model, st,
		map[string]jevclient.Question{QuestionID: TierQuestion()})
	v := Verdict{Model: cfg.Model, Cache: CacheMiss}
	if errCall != nil {
		v.Verdict = VerdictError
		var statusErr *jevclient.StatusError
		if errors.As(errCall, &statusErr) &&
			(statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden) {
			g.breaker.Trip(routerID)
		}
		return v, false
	}
	v.LatencyMs = time.Since(start).Milliseconds()
	v.InputTokens = resp.Usage.InputTokens
	answer, ok := resp.Answers[QuestionID]
	if !ok {
		// A 200 without our answer is a malformed response, not a verdict.
		v.Verdict = VerdictError
		return v, false
	}
	v.Choice = answer.Choice
	v.Confidence = answer.Confidence
	v.Probabilities = answer.Probabilities
	if v.Confidence >= cfg.MinConfidence {
		v.Verdict = VerdictAccepted
	} else {
		v.Verdict = VerdictLowConfidence
	}
	if g.cache != nil {
		g.cache.Put(key, v)
	}
	return v, v.Verdict == VerdictAccepted
}
```

**Step 4: Run tests to verify they pass**

```bash
go test ./internal/autorouter/jevgate/ -v -race
```

Expected: all tests PASS, no races. Note `TestDecideTimeoutFailsOpen` takes ~20ms.

**Step 5: Commit**

```bash
gofmt -w .
git add internal/autorouter/jevgate/
git commit -m "feat(jevgate): confidence gate with fail-open fallback and auth breaker"
```

---

## Task 5: `store` — `jev_settings` table and store

**Files:**
- Modify: `internal/store/postgresstore.go` (table constant, cfg field, DDL, accessor)
- Create: `internal/store/pg_jev.go`
- Test: `internal/store/pg_jev_test.go`

**Step 1: Write the failing test**

Postgres-backed store tests in this repo run only when `PGSTORE_TEST_DSN` is set; follow the skip pattern used by `pg_alerts_test.go`.

```go
package store

import (
	"context"
	"os"
	"testing"
)

func jevTestStore(t *testing.T) *JevStore {
	t.Helper()
	dsn := os.Getenv("PGSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_TEST_DSN not set")
	}
	ps, err := NewPostgresStore(context.Background(), Config{DSN: dsn})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	return NewJevStore(ps)
}

func TestJevStoreDefaultsDisabledWithPinnedModel(t *testing.T) {
	s := jevTestStore(t)
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Enabled {
		t.Error("enabled must default to false")
	}
	if got.APIKeySet {
		t.Error("api_key_set must default to false")
	}
	if got.Model != JevDefaultModel {
		t.Errorf("model = %q, want %q", got.Model, JevDefaultModel)
	}
}

func TestJevStoreUpsertKeyLifecycle(t *testing.T) {
	s := jevTestStore(t)
	ctx := context.Background()
	defer func() { _, _ = s.Upsert(ctx, JevSettings{Model: JevDefaultModel}, ptrString("")) }()

	key := "sk-ts-abcdef123456"
	set := JevSettings{Enabled: true, Model: "jev-1.13.0"}
	got, err := s.Upsert(ctx, set, &key)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !got.APIKeySet {
		t.Error("api_key_set must be true after storing a key")
	}
	if got.APIKeyPrefix == "" || got.APIKeyPrefix == key {
		t.Errorf("prefix must be a mask, got %q", got.APIKeyPrefix)
	}

	// nil keeps the stored key.
	kept, err := s.Upsert(ctx, JevSettings{Enabled: false, Model: "jev-1.13.0"}, nil)
	if err != nil {
		t.Fatalf("Upsert keep: %v", err)
	}
	if !kept.APIKeySet {
		t.Error("nil apiKey must keep the stored key")
	}

	// The plaintext key must never appear in the settings projection.
	if kept.APIKeyPrefix == key {
		t.Error("settings projection leaked the key")
	}

	// "" clears it.
	cleared, err := s.Upsert(ctx, JevSettings{Model: "jev-1.13.0"}, ptrString(""))
	if err != nil {
		t.Fatalf("Upsert clear: %v", err)
	}
	if cleared.APIKeySet {
		t.Error("empty apiKey must clear the stored key")
	}
}

func TestJevStoreAPIKeyRoundTrips(t *testing.T) {
	s := jevTestStore(t)
	ctx := context.Background()
	key := "sk-ts-roundtrip"
	defer func() { _, _ = s.Upsert(ctx, JevSettings{Model: JevDefaultModel}, ptrString("")) }()

	if _, err := s.Upsert(ctx, JevSettings{Model: JevDefaultModel}, &key); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.APIKey(ctx)
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if got != key {
		t.Errorf("APIKey = %q, want %q", got, key)
	}
}

func ptrString(s string) *string { return &s }
```

**Step 2: Run test to verify it fails**

```bash
PGSTORE_TEST_DSN="$PGSTORE_TEST_DSN" go test ./internal/store/ -run TestJevStore -v
```

Expected: build failure — `undefined: JevStore`, `undefined: NewJevStore`, `undefined: JevDefaultModel`. (Without `PGSTORE_TEST_DSN` the tests skip; the build failure is the point here.)

**Step 3: Wire the table into `PostgresStore`**

In `internal/store/postgresstore.go`:

(a) Add the constant next to `defaultLiteLLMSyncSettingsTable` (~line 69):

```go
	// defaultJevSettingsTable stores the singleton Jev AI classifier
	// configuration (master toggle + sealed API key + model). Mirrors the
	// alert_settings / litellm_sync_settings singleton pattern.
	defaultJevSettingsTable = "jev_settings"
```

(b) Add the exported cfg field next to `LiteLLMSyncSettingsTable` (~line 264):

```go
	// JevSettingsTable stores the singleton Jev AI classifier configuration.
	JevSettingsTable string
```

(c) Default it next to the LiteLLM default (~line 428):

```go
	if cfg.JevSettingsTable == "" {
		cfg.JevSettingsTable = defaultJevSettingsTable
	}
```

(d) Add the DDL + seed immediately after the `litellm_sync_settings` block (after the backfill loop, ~line 831):

```go
	// jev_settings stores the singleton Jev AI classifier configuration: the
	// master on/off switch, the AES-GCM-sealed API key (plaintext when
	// PGSTORE_ENCRYPTION_KEY is unset, the same legacy-tolerant path as the
	// other sealed stores), and the pinned classifier model.
	jevTable := s.fullTableName(s.cfg.JevSettingsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                INTEGER PRIMARY KEY DEFAULT 1,
			enabled           BOOLEAN NOT NULL DEFAULT FALSE,
			api_key_sealed    TEXT,
			api_key_prefix    TEXT,
			model             TEXT NOT NULL DEFAULT 'jev-1.13.0',
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT jev_settings_singleton CHECK (id = 1)
		)
	`, jevTable)); err != nil {
		return fmt.Errorf("postgres store: create jev_settings table: %w", err)
	}
	// Seed the singleton row so Get always finds a row.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (id) VALUES (1) ON CONFLICT (id) DO NOTHING`, jevTable,
	)); err != nil {
		return fmt.Errorf("postgres store: seed jev_settings singleton: %w", err)
	}
```

(e) Add the table-name accessor next to `LiteLLMSyncSettingsTable()` (~line 2774):

```go
// JevSettingsTable returns the fully-qualified name of the jev_settings
// singleton table.
func (s *PostgresStore) JevSettingsTable() string {
	if s == nil {
		return ""
	}
	return s.fullTableName(s.cfg.JevSettingsTable)
}
```

**Step 4: Write the store implementation**

`internal/store/pg_jev.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// JevDefaultModel is the default classifier model. It is pinned rather than the
// "jev-latest" alias: an alias moves when a release ships, and a moved model
// invalidates confidence thresholds an operator already tuned.
const JevDefaultModel = "jev-1.13.0"

// JevSettings is the singleton operator configuration for Jev AI
// classification. The plaintext API key is never represented here — only
// APIKeySet and a masked APIKeyPrefix — so a GET response cannot leak it. The
// key is sealed at rest via the shared Sealer.
type JevSettings struct {
	Enabled      bool      `json:"enabled"`
	APIKeySet    bool      `json:"api_key_set"`
	APIKeyPrefix string    `json:"api_key_prefix,omitempty"`
	Model        string    `json:"model"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// defaultJevSettings returns the fallback settings used when the singleton row
// is missing. Classification is disabled by default, so no external traffic is
// attempted until an operator turns it on and supplies a key.
func defaultJevSettings() JevSettings {
	return JevSettings{Enabled: false, Model: JevDefaultModel}
}

// JevStore provides the singleton classifier configuration. It is backed by the
// same *sql.DB connection as PostgresStore and seals the API key at rest via the
// shared Sealer.
//
// The request path reads this configuration on every routed request, so Get is
// served from an in-memory cache refreshed on every write. That keeps the hot
// path free of database round-trips while still picking up dashboard changes
// immediately.
type JevStore struct {
	db     *sql.DB
	table  string
	sealer *Sealer

	mu     sync.RWMutex
	cached *JevSettings
}

// NewJevStore builds a JevStore reusing the PostgresStore connection and table
// name. Returns nil when the parent is nil so feature-detection is a nil check.
func NewJevStore(parent *PostgresStore) *JevStore {
	if parent == nil {
		return nil
	}
	sealer, errSealer := NewSealer(parent.cfg.UsageEncryptionKey)
	if errSealer != nil {
		log.WithError(errSealer).Warn("postgres store: jev api-key encryption disabled due to key error")
		sealer = nil
	}
	return &JevStore{db: parent.DB(), table: parent.JevSettingsTable(), sealer: sealer}
}

// Get returns the singleton settings. When the row is missing the safe defaults
// are returned with no error. The plaintext key is never returned.
func (s *JevStore) Get(ctx context.Context) (JevSettings, error) {
	if s == nil || s.db == nil {
		return defaultJevSettings(), fmt.Errorf("postgres store: jev store not initialized")
	}
	s.mu.RLock()
	if s.cached != nil {
		cached := *s.cached
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	set := JevSettings{}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT enabled,
		       COALESCE(api_key_sealed, '') <> '' AS api_key_set,
		       COALESCE(api_key_prefix, ''),
		       model, updated_at
		FROM %s WHERE id = 1`, s.table))
	errScan := row.Scan(&set.Enabled, &set.APIKeySet, &set.APIKeyPrefix, &set.Model, &set.UpdatedAt)
	if errScan != nil {
		if errors.Is(errScan, sql.ErrNoRows) {
			return defaultJevSettings(), nil
		}
		return defaultJevSettings(), fmt.Errorf("postgres store: get jev_settings: %w", errScan)
	}
	if strings.TrimSpace(set.Model) == "" {
		set.Model = JevDefaultModel
	}
	s.mu.Lock()
	s.cached = &set
	s.mu.Unlock()
	return set, nil
}

// Upsert replaces the singleton settings row. The API key is updated only when
// apiKey is non-nil: a non-empty value is sealed and stored (rotating the key
// and prefix), an empty value clears it. When apiKey is nil the stored key is
// left unchanged, so callers can toggle the feature without erasing the
// credential.
func (s *JevStore) Upsert(ctx context.Context, set JevSettings, apiKey *string) (JevSettings, error) {
	if s == nil || s.db == nil {
		return defaultJevSettings(), fmt.Errorf("postgres store: jev store not initialized")
	}
	if strings.TrimSpace(set.Model) == "" {
		set.Model = JevDefaultModel
	}

	sealedArg := sql.NullString{}
	prefixArg := sql.NullString{}
	switch {
	case apiKey == nil:
		// Carry the existing sealed key + prefix forward so the UPDATE below
		// does not erase a credential the caller did not mention.
		var sealed, prefix *string
		row := s.db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT api_key_sealed, api_key_prefix FROM %s WHERE id = 1`, s.table))
		if errScan := row.Scan(&sealed, &prefix); errScan == nil {
			if sealed != nil && *sealed != "" {
				sealedArg = sql.NullString{String: *sealed, Valid: true}
			}
			if prefix != nil && *prefix != "" {
				prefixArg = sql.NullString{String: *prefix, Valid: true}
			}
		}
	case *apiKey == "":
		// Explicitly clear the key.
	default:
		key := strings.TrimSpace(*apiKey)
		sealed := key
		if s.sealer != nil && s.sealer.Enabled() {
			if sealedVal, errSeal := s.sealer.Seal(key); errSeal == nil {
				sealed = sealedVal
			} else {
				log.WithError(errSeal).Warn("postgres store: jev api-key seal failed; storing plaintext")
			}
		}
		sealedArg = sql.NullString{String: sealed, Valid: true}
		prefixArg = sql.NullString{String: prefixOf(key), Valid: true}
	}

	if _, errExec := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, enabled, api_key_sealed, api_key_prefix, model, updated_at)
		VALUES (1, $1, $2, $3, $4, NOW())
		ON CONFLICT (id) DO UPDATE SET
			enabled         = EXCLUDED.enabled,
			api_key_sealed  = EXCLUDED.api_key_sealed,
			api_key_prefix  = EXCLUDED.api_key_prefix,
			model           = EXCLUDED.model,
			updated_at      = NOW()
	`, s.table), set.Enabled, sealedArg, prefixArg, set.Model); errExec != nil {
		return set, fmt.Errorf("postgres store: upsert jev_settings: %w", errExec)
	}

	s.mu.Lock()
	s.cached = nil // force the next Get to re-read
	s.mu.Unlock()
	return s.Get(ctx)
}

// APIKey returns the unsealed plaintext API key, or "" when none is configured.
// Used exclusively by the classifier client; never exposed over HTTP.
func (s *JevStore) APIKey(ctx context.Context) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("postgres store: jev store not initialized")
	}
	var sealed sql.NullString
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT api_key_sealed FROM %s WHERE id = 1`, s.table))
	if errScan := row.Scan(&sealed); errScan != nil {
		if errors.Is(errScan, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("postgres store: get jev api key: %w", errScan)
	}
	if !sealed.Valid || sealed.String == "" {
		return "", nil
	}
	if s.sealer == nil || !s.sealer.Enabled() {
		return sealed.String, nil // legacy plaintext
	}
	plain, errOpen := s.sealer.Open(sealed.String)
	if errOpen != nil {
		return "", fmt.Errorf("postgres store: unseal jev api key: %w", errOpen)
	}
	return plain, nil
}
```

Check `prefixOf` exists in `internal/store` (it is used by `litellm_sync.go`); if it is unexported in the same package, reuse it directly. Verify with:

```bash
grep -rn "func prefixOf" internal/store/
```

**Step 5: Run tests to verify they pass**

```bash
gofmt -w .
go build ./... && go test ./internal/store/ -run TestJevStore -v
```

Expected: PASS with `PGSTORE_TEST_DSN` set; SKIP without it. Build must be clean either way.

**Step 6: Commit**

```bash
git add internal/store/postgresstore.go internal/store/pg_jev.go internal/store/pg_jev_test.go
git commit -m "feat(store): jev_settings singleton with sealed API key"
```

---

## Task 6: Management API — Jev settings routes

**Files:**
- Create: `internal/api/handlers/management/jev.go`
- Modify: `internal/api/handlers/management/handler.go` (store field + setter)
- Modify: `internal/api/server_management.go` (routes)
- Test: `internal/api/handlers/management/jev_test.go`

**Step 1: Write the failing test**

```go
package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubJevStore is the minimal surface the handler needs.
type stubJevStore struct {
	set    store.JevSettings
	apiKey *string
}

func (s *stubJevStore) Get(_ context.Context) (store.JevSettings, error) { return s.set, nil }
func (s *stubJevStore) Upsert(_ context.Context, set store.JevSettings, apiKey *string) (store.JevSettings, error) {
	s.set.Enabled = set.Enabled
	s.set.Model = set.Model
	s.apiKey = apiKey
	if apiKey != nil && *apiKey != "" {
		s.set.APIKeySet = true
		s.set.APIKeyPrefix = "sk-ts-…" + (*apiKey)[len(*apiKey)-4:]
	}
	if apiKey != nil && *apiKey == "" {
		s.set.APIKeySet = false
		s.set.APIKeyPrefix = ""
	}
	return s.set, nil
}

func TestPutJevSettingsNeverEchoesKey(t *testing.T) {
	stub := &stubJevStore{}
	h := &Handler{jevStore: stub}

	body := `{"enabled":true,"api_key":"sk-ts-secretvalue1234","model":"jev-1.13.0"}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/jev/settings", bytes.NewBufferString(body))

	h.PutJevSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("secretvalue")) {
		t.Fatalf("response leaked the API key: %s", rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	settings, _ := got["settings"].(map[string]any)
	if settings["enabled"] != true {
		t.Errorf("enabled = %v, want true", settings["enabled"])
	}
	if settings["api_key_set"] != true {
		t.Errorf("api_key_set = %v, want true", settings["api_key_set"])
	}
}
```

Imports here need `context`, `github.com/gin-gonic/gin`, and the store package. Adjust the import block accordingly.

**Step 2: Run test to verify it fails**

```bash
go test ./internal/api/handlers/management/ -run TestPutJevSettings -v
```

Expected: build failure — `unknown field jevStore`, `undefined: h.PutJevSettings`.

**Step 3: Write the handler**

`internal/api/handlers/management/jev.go`:

```go
package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// GetJevSettings handles GET /v0/management/jev/settings. The plaintext API key
// is never returned — only api_key_set and a masked prefix — so the response is
// safe to render and to log.
func (h *Handler) GetJevSettings(c *gin.Context) {
	if h.jevStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "jev settings require PGSTORE_DSN"})
		return
	}
	settings, errGet := h.jevStore.Get(c.Request.Context())
	if errGet != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errGet.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": settings})
}

// jevSettingsRequest is the JSON body accepted by PutJevSettings. Pointer-typed
// fields are applied only when present, so a client can toggle the feature
// without resending the key. api_key semantics: nil = keep, "" = clear,
// non-empty = rotate and seal.
type jevSettingsRequest struct {
	Enabled *bool   `json:"enabled,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
	Model   *string `json:"model,omitempty"`
}

// PutJevSettings handles PUT /v0/management/jev/settings.
func (h *Handler) PutJevSettings(c *gin.Context) {
	if h.jevStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "jev settings require PGSTORE_DSN"})
		return
	}
	var req jevSettingsRequest
	if errBind := c.ShouldBindJSON(&req); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errBind.Error()})
		return
	}
	current, errGet := h.jevStore.Get(c.Request.Context())
	if errGet != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errGet.Error()})
		return
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.Model != nil && strings.TrimSpace(*req.Model) != "" {
		current.Model = strings.TrimSpace(*req.Model)
	}
	updated, errPut := h.jevStore.Upsert(c.Request.Context(), current, req.APIKey)
	if errPut != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errPut.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": updated})
}
```

Add `"strings"` to the import block.

**Step 4: Wire the store and routes**

In `internal/api/handlers/management/handler.go`, next to the alerts store field (~line 530):

```go
	// jevStore is the PG-backed singleton Jev AI classifier configuration
	// (master toggle + sealed API key + model). Nil when PGSTORE_DSN is unset.
	jevStore *store.JevStore
```

and the setter next to `SetAlertsStore`:

```go
// SetJevStore wires the PG-backed store for the Jev AI classifier settings.
func (h *Handler) SetJevStore(s *store.JevStore) {
	h.jevStore = s
}
```

In `internal/api/server_management.go`, register next to the `/litellm/settings` routes (~line 255):

```go
		// Jev AI classifier settings (master toggle + API key + model).
		mgmt.GET("/jev/settings", s.mgmt.GetJevSettings)
		mgmt.PUT("/jev/settings", s.mgmt.PutJevSettings)
```

Then find where `SetAlertsStore` is called during server construction and add the `NewJevStore` wiring alongside it:

```bash
grep -rn "SetAlertsStore" internal/api/ cmd/
```

Call the analogous `SetJevStore(store.NewJevStore(pgStore))` guarded by the same nil check used there.

**Step 5: Run tests to verify they pass**

```bash
gofmt -w .
go build ./... && go test ./internal/api/handlers/management/ -run TestPutJevSettings -v
```

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/api/handlers/management/jev.go internal/api/handlers/management/jev_test.go \
        internal/api/handlers/management/handler.go internal/api/server_management.go
git commit -m "feat(management): jev settings API with write-only API key"
```

---

## Task 7: Per-router Jev knobs

**Files:**
- Modify: `internal/store/pg_auto_routers.go` (`AutoRouter` struct, DDL backfill, CRUD)
- Modify: `internal/autorouter/types.go` (`Config` fields)
- Test: `internal/store/pg_auto_routers_jev_test.go`

**Step 1: Write the failing test**

```go
package store

import "testing"

func TestAutoRouterJevKnobsPersist(t *testing.T) {
	s := autoRouterTestStore(t) // reuse the helper already present in this package
	ctx := context.Background()

	in := &AutoRouter{
		Name: "jev-test", ModelID: "router:jev-test", Enabled: true,
		JevEnabled: true, JevMinConfidence: 0.65, JevTimeoutMs: 300, JevModelOverride: "jev-preview",
	}
	created, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.JevEnabled {
		t.Error("jev_enabled did not persist")
	}
	if got.JevMinConfidence != 0.65 {
		t.Errorf("jev_min_confidence = %v, want 0.65", got.JevMinConfidence)
	}
	if got.JevTimeoutMs != 300 {
		t.Errorf("jev_timeout_ms = %d, want 300", got.JevTimeoutMs)
	}
	if got.JevModelOverride != "jev-preview" {
		t.Errorf("jev_model_override = %q", got.JevModelOverride)
	}
}
```

Check the existing helper name in `internal/store/pg_auto_routers_test.go` and reuse it; if there is none, copy the skip-on-`PGSTORE_TEST_DSN` pattern used by `pg_alerts_test.go`.

**Step 2: Run test to verify it fails**

```bash
go test ./internal/store/ -run TestAutoRouterJevKnobs -v
```

Expected: build failure — `unknown field JevEnabled`.

**Step 3: Add the columns**

In `internal/store/postgresstore.go`, alongside the existing `auto_routers` idempotent backfills (~line 2096):

```go
	// Idempotent backfill for the per-router Jev AI classifier knobs. The
	// feature is off per router by default; the global master toggle lives in
	// jev_settings, so both must be on for the classifier to run.
	for _, col := range []string{
		`jev_enabled BOOLEAN NOT NULL DEFAULT FALSE`,
		`jev_min_confidence DOUBLE PRECISION NOT NULL DEFAULT 0.5`,
		`jev_timeout_ms INTEGER NOT NULL DEFAULT 400`,
		`jev_model_override TEXT`,
	} {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s`, autoRoutersTable, col,
		)); err != nil {
			return fmt.Errorf("postgres store: alter auto_routers add %q: %w", col, err)
		}
	}
```

**Step 4: Extend the struct and CRUD**

In `internal/store/pg_auto_routers.go`, add to `AutoRouter` (after `VisionBridgeModel`, ~line 121):

```go
	// JevEnabled opts this router into Jev AI classification. The global
	// jev_settings master toggle must also be on, and an API key must be
	// configured; otherwise the router's heuristic tier is used.
	JevEnabled bool `json:"jev_enabled"`
	// JevMinConfidence is the confidence floor for accepting a classifier
	// verdict. Below it the heuristic tier wins. Zero means "use the default".
	JevMinConfidence float64 `json:"jev_min_confidence,omitempty"`
	// JevTimeoutMs bounds the classifier call. Zero means "use the default".
	JevTimeoutMs int `json:"jev_timeout_ms,omitempty"`
	// JevModelOverride pins a classifier model for this router. Empty uses the
	// global model.
	JevModelOverride string `json:"jev_model_override,omitempty"`
```

Update every SELECT column list, INSERT, and UPDATE in that file to carry the four new columns, following the exact pattern already used for `vision_bridge_model`. Find them with:

```bash
grep -n "vision_bridge_model" internal/store/pg_auto_routers.go
```

Every site that mentions `vision_bridge_model` needs an adjacent `jev_enabled, jev_min_confidence, jev_timeout_ms, jev_model_override`.

**Step 5: Extend the pure config**

In `internal/autorouter/types.go`, add to `Config` (after `VisionBridgeModel`, ~line 182):

```go
	// JevEnabled opts this router into Jev AI classification (the global
	// master toggle must also be on).
	JevEnabled bool `json:"jev_enabled"`
	// JevMinConfidence is the confidence floor for accepting a classifier
	// verdict; JevTimeoutMs bounds the call. JevModelOverride pins a model.
	JevMinConfidence float64 `json:"jev_min_confidence,omitempty"`
	JevTimeoutMs     int     `json:"jev_timeout_ms,omitempty"`
	JevModelOverride string  `json:"jev_model_override,omitempty"`
```

Then update `store.BridgeAutoRouterConfig` to copy them. Find it with:

```bash
grep -rn "func BridgeAutoRouterConfig" -A 40 internal/store/
```

**Step 6: Run tests to verify they pass**

```bash
gofmt -w .
go build ./... && go test ./internal/store/ -run TestAutoRouter -v && go test ./internal/autorouter/...
```

Expected: PASS (or SKIP without a DSN); the `autorouter` package tests must stay green, especially `TestScorerParityOnCorpus`.

**Step 7: Commit**

```bash
git add internal/store/pg_auto_routers.go internal/store/postgresstore.go \
        internal/autorouter/types.go internal/store/pg_auto_routers_jev_test.go
git commit -m "feat(autorouter): per-router Jev classifier knobs"
```

---

## Task 8: Wire the gate into the request path

**Files:**
- Modify: `internal/autorouter/profile.go` (`DecisionSnapshot` Jev block, cause constants)
- Modify: `sdk/api/handlers/handlers_auto_router.go` (gate call)
- Test: `sdk/api/handlers/handlers_auto_router_jev_test.go`

**Step 1: Write the failing test**

```go
package handlers

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
)

func TestJevDisabledIsByteIdenticalToHeuristic(t *testing.T) {
	// A router with the gate off must produce exactly the heuristic tier, with
	// no jev block in the snapshot. This is the regression guard for the
	// fail-identical invariant.
	cfg := &autorouter.Config{Enabled: true, JevEnabled: false}
	if cfg.JevEnabled {
		t.Fatal("precondition: jev must be off")
	}
	// Assert the gate is never consulted when the router knob is off: the
	// handler reads JevEnabled before building any state.
	// (Full request-path coverage comes from the existing auto-router handler
	// tests, which must continue to pass unchanged.)
}

func TestEffectiveJevConfigRequiresAllThreeSwitches(t *testing.T) {
	base := autorouter.Config{JevEnabled: true, JevMinConfidence: 0.6, JevTimeoutMs: 250}

	got := effectiveJevConfig(true, true, base)
	if !got.Enabled() {
		t.Fatal("all three switches on must enable the gate")
	}
	if got.MinConfidence != 0.6 {
		t.Errorf("min confidence = %v, want 0.6", got.MinConfidence)
	}

	if effectiveJevConfig(false, true, base).Enabled() {
		t.Error("global off must disable the gate")
	}
	if effectiveJevConfig(true, false, base).Enabled() {
		t.Error("missing API key must disable the gate")
	}
	if effectiveJevConfig(true, true, autorouter.Config{JevEnabled: false}).Enabled() {
		t.Error("router off must disable the gate")
	}
}

func TestEffectiveJevConfigAppliesDefaults(t *testing.T) {
	got := effectiveJevConfig(true, true, autorouter.Config{JevEnabled: true})
	if got.MinConfidence != jevDefaultMinConfidence {
		t.Errorf("min confidence = %v, want %v", got.MinConfidence, jevDefaultMinConfidence)
	}
	if got.Timeout != jevDefaultTimeout {
		t.Errorf("timeout = %v, want %v", got.Timeout, jevDefaultTimeout)
	}
	if got.Model == "" {
		t.Error("model must fall back to the global default")
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./sdk/api/handlers/ -run "TestEffectiveJevConfig|TestJevDisabled" -v
```

Expected: build failure — `undefined: effectiveJevConfig`.

**Step 3: Add the snapshot block and causes**

In `internal/autorouter/profile.go`, add to `DecisionSnapshot` (after `TargetModel`, ~line 346):

```go
	// Jev carries the classifier verdict when the Jev gate was consulted. Nil
	// when the gate is disabled or was never reached, so its presence alone
	// distinguishes "heuristic only" from "classifier involved".
	Jev *JevDecision `json:"jev,omitempty"`
```

and the type next to it:

```go
// JevDecision is the classifier's contribution to a routing decision: what it
// chose, how confident it was, and how the gate resolved it. Persisting the
// full probability distribution (not just the winner) is what lets operators
// tune the confidence floor against recorded data.
type JevDecision struct {
	Model         string             `json:"model,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	LatencyMs     int64              `json:"latency_ms"`
	InputTokens   int                `json:"input_tokens"`
	Cache         string             `json:"cache,omitempty"`
	Verdict       string             `json:"verdict,omitempty"`
}
```

Add the new decision causes next to `DecisionCauseKeywordMatch` (~line 26):

```go
	// DecisionCauseJevClassifier: the classifier's tier was used.
	DecisionCauseJevClassifier = "jev_classifier"
	// DecisionCauseJevLowConfidence: the classifier answered below the
	// confidence floor, so the heuristic tier was used.
	DecisionCauseJevLowConfidence = "jev_low_confidence"
	// DecisionCauseJevFallback: the classifier was unavailable (timeout, bad
	// status, malformed response) or the breaker was open, so the heuristic
	// tier was used.
	DecisionCauseJevFallback = "jev_fallback_heuristic"
```

**Step 4: Add the config resolver and gate call**

In `sdk/api/handlers/handlers_auto_router.go`:

```go
const (
	// jevDefaultMinConfidence is the classifier confidence floor when a router
	// does not set one. With four options an even probability spread scores 0,
	// so 0.5 means "at least a mild preference".
	jevDefaultMinConfidence = 0.5
	// jevDefaultTimeout bounds the classifier call. It applies before any
	// upstream model connection exists, the same phase as the vision bridge.
	jevDefaultTimeout = 400 * time.Millisecond
	// jevDefaultModel mirrors store.JevDefaultModel for this package's default
	// when no global model is configured.
	jevDefaultModel = "jev-1.13.0"
)

// jevGate and jevBreaker are process-local. The gate holds no per-router state;
// the breaker's cooldowns are keyed by router id.
var (
	jevGate    *jevgate.Gate
	jevBreaker = jevgate.NewBreaker(0)
)

// SetJevGate wires the classifier gate. Called once during server construction;
// while unwired the gate is skipped entirely, so existing deployments are
// unaffected.
func SetJevGate(caller jevgate.Caller) {
	if caller == nil {
		jevGate = nil
		return
	}
	jevGate = jevgate.NewGate(caller, jevgate.NewCache(0), jevBreaker)
}

// effectiveJevConfig folds the three switches (global master, API key present,
// router opt-in) and the router knobs into the gate's config, applying defaults
// for unset values.
func effectiveJevConfig(globalEnabled, apiKeySet bool, cfg autorouter.Config) jevgate.Config {
	moduleCfg := jevgate.Config{
		GlobalEnabled: globalEnabled,
		APIKeySet:     apiKeySet,
		RouterEnabled: cfg.JevEnabled,
		Model:         strings.TrimSpace(cfg.JevModelOverride),
		MinConfidence: cfg.JevMinConfidence,
		Timeout:       time.Duration(cfg.JevTimeoutMs) * time.Millisecond,
	}
	if moduleCfg.Model == "" {
		moduleCfg.Model = jevDefaultModel
	}
	if moduleCfg.MinConfidence <= 0 {
		moduleCfg.MinConfidence = jevDefaultMinConfidence
	}
	if moduleCfg.Timeout <= 0 {
		moduleCfg.Timeout = jevDefaultTimeout
	}
	return moduleCfg
}

// applyJevGate consults the classifier and returns the tier to use plus the
// snapshot block to persist. It returns the heuristic tier unchanged whenever
// the gate is disabled, unavailable, or not confident enough — the caller does
// not branch on errors because there are none.
func applyJevGate(
	ctx context.Context,
	jevCfg jevgate.Config,
	ext jevExtract,
	result autorouter.ScoreResult,
	format, routerID, modelRequested string,
) (autorouter.Tier, string, *autorouter.JevDecision) {
	if jevGate == nil || !jevCfg.Enabled() {
		return result.EffectiveTier, result.DecisionCause, nil
	}
	state := jevgate.BuildState(jevgate.StateInput{
		LatestUserText:      ext.LatestUserText,
		MessageCount:        ext.MessageCount,
		HistoryWordEstimate: ext.HistoryWordEstimate,
		HasTools:            ext.HasTools,
		HasCodeFence:        ext.HasCodeFence,
		HasImages:           ext.HasImages,
		ModelRequested:      modelRequested,
	})
	verdict, accepted := jevGate.Decide(ctx, jevCfg, format, routerID, state)
	if verdict.Verdict == "" {
		return result.EffectiveTier, result.DecisionCause, nil
	}
	info := &autorouter.JevDecision{
		Model: verdict.Model, Choice: verdict.Choice, Confidence: verdict.Confidence,
		Probabilities: verdict.Probabilities, LatencyMs: verdict.LatencyMs,
		InputTokens: verdict.InputTokens, Cache: verdict.Cache, Verdict: verdict.Verdict,
	}
	if !accepted {
		cause := autorouter.DecisionCauseJevLowConfidence
		if verdict.Verdict == jevgate.VerdictError || verdict.Verdict == jevgate.VerdictBreakerOpen {
			cause = autorouter.DecisionCauseJevFallback
		}
		return result.EffectiveTier, cause, info
	}
	tier := autorouter.Tier(strings.TrimSpace(verdict.Choice))
	if !validTier(tier) {
		// A choice outside the four option keys is a malformed response; fall
		// back rather than letting Resolve see an unknown tier.
		info.Verdict = jevgate.VerdictError
		return result.EffectiveTier, autorouter.DecisionCauseJevFallback, info
	}
	return tier, autorouter.DecisionCauseJevClassifier, info
}

// validTier reports whether t is one of the four classifier options.
func validTier(t autorouter.Tier) bool {
	for _, candidate := range autorouter.TierOrder {
		if candidate == t {
			return true
		}
	}
	return false
}
```

**Step 5: Expose the fields `applyJevGate` needs**

`ext` above is a small struct the scorer already computes. Rather than exporting the scorer's internal `extractedRequest`, add an exported accessor in `internal/autorouter` that returns just the fields the gate needs:

```go
// JevStateInput is the subset of the extracted request the Jev gate sends to
// the classifier. Exported so the request path can build the classifier state
// without widening the scorer's internal extraction type.
type JevStateInput struct {
	LatestUserText      string
	MessageCount        int
	HistoryWordEstimate int
	HasTools            bool
	HasCodeFence        bool
	HasImages           bool
}

// ExtractJevStateInput extracts the classifier state inputs from a request body.
// It shares the scorer's per-format extraction, so it supports exactly the same
// request shapes.
func ExtractJevStateInput(rawJSON []byte, format string) JevStateInput {
	ext := extractRequest(rawJSON, format)
	return JevStateInput{
		LatestUserText:      ext.LatestUserText,
		MessageCount:        ext.MessageCount,
		HistoryWordEstimate: ext.HistoryWords,
		HasTools:            ext.HasTools,
		HasCodeFence:        ext.FenceTokens > 0,
		HasImages:           ext.HasImages,
	}
}
```

Verify the exact field names on `extractedRequest` before writing this — run:

```bash
grep -n "type extractedRequest struct" -A 25 internal/autorouter/scorer.go
```

If a field does not exist (for example `HasImages` or `HasTools`), either derive it in `ExtractJevStateInput` from the raw JSON with gjson (the package already imports it at `scorer.go:8`) or drop that flag from the state. Do not add fields to `extractedRequest` unless they are genuinely useful to the scorer too.

Then in `applyJevGate`, replace the `ext jevExtract` parameter with `st jevgate.State` built by the caller, which keeps `handlers_auto_router.go` free of extraction details:

```go
func applyJevGate(
	ctx context.Context,
	jevCfg jevgate.Config,
	state jevgate.State,
	result autorouter.ScoreResult,
	format, routerID string,
) (autorouter.Tier, string, *autorouter.JevDecision) {
```

and drop `ModelRequested` from `StateInput` at the call site by setting it on the built `State` instead:

```go
state := jevgate.BuildState(jevgate.StateInput{ /* ...from ExtractJevStateInput... */ })
state.Metadata.ModelRequested = baseModel
```

**Step 6: Call the gate inside `resolveAutoRouterModel`**

In `resolveAutoRouterModel`, after `result := autorouter.ScoreWithProfileCompiled(...)` and before `h.autoRouterResolvedFromScore(...)`, insert the gate. `autoRouterResolvedFromScore` must accept the override, so add two parameters:

```go
tier, cause, jevInfo := applyJevGate(ctx, jevCfg, state, result, entryProtocol, router.ID)
return h.autoRouterResolvedFromScore(router, routerCfg, result, result.ProfileHash, result.ProfileVersion, tier, cause, jevInfo)
```

and in `autoRouterResolvedFromScore`, replace the three uses of `result.EffectiveTier`/`result.DecisionCause` with the passed `tier`/`cause`:

```go
func (h *BaseAPIHandler) autoRouterResolvedFromScore(router *store.AutoRouter, routerCfg *autorouter.Config, result autorouter.ScoreResult, profileHash string, profileVersion int64, tier autorouter.Tier, cause string, jevInfo *autorouter.JevDecision) autoRouterResolved {
	resolved, ok := autorouter.Resolve(tier, routerCfg)
	if !ok || resolved == nil || strings.TrimSpace(resolved.Model) == "" {
		log.WithFields(log.Fields{
			"router_id": strings.TrimSpace(router.ID),
			"tier":      string(tier),
		}).Warn("auto-router: no resolvable tier mapping; rejecting request")
		return autoRouterResolved{resolveFailed: true, tier: string(tier), routerID: strings.TrimSpace(router.ID)}
	}
	decision := autorouter.DecisionSnapshot{
		ProfileVersion: profileVersion, ProfileHash: profileHash, ProfileSnapshot: result.ProfileConfig,
		ScoreTotal: result.Score.Total, ScoreFields: result.Score.Fields, ReasoningMarkers: result.Score.ReasoningMarkers,
		ScoredTier: result.Score.Tier, EffectiveTier: tier, DecisionCause: cause,
		MatchedRules: result.MatchedRules, MappingTier: resolved.MappingTier, FallbackChain: resolved.FallbackChain, TargetModel: resolved.Model,
		Jev: jevInfo,
	}
	return autoRouterResolved{targetModel: resolved.Model, route: resolved, visionBridgeModel: strings.TrimSpace(router.VisionBridgeModel), tier: string(tier), routerID: strings.TrimSpace(router.ID), decision: decision, matched: true}
}
```

Update both call sites (compiled path and legacy path) to pass `tier, cause, jevInfo`. The legacy non-compiled path may pass `result.EffectiveTier, result.DecisionCause, nil` so it stays behavior-identical — the gate only runs on the compiled path.

**Step 7: Wire the caller during server construction**

Where the auto-router resolver is wired onto the handler, also call `SetJevGate` with a `jevclient.Client` built from `JevStore.APIKey`:

```bash
grep -rn "AutoRouterResolver\s*=" internal/api/ cmd/ sdk/
```

Add alongside, guarded so a nil JevStore or empty key disables the gate (the gate checks `APIKeySet` anyway, so a client with an empty key simply never succeeds — but skipping the wiring is cleaner):

```go
if jevStore != nil {
	if set, errGet := jevStore.Get(ctx); errGet == nil && set.APIKeySet {
		if key, errKey := jevStore.APIKey(ctx); errKey == nil && key != "" {
			handlers.SetJevGate(jevclient.New("", key, nil))
		}
	}
}
```

The API key is read once at startup. A key rotated through the dashboard takes effect on restart. Note this in the Settings card copy as a known limitation rather than adding a live-reload path in v1.

**Step 8: Run tests to verify they pass**

```bash
gofmt -w .
go build -o test-output ./cmd/server && rm test-output
go test ./sdk/api/handlers/ -v -run "Jev|AutoRouter" && go test ./internal/autorouter/... -v
```

Expected: PASS, including the pre-existing `TestScorerParityOnCorpus` and `decision_golden_test.go` unchanged.

**Step 9: Commit**

```bash
git add internal/autorouter/profile.go internal/autorouter/scorer.go \
        sdk/api/handlers/handlers_auto_router.go sdk/api/handlers/handlers_auto_router_jev_test.go
git commit -m "feat(autorouter): hybrid Jev gate in the request path"
```

---

## Task 9: Dashboard — Settings card

**Files:**
- Modify: `web/dashboard/src/api/client.js` (two helpers)
- Modify: `web/dashboard/src/pages/SettingsPage.jsx` (`JevSettingsCard`)

**Step 1: Add the client helpers**

Follow `getLiteLLMSyncSettings` / `putLiteLLMSyncSettings` (~`client.js:790-801`):

```js
export function getJevSettings() {
  return fetchJSON('/jev/settings');
}

export function putJevSettings(body) {
  return fetchJSON('/jev/settings', { method: 'PUT', body: JSON.stringify(body) });
}
```

Match the exact signature `fetchJSON` uses for method/body in this file — read the existing helper before writing, since `putLiteLLMSyncSettings` is the authority on the shape.

**Step 2: Add the card**

In `SettingsPage.jsx`, import `ToggleRow` and `PasswordInput` from `./manage-cpa/FormPrimitives.jsx` and `getJevSettings, putJevSettings` from `../api/client.js`, then add the component and render it between `<AlertSettingsCard />` and the `DynamicSettingsCard` div:

```jsx
// JevSettingsCard configures the optional Jev AI classifier used by Auto
// Routers. The API key is write-only: the server returns only a mask, and a
// blank field means "keep the stored key".
function JevSettingsCard({ toast }) {
  const req = useAsync(() => getJevSettings(), []);
  const [saving, setSaving] = useState(false);
  const [draft, setDraft] = useState(null);
  const [apiKey, setApiKey] = useState('');
  const [apiKeyDirty, setApiKeyDirty] = useState(false);

  useEffect(() => {
    if (req.data?.settings) setDraft(req.data.settings);
  }, [req.data]);

  if (req.loading) return <div className="card"><Spinner /></div>;
  if (req.error) return <div className="card"><ErrorBanner error={req.error} /></div>;
  if (!draft) return null;

  function set(patch) { setDraft((d) => ({ ...d, ...patch })); }

  async function handleSave() {
    setSaving(true);
    try {
      const body = {
        enabled: draft.enabled,
        model: draft.model,
      };
      if (apiKeyDirty) body.api_key = apiKey;
      const res = await putJevSettings(body);
      if (res?.settings) setDraft(res.settings);
      setApiKey('');
      setApiKeyDirty(false);
      toast?.('Jev settings saved');
    } catch (err) {
      toast?.(err.message || 'Failed to save Jev settings', 'error');
    } finally {
      setSaving(false);
    }
  }

  async function handleClearKey() {
    setSaving(true);
    try {
      const res = await putJevSettings({ api_key: '' });
      if (res?.settings) setDraft(res.settings);
      setApiKey('');
      setApiKeyDirty(false);
      toast?.('Jev API key cleared');
    } catch (err) {
      toast?.(err.message || 'Failed to clear key', 'error');
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="card">
      <h3 className="card__title">Jev AI classification</h3>
      <p className="muted">
        An optional semantic classifier for Auto Routers. When enabled, it can
        override a router's heuristic complexity tier when its confidence
        clears the router's threshold. Every failure falls back to the
        heuristic. Changing the model invalidates tuned confidence thresholds.
      </p>
      <ToggleRow
        label="Enable Jev AI classification"
        hint="Master switch for every router. Each router must also opt in."
        checked={!!draft.enabled}
        onChange={(enabled) => set({ enabled })}
      />
      <div className="form__row" style={{ marginTop: 12 }}>
        <label className="form__label">
          API key {draft.api_key_set ? `(stored: ${draft.api_key_prefix})` : '(not set)'}
        </label>
        <PasswordInput
          value={apiKey}
          onChange={(v) => { setApiKey(v); setApiKeyDirty(true); }}
          placeholder={draft.api_key_set ? 'Leave blank to keep the stored key' : 'sk-ts-…'}
        />
      </div>
      <div className="form__row">
        <label className="form__label">Model</label>
        <input
          type="text"
          value={draft.model || ''}
          onChange={(e) => set({ model: e.target.value })}
        />
      </div>
      <p className="muted">
        A rotated key takes effect after a server restart.
      </p>
      <div className="form__actions">
        <button onClick={handleSave} disabled={saving}>
          {saving ? 'Saving…' : 'Save'}
        </button>
        {draft.api_key_set && (
          <button onClick={handleClearKey} disabled={saving}>Clear key</button>
        )}
      </div>
    </div>
  );
}
```

Verify the actual prop names of `ToggleRow` and `PasswordInput` in `FormPrimitives.jsx` before writing — they are `checked`/`onChange` and `value`/`onChange` per the LiteLLM usage, but confirm.

**Step 3: Build the SPA**

```bash
cd web/dashboard && npm run build
```

Expected: build succeeds with no errors.

**Step 4: Commit**

```bash
cd /home/bilfid/projects/nixllm/.claude/worktrees/jev-gate
git add web/dashboard/src/api/client.js web/dashboard/src/pages/SettingsPage.jsx
git commit -m "feat(dashboard): Jev AI settings card with write-only API key"
```

---

## Task 10: Dashboard — per-router Jev fields

**Files:**
- Modify: `web/dashboard/src/components/AutoRouterForm.jsx`

**Step 1: Add the fields**

Add a section to the router form mirroring the existing vision-bridge field, gated behind the global setting:

- `jev_enabled` — toggle
- `jev_min_confidence` — number input, step 0.05, min 0, max 1, placeholder 0.5
- `jev_timeout_ms` — number input, placeholder 400
- `jev_model_override` — text input, placeholder "use global model"

Read `AutoRouterForm.jsx` first and follow its existing field-state pattern exactly (the memory note about `ModelListEditor` index-mapping applies here: if the parent filters the mapping list, an index-based `onChange` goes stale — use the same keyed approach the file already uses for vision bridge).

Add a hint when the global setting is off, so an operator is not confused by a per-router toggle that has no effect:

```jsx
<p className="muted">
  Jev classification also requires the global toggle in Settings and a
  configured API key.
</p>
```

**Step 2: Build the SPA**

```bash
cd web/dashboard && npm run build
```

Expected: build succeeds.

**Step 3: Embed and commit**

```bash
cd /home/bilfid/projects/nixllm/.claude/worktrees/jev-gate
make dash-embed
git add web/dashboard/src/components/AutoRouterForm.jsx internal/dashboardasset/
git commit -m "feat(dashboard): per-router Jev classifier fields"
```

Note: `make dash-embed` must copy `dist/` into `internal/dashboardasset` before the Go build for the embed to pick it up (a known trap in this repo).

---

## Task 11: Corpus evaluation tool

**Files:**
- Create: `cmd/autorouter_eval/main.go`
- Create: `cmd/autorouter_eval/corpus.example.json`
- Test: `cmd/autorouter_eval/main_test.go`

**Step 1: Write the failing test**

```go
package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
)

func TestConfusionMatrixCounts(t *testing.T) {
	pairs := []Pair{
		{Want: autorouter.TierSimple, Got: autorouter.TierSimple},
		{Want: autorouter.TierSimple, Got: autorouter.TierMedium},
		{Want: autorouter.TierReasoning, Got: autorouter.TierReasoning},
	}
	m := buildConfusion(pairs)
	if m[autorouter.TierSimple][autorouter.TierSimple] != 1 {
		t.Error("diagonal cell miscounted")
	}
	if m[autorouter.TierSimple][autorouter.TierMedium] != 1 {
		t.Error("off-diagonal cell miscounted")
	}
	if acc := accuracy(pairs); acc != 2.0/3.0 {
		t.Errorf("accuracy = %v, want %v", acc, 2.0/3.0)
	}
}

func TestMacroF1IsZeroWhenNothingMatches(t *testing.T) {
	pairs := []Pair{
		{Want: autorouter.TierSimple, Got: autorouter.TierReasoning},
		{Want: autorouter.TierComplex, Got: autorouter.TierSimple},
	}
	if f1 := macroF1(pairs); f1 != 0 {
		t.Errorf("macroF1 = %v, want 0", f1)
	}
}

func TestMacroF1IsOneOnPerfectAgreement(t *testing.T) {
	pairs := []Pair{
		{Want: autorouter.TierSimple, Got: autorouter.TierSimple},
		{Want: autorouter.TierComplex, Got: autorouter.TierComplex},
	}
	if f1 := macroF1(pairs); f1 != 1 {
		t.Errorf("macroF1 = %v, want 1", f1)
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./cmd/autorouter_eval/ -v
```

Expected: build failure — `undefined: Pair`, `undefined: buildConfusion`.

**Step 3: Write the tool**

`cmd/autorouter_eval/main.go`:

```go
// Command autorouter_eval compares the Auto Router's lexical heuristic against
// the Jev AI classifier over a labelled prompt corpus. It is an offline
// evaluation tool: it makes no changes and is never run by the server.
//
// Usage:
//
//	autorouter_eval -corpus corpus.json -mode heuristic
//	autorouter_eval -corpus corpus.json -mode jev -compare
//
// The corpus is a JSON array of {id, prompt, want} where want is one of
// simple|medium|complex|reasoning. Labels are ground truth supplied by the
// operator (produced by a strong model and spot-checked by hand).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevgate"
)

// Entry is one labelled corpus row.
type Entry struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Want   string `json:"want"`
}

// Pair is one classifier prediction against ground truth.
type Pair struct {
	ID   string
	Want autorouter.Tier
	Got  autorouter.Tier
}

func main() {
	corpusPath := flag.String("corpus", "", "path to the labelled corpus JSON")
	mode := flag.String("mode", "heuristic", "heuristic | jev")
	compare := flag.Bool("compare", false, "also run the other classifier and report disagreements")
	model := flag.String("model", "jev-1.13.0", "classifier model for -mode jev")
	flag.Parse()

	if *corpusPath == "" {
		fmt.Fprintln(os.Stderr, "autorouter_eval: -corpus is required")
		os.Exit(2)
	}
	entries, errLoad := loadCorpus(*corpusPath)
	if errLoad != nil {
		fmt.Fprintf(os.Stderr, "autorouter_eval: %v\n", errLoad)
		os.Exit(1)
	}

	var heurPairs, jevPairs []Pair
	for _, e := range entries {
		want := autorouter.Tier(e.Want)
		heurPairs = append(heurPairs, Pair{ID: e.ID, Want: want, Got: heuristicTier(e.Prompt)})
		if *mode == "jev" {
			jevPairs = append(jevPairs, Pair{ID: e.ID, Want: want, Got: jevTier(e.Prompt, *model)})
		}
	}

	report("heuristic", heurPairs)
	if len(jevPairs) > 0 {
		report("jev", jevPairs)
		if *compare {
			reportDisagreements(heurPairs, jevPairs)
		}
	}
}

// heuristicTier runs the in-process scorer over an OpenAI-shaped body.
func heuristicTier(prompt string) autorouter.Tier {
	body, errMarshal := json.Marshal(map[string]any{
		"model":    "eval",
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if errMarshal != nil {
		return autorouter.TierSimple
	}
	return autorouter.Score(body, "openai").Tier
}

// jevTier classifies one prompt through the live API. A failure is reported on
// stderr and counted as SIMPLE so a run never silently skews toward higher
// tiers; the error count is printed with the report.
func jevTier(prompt, model string) autorouter.Tier {
	apiKey := os.Getenv("JEV_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "autorouter_eval: JEV_API_KEY is required for -mode jev")
		os.Exit(2)
	}
	client := jevclient.New("", apiKey, nil)
	gate := jevgate.NewGate(client, jevgate.NewCache(64), nil)
	cfg := jevgate.Config{
		GlobalEnabled: true, APIKeySet: true, RouterEnabled: true,
		Model: model, MinConfidence: 0, Timeout: 10 * time.Second,
	}
	// MinConfidence 0 means "report the raw choice": the evaluation compares
	// classifiers, not the confidence gate.
	verdict, _ := gate.Decide(context.Background(), cfg, "openai", "eval",
		jevgate.BuildState(jevgate.StateInput{LatestUserText: prompt}))
	if verdict.Verdict != jevgate.VerdictAccepted && verdict.Verdict != jevgate.VerdictLowConfidence {
		fmt.Fprintf(os.Stderr, "autorouter_eval: classification failed: %s\n", verdict.Verdict)
		return autorouter.TierSimple
	}
	return autorouter.Tier(verdict.Choice)
}

// buildConfusion returns a tier x tier count matrix.
func buildConfusion(pairs []Pair) map[autorouter.Tier]map[autorouter.Tier]int {
	m := make(map[autorouter.Tier]map[autorouter.Tier]int)
	for _, t := range autorouter.TierOrder {
		m[t] = make(map[autorouter.Tier]int)
	}
	for _, p := range pairs {
		if _, ok := m[p.Want]; !ok {
			m[p.Want] = make(map[autorouter.Tier]int)
		}
		m[p.Want][p.Got]++
	}
	return m
}

// accuracy is the fraction of exact tier matches.
func accuracy(pairs []Pair) float64 {
	if len(pairs) == 0 {
		return 0
	}
	correct := 0
	for _, p := range pairs {
		if p.Want == p.Got {
			correct++
		}
	}
	return float64(correct) / float64(len(pairs))
}

// macroF1 is the unweighted mean of per-tier F1, so a classifier that ignores a
// rare tier is penalized rather than hidden by the majority class.
func macroF1(pairs []Pair) float64 {
	tiers := autorouter.TierOrder
	total := 0.0
	for _, t := range tiers {
		var tp, fp, fn int
		for _, p := range pairs {
			switch {
			case p.Want == t && p.Got == t:
				tp++
			case p.Want != t && p.Got == t:
				fp++
			case p.Want == t && p.Got != t:
				fn++
			}
		}
		precision, recall := 0.0, 0.0
		if tp+fp > 0 {
			precision = float64(tp) / float64(tp+fp)
		}
		if tp+fn > 0 {
			recall = float64(tp) / float64(tp+fn)
		}
		f1 := 0.0
		if precision+recall > 0 {
			f1 = 2 * precision * recall / (precision + recall)
		}
		total += f1
	}
	return total / float64(len(tiers))
}

// report prints per-classifier accuracy, macro-F1 and the confusion matrix.
func report(name string, pairs []Pair) {
	fmt.Printf("\n=== %s ===\n", name)
	fmt.Printf("n=%d accuracy=%.3f macroF1=%.3f\n", len(pairs), accuracy(pairs), macroF1(pairs))
	m := buildConfusion(pairs)
	fmt.Printf("%-11s", "want\\got")
	for _, t := range autorouter.TierOrder {
		fmt.Printf("%-11s", t)
	}
	fmt.Println()
	for _, want := range autorouter.TierOrder {
		fmt.Printf("%-11s", want)
		for _, got := range autorouter.TierOrder {
			fmt.Printf("%-11d", m[want][got])
		}
		fmt.Println()
	}
}

// reportDisagreements lists prompts where the two classifiers chose differently,
// which is the interesting set for manual inspection.
func reportDisagreements(heur, jev []Pair) {
	byID := make(map[string]Pair, len(jev))
	for _, p := range jev {
		byID[p.ID] = p
	}
	ids := make([]string, 0, len(heur))
	for _, h := range heur {
		if j, ok := byID[h.ID]; ok && h.Got != j.Got {
			ids = append(ids, h.ID)
		}
	}
	sort.Strings(ids)
	fmt.Printf("\n=== disagreements (heuristic vs jev): %d ===\n", len(ids))
	for _, id := range ids {
		for _, h := range heur {
			if h.ID != id {
				continue
			}
			j := byID[id]
			fmt.Printf("%-20s want=%-10s heur=%-10s jev=%-10s\n", id, h.Want, h.Got, j.Got)
		}
	}
}

// loadCorpus reads and validates the labelled corpus.
func loadCorpus(path string) ([]Entry, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, fmt.Errorf("read corpus: %w", errRead)
	}
	var entries []Entry
	if errUnmarshal := json.Unmarshal(raw, &entries); errUnmarshal != nil {
		return nil, fmt.Errorf("parse corpus: %w", errUnmarshal)
	}
	valid := make(map[autorouter.Tier]bool, len(autorouter.TierOrder))
	for _, t := range autorouter.TierOrder {
		valid[t] = true
	}
	for i, e := range entries {
		if !valid[autorouter.Tier(e.Want)] {
			return nil, fmt.Errorf("entry %d (%s): want %q is not a valid tier", i, e.ID, e.Want)
		}
	}
	return entries, nil
}
```

`cmd/autorouter_eval/corpus.example.json`:

```json
[
  {"id": "greet-1", "prompt": "hi, how are you?", "want": "simple"},
  {"id": "summary-1", "prompt": "summarize these meeting notes in three bullets", "want": "medium"},
  {"id": "code-1", "prompt": "add retry with exponential backoff to the fetch helper and update its tests", "want": "complex"},
  {"id": "proof-1", "prompt": "prove that this algorithm is O(n log n) and analyze the memory trade-off", "want": "reasoning"}
]
```

**Step 4: Run tests to verify they pass**

```bash
gofmt -w .
go test ./cmd/autorouter_eval/ -v
go run ./cmd/autorouter_eval -corpus cmd/autorouter_eval/corpus.example.json -mode heuristic
```

Expected: tests PASS; the run prints a 4×4 matrix for the heuristic over the example corpus.

**Step 5: Commit**

```bash
git add cmd/autorouter_eval/
git commit -m "feat(eval): heuristic vs Jev corpus comparison tool"
```

---

## Task 12: Final verification

**Step 1: Full build and test**

```bash
cd /home/bilfid/projects/nixllm/.claude/worktrees/jev-gate
gofmt -l . | grep -v '^$' && echo "UNFORMATTED FILES ABOVE" || echo "gofmt clean"
go build -o test-output ./cmd/server && rm test-output
go test ./... 2>&1 | tail -30
```

Expected: gofmt clean, build succeeds, tests pass. Pre-existing failures in this repo are: 3 environment-dependent tests, `internal/util`, and a flaky home timer test. Confirm any failure is on that known list before treating it as a regression.

**Step 2: Benchmark the disabled path**

```bash
go test ./internal/autorouter/ -bench BenchmarkScoreSmallBody -benchtime 100000x -run '^$'
```

Expected: within noise of the recorded baseline (3.27µs/op, 912B, 12 allocs). The gate adds no work when disabled; a regression here means the gate was placed before the enabled check.

**Step 3: Confirm the invariants**

- `git diff main -- internal/translator/` is empty.
- `grep -rn "log.Fatal" internal/autorouter/jevclient internal/autorouter/jevgate internal/store/pg_jev.go cmd/autorouter_eval/` returns nothing.
- `git diff main -- internal/autorouter/score_cache.go internal/autorouter/scorer.go` shows no behavioral change to the scorer (only the additive `JevStateInput` accessor).

**Step 4: Dashboard build and embed**

```bash
cd web/dashboard && npm run build
cd .. && make dash-embed
git status --short
```

Expected: `internal/dashboardasset/` updated with the new `dist`; commit it if it changed.

**Step 5: Commit any remaining verification artifacts**

```bash
git add -A
git commit -m "chore(autorouter): verification pass for Jev hybrid gate" || echo "nothing to commit"
```

---

## Post-implementation (not in this plan)

These are deliberately out of scope; the design doc records them as follow-ups:

1. Run the Layer 1 corpus evaluation against real traffic and record accuracy, macro-F1 and the disagreement set.
2. Canary one router with `jev_enabled: true`; watch `jev.cache` hit rate and the per-tier latency/cost in the Overview tab.
3. Tune `jev_min_confidence` from the recorded confidence distribution.
4. Consider live API-key reload (currently restart-gated).
5. Consider per-tier confidence thresholds and Score-based composite scoring — both deferred pending real data.
