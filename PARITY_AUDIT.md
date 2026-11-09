# Compression parity audit — handoff brief

You are auditing a Go port of OmniRoute's prompt-compression subsystem. The port
is deliberately partial. Your job is **not** to port anything. It is to decide,
module by module, whether omitting each piece was correct, and to find anything
that was missed silently.

Report format at the end of this file.

## Repos

| role | path | ref |
|---|---|---|
| upstream (source of truth) | `/home/igy/projects/OmniRoute` | `cheap/release/v3.8.53` = `b2a64106c9` |
| port (under audit) | `/home/igy/orca/workspaces/CLIProxyAPI/viperfish` | `lite-tool-cap-and-rtk-command` = `dc0f4e5b`, PR #8 |

Upstream remote is named `cheap` = `https://github.com/cheaper-inference/OmniRoute`.
The local checkout `HEAD` is `f73b76958f`, which is **older** than `cheap/release/v3.8.53`.
Always compare against the ref, never against the working tree:

```bash
cd /home/igy/projects/OmniRoute
git log --oneline HEAD..cheap/release/v3.8.53 -- open-sse/services/compression/
git show cheap/release/v3.8.53:open-sse/services/compression/lite.ts
```

Port: `internal/payloadcompress/`, 12 files, ~5660 lines including tests.
Upstream: `open-sse/services/compression/`, ~27700 lines of TS plus 56 RTK JSON packs.

## What shipped

Engines, 6 of the 12 in `engineCatalog.ts`:

| engine | upstream | port |
|---|---|---|
| `session-dedup` | `engines/session-dedup/` | `dedup.go` |
| `lite` | `lite.ts` | `lite.go` + `lite_body.go` (passes 4/5: redundant-content removal, image placeholder) |
| `rtk` | `engines/rtk/` (4194 lines, 20 files) | `rtk.go` + `rtkfilters.go` + 56 JSON packs |
| `headroom` | `engines/headroom/` | `headroom.go` + embedded arrays |
| `relevance` | `engines/relevance/` | `relevance.go` (lexical overlap scorer) |
| `caveman` | `caveman.ts` + `cavemanRules.ts` | `caveman.go` + `packs.go` |

Framework and cross-cutting pieces that did ship:

- `cacheAwareConfig.ts`, `preserveSystemPromptMode.ts` -> `preserve-system-prompt` modes in `prefixfreeze.go`
- `prefixFreeze.ts` -> `prefixfreeze.go`
- `outputMode.ts` + `outputStyles/` -> `outputstyle.go`
- `bodyAdapter.ts` equivalent -> `segments.go` (scanner). Different design, same job.
- `liveZone.ts` + `chatCore/liveZoneCompression.ts` -> `livezone.go`. See "Judgement calls".
- `engines/headroom/` embedded arrays -> `compactEmbeddedArrays` in `headroom.go`.
- `engines/mcpAccessibility/` -> `mcp_accessibility.go` (noise-line drop,
  repeated-line collapse, oversized-text clamp).
- `quantumLock/` -> `quantum_lock.go` (span masking, `quantum-lock` config knob).
- per-engine telemetry (`engineBreakdown.ts`) -> `Stats.EngineBytesIn`/`EngineBytesOut`.

## Not ported — verify each verdict

Mark each ✅ (correct to omit), ⚠️ (omission costs something), or ❌ (real gap).

Engines:

| upstream | lines | claim to check |
|---|---|---|
| `aggressive.ts` | 233 | another truncation ladder. Overlaps `lite`. Omitted. |
| `ultra.ts` + `ultraHeuristic.ts` | 532 | Omitted. |
| `engines/llmlingua/` | ~3325 | ONNX SLM. Non-deterministic, breaks append-stability. Omitted. |
| `engines/ccr/` | ~1252 | replaces text with `[CCR retrieve hash=..]` needing an MCP retrieve tool. Unrecoverable here. Omitted. |
| `engines/codex-responses/` | 335 | Omitted. |
| `omniglyphAdapter.ts`, `omniglyphSingleMode.ts` | ~156 | Omitted. |
| `summarizer.ts` | 244 | Omitted. |
| `engines/ionizer/`, `engines/llm/`, `engines/readLifecycle/` | ? | Not inspected in this audit. |

Governance and pipeline layer: the fidelity gate (protected tokens and numeric
integrity), the secret risk gate (mask/restore), exclusions, hard budget and
pipeline guards are ported. Still unported:

`preservation.ts` (253), `progressiveAging.ts` (153),
`strategySelector.ts` (1185), `adaptiveCompression/`, `cachingAware.ts` (145),
`lossyRequestPolicy.ts` (71), `toolResultCompressor.ts` (290),
`pipelineEngineBreaker.ts` (139), `resultMemo.ts` (217),
`planResolution.ts` (166), `resolveCompressionPlan.ts` (28), `deriveDefaultPlan.ts` (56),
`entrypointWrap.ts` (43), `failOpenNotifier.ts` (48), `stackedStepCore.ts` (117),
`stepDetailConfig.ts` (28), `ruleLoader.ts` (307), `validation.ts` (432).

Measurement and ops: `harness/`, `eval/`, `stats.ts` (329),
`diffHelper.ts` (219), `compressionWorker*.ts` (~580), `omniglyphTelemetry.ts`,
`imageTransportPolicy.ts`, `mediaDetect.ts`, `languageDetector.ts`.

Known dead code, do not port: `cachingAware.ts`'s `deterministicOnly` and the
intensity-downgrade path are computed and never consumed anywhere in production.

## Judgement calls already made

1. **`liveZone.ts` ported, off by default (upstream default).** It is a per-session
   memo of the last transform: reuse is proven by one sha256 over the body up to
   the previously compressed length, so a byte-identical prefix skips the engines
   and only the appended tail is compressed. It buys CPU, not tokens: every engine
   here is a pure function of one segment plus prior segments, so recompressing
   yields identical bytes (measured 1.3 ms per 67 KB request, and
   `TestApplyIsAppendStable` pins byte-identical prefixes across turns). Upstream's
   32 MB total-byte budget is not mirrored: the 100-entry cap already bounds memory
   for bodies this size. `cache-prefix` must be `rewrite`, because `protect` leaves
   no transform to reuse. The session id is **not** upstream's
   `x-omniroute-session-id` header: it is this proxy's native identity, resolved by
   `session.SessionIDFromMetadata` with the same precedence affinity routing uses
   (canonical, LCP affinity, execution, derived), so one conversation is one
   session for both. Upstream's own conversation fingerprint is therefore not
   ported, deliberately: two derivations would drift apart.
2. **`cache-prefix: rewrite` is the default, not `protect`.** Upstream never skips
   a message to protect a cache breakpoint. `protect` measured 0% on a realistic
   Claude Code body because the breakpoint sits near the tail.
3. **The scanner emits assistant prose as `kindAssistant`.** Session dedup and
   lite/headroom may rewrite it; RTK skips it. Tool-call arguments, thinking blocks,
   `tools[]`, `tool_choice`, and the `instructions` field are never emitted; tool-call
   arguments are read for the command only and never rewritten.

## Differential harness

Artifacts in `/tmp/cpar/`. If missing, regenerate:

```bash
node /tmp/cpar/mkcorpus2.mjs          # -> corpus2.json, 1 body, 25 msgs
node /tmp/cpar/minify.mjs             # -> min.json, 67343 byte ASCII body
```

`min.json` is the only fair input: upstream measured `JSON.stringify(...).length`
in UTF-16 code units, Go measures bytes. Feeding Go the indented `corpus2.json`
raw produced a 1177-byte larger denominator and a fake 2-point "gap".
**Always compare on `min.json`.**

Upstream side:

```bash
cd /home/igy/projects/OmniRoute
npx tsx /tmp/cpar/upreal.mts     # -> up-real.json, ratios per engine combination
npx tsx /tmp/cpar/litecmp.mts    # -> up-lite.json
npx tsx /tmp/cpar/rtkcmp.mts     # -> up-rtk.json, per-message shrink
```

Go side: write a throwaway `*_test.go` in `internal/payloadcompress/` that decodes
`/tmp/cpar/min.json`, builds a `Config` with the pipeline under test, calls
`Apply(cfg, model, body)` and prints `len(out)` and `stats.SavedPct()`. Delete it
after; do not commit it. `testConfig(...)` in `compress_test.go` is the shortcut for
building a pipeline.

Comparison method: compare **ratios**, not bytes. Compare per-message shrink, not
just totals, or a single diverging message hides inside an equal total.

## Numbers as of `dc0f4e5b`

Input: `min.json`, 67343 bytes, 25 messages, Claude Code shape, one `cache_control`
breakpoint at the tail, tool output is `go test ./...` noise with ANSI.

| pipeline | port | upstream |
|---|---|---|
| `lite` | 69.0% | 67.3% |
| `lite` max-tool-length 500 | 83.0% | not measured |
| `rtk` minimal | 26.0% | 27.0% |
| `rtk` + `caveman` (upstream seed) | 26.0% | 27.0% |
| `caveman` alone | 0% | 0% |
| `cache-prefix: preserve` | 0% (0 segments) | n/a |
| 5-engine pipeline, max-tool-length 500 | 83.0% | not measured |

Cost: 1306 µs, 1110 allocs per 67 KB request.

## Traps that will waste your time

1. **Stale shell output.** In the session that produced this brief, `ls`, `cat` and
   `git status` repeatedly returned old or silently filtered output. Verify every
   file claim through a *different* command than the one that made it:
   `git ls-tree -r HEAD --name-only <path>`, `wc -c`, `jq`. A missing file is
   usually a lying `ls`, not a missing file.
2. **`npx vitest` is broken** in the upstream repo: `Cannot find module
   '@rolldown/binding-wasm32-wasi'`. Use `npx tsx` with direct engine imports.
   `npx tsx` fails on any entry importing `omniglyph` (bad `exports` map in
   `node_modules/omniglyph/package.json`).
3. **`CGO_ENABLED=0` is required** for `go build ./...`; there is no gcc here.
4. **BusyBox userland.** `find -printf`, `cp -v`, `ls --color` are unavailable or
   fail silently.
5. **`level:` is accepted by caveman and RTK.** Caveman keeps lite/full/ultra;
   RTK maps lite/minimal to minimal, full/standard to standard, and ultra/aggressive
   to aggressive, and `options.intensity` wins when both are set. Every other engine
   rejects `level`. Upstream's `engineCatalog` also offers `readOnly`/`safeDefault`
   metadata the port ignores.
6. **`corpus2.json` is synthetic.** Its 27% RTK saving comes largely from repeated
   identical lines and ANSI, which flatters line-dedup. Do not generalise the RTK
   figure without a real captured session.

## Definition of done

Filled in from the branch as implemented:

```
Engine parity
  session-dedup  [x] faithful  [ ] differs: ...
  lite           [x] faithful: passes 4/5 (removeRedundantContent,
                 image-placeholder) available in lite_body.go behind
                 `lite-remove-redundant` / `lite-replace-images` (default off;
                 upstream runs them by default); lossless by
                 default and only truncates tool results when
                 compress-tool-results is set. PreserveToolWhitespace is
                 honored; max-tool-length outside 256..1000000 falls back to 2000.
  rtk            [x] faithful  [ ] differs: ...
  headroom       [x] differs: no reconstructHeadroom() decoder. Embedded arrays
                 in prose are now compacted via compactEmbeddedArrays.
  caveman        [x] faithful  [ ] differs: ...
  relevance      [x] faithful: lexical query-overlap sentence retention.
  mcp-accessibility [x] faithful: noise-line drop / repeat collapse / clamp.
  quantum-lock   [x] faithful: protected-span masking, behind `quantum-lock`.
  telemetry      [x] faithful: per-engine byte counts in `Stats`.

Omitted engines — was the omission correct?
  aggressive     [x] yes — lossy prose rewrite; redundant with the lossless default.
  ultra          [x] yes — most lossy tier; not enabled upstream by default either.
  llmlingua      [x] yes — needs a Python/LLM runtime; cannot live in a Go proxy.
  ccr            [x] yes — replays a prior compressed payload; needs an upstream session store.
  codex-responses[x] yes — provider-specific Responses adapter; generic body compression covers it.
  omniglyph      [x] yes — optional external codec, not an engine in the catalog used here.
  ionizer/llm/readLifecycle [x] yes — catalog entries without compression
                 behavior (read-lifecycle needs a session store); nothing to port.

Deliberately unported governance modules
  strategySelector     — picks an engine from model class; the port keeps explicit config/combos.
  adaptiveCompression  — tunes intensity from runtime token pressure; out of scope, lossless default.
  progressiveAging     — ages/evicts cached prefixes; needs a long-lived cache owner.
  resultMemo           — memoizes engine results; the port has no cross-request result cache.
  worker pool          — schedules large batches concurrently; Go handles bodies synchronously per request.

Silently missing
  Nothing else found: segment kinds, provider adapters (chat, Responses, Gemini
  native contents, developer role) and pipeline stages are all wired.

Biggest single remaining gap, ranked, with the measurement that proves it
  1. headroom emits `# columns=` with no decoder; savings are not measurable
     against upstream's decoder.
  2. RE2 (Go regexp) vs JS regex differences can still shift caveman/RTK rule
     coverage even after lookaround/backreference handling.
```

Known open items for that last section, to confirm or refute:

- `headroom` emits `# columns=` with no decoder, while upstream ships
  `reconstructHeadroom()`. Is the ported format model-readable?
- `min-bytes` crossing mid-session rewrites the whole prefix once (one cache miss).

## Fixes applied (branch DarknessKiller/argonaut)

- lite: lossless default; `PreserveToolWhitespace` honored; `max-tool-length`
  outside 256..1000000 falls back to 2000. Passes 4/5 (`removeRedundantContent`,
  image-placeholder) are ported in `lite_body.go`. Whitespace cleanup matches
  upstream's string-content-only behavior; content-array text is left untouched.
- headroom: arrays inside ```json fences (outside whole-array segments) are
  located and compacted by `compactEmbeddedArrays`.
- mcp-accessibility: noise-line drop, repeated-line collapse, and oversized-text
  clamp ported to `mcp_accessibility.go`.
- quantum-lock: volatile spans in the first system message (UUIDs, timestamps,
  hashes, request/trace ids, keys) become stable placeholders plus a value tail,
  behind the `quantum-lock` knob and only for caching providers.
- relevance: opt-in body engine keeps older user sentences that overlap the
  latest query within a character budget (`relevance.go`).
- per-engine telemetry: `Stats.EngineBytesIn`/`EngineBytesOut` record decoded
  segment bytes in and out, per pipeline step.
- Gemini native `contents[]` and the `developer` role are scanned; assistant
  prose is tagged `kindAssistant` and deduped. Lite/headroom may rewrite
  assistant string content as upstream does; RTK skips assistant messages
  (upstream `applyToAssistantMessages=false`).
- RTK intensity ladder implemented (minimal 1.5x, standard 1.0x, aggressive
  0.5x, scaling an explicit maxLines too) plus the upstream repeat renderer.
- RTK filter selection: a detector's own command patterns win over the pack's
  broader patterns; custom pack files are loaded.
- Runtime config level mapping (`lite`/`minimal` -> minimal, `full`/`standard`
  -> standard); unknown levels are rejected instead of silently dropped.
- Unsupported compression config keys are reported as warnings.
- Inflation guard, pipeline panic recovery (fail open to the original body),
  and a final size invariant.
- Exclusion support: `exclusions` matches model id or provider/model and bypasses
  compression.
- Opt-in fidelity gate, secret risk gate (mask/restore), and hard token budget.
- Zero-engine stacked resolution: a combo with zero enabled engines resolves to
  off.
- session-dedup: first-occurrence rewrite with the upstream sha marker.
