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

Engines, 5 of the 12 in `engineCatalog.ts`:

| engine | upstream | port |
|---|---|---|
| `session-dedup` | `engines/session-dedup/` | `dedup.go` |
| `lite` | `lite.ts` | `lite.go` |
| `rtk` | `engines/rtk/` (4194 lines, 20 files) | `rtk.go` + `rtkfilters.go` + 56 JSON packs |
| `headroom` | `engines/headroom/` | `headroom.go` |
| `caveman` | `caveman.ts` + `cavemanRules.ts` | `caveman.go` + `packs.go` |

Framework and cross-cutting pieces that did ship:

- `cacheAwareConfig.ts`, `preserveSystemPromptMode.ts` -> `preserve-system-prompt` modes in `prefixfreeze.go`
- `prefixFreeze.ts` -> `prefixfreeze.go`
- `outputMode.ts` + `outputStyles/` -> `outputstyle.go`
- `bodyAdapter.ts` equivalent -> `segments.go` (scanner). Different design, same job.
- `liveZone.ts` + `chatCore/liveZoneCompression.ts` -> `livezone.go`. See "Judgement calls".

## Not ported — verify each verdict

Mark each ✅ (correct to omit), ⚠️ (omission costs something), or ❌ (real gap).

Engines:

| upstream | lines | claim to check |
|---|---|---|
| `engines/relevance/` | ~333 | needs a scoring model. Omitted. |
| `aggressive.ts` | 233 | another truncation ladder. Overlaps `lite`. Omitted. |
| `ultra.ts` + `ultraHeuristic.ts` | 532 | Omitted. |
| `engines/llmlingua/` | ~3325 | ONNX SLM. Non-deterministic, breaks append-stability. Omitted. |
| `engines/ccr/` | ~1252 | replaces text with `[CCR retrieve hash=..]` needing an MCP retrieve tool. Unrecoverable here. Omitted. |
| `engines/codex-responses/` | 335 | Omitted. |
| `omniglyphAdapter.ts`, `omniglyphSingleMode.ts` | ~156 | Omitted. |
| `summarizer.ts` | 244 | Omitted. |
| `engines/ionizer/`, `engines/llm/`, `engines/mcpAccessibility/`, `engines/readLifecycle/` | ? | Not inspected in this audit. |

Governance and pipeline layer, none of it ported:

`fidelityGate.ts` (124), `fidelityGateStep.ts` (44), `riskGate/`, `quantumLock/`,
`preservation.ts` (253), `exclusions.ts` (73), `hardBudget.ts` (202),
`progressiveAging.ts` (153), `strategySelector.ts` (1185), `adaptiveCompression/`,
`cachingAware.ts` (145), `lossyRequestPolicy.ts` (71), `toolResultCompressor.ts` (290),
`pipelineGuards.ts` (95), `pipelineEngineBreaker.ts` (139), `resultMemo.ts` (217),
`planResolution.ts` (166), `resolveCompressionPlan.ts` (28), `deriveDefaultPlan.ts` (56),
`entrypointWrap.ts` (43), `failOpenNotifier.ts` (48), `stackedStepCore.ts` (117),
`stepDetailConfig.ts` (28), `ruleLoader.ts` (307), `validation.ts` (432).

Measurement and ops: `harness/`, `eval/`, `stats.ts` (329), `engineBreakdown.ts` (66),
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
   no transform to reuse.
2. **`cache-prefix: rewrite` is the default, not `protect`.** Upstream never skips
   a message to protect a cache breakpoint. `protect` measured 0% on a realistic
   Claude Code body because the breakpoint sits near the tail.
3. **The scanner never emits assistant prose, tool-call arguments, thinking blocks,
   `tools[]`, `tool_choice`, or the `instructions` field.** It reads tool-call
   arguments for the command only, never rewrites them.

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
5. **`level:` is caveman-only** in the port's config. `normalizeEngineLevel` strips
   it for every other engine; rtk intensity is `options.intensity`. Upstream's
   `engineCatalog` also offers `readOnly`/`safeDefault` metadata the port ignores.
6. **`corpus2.json` is synthetic.** Its 27% RTK saving comes largely from repeated
   identical lines and ANSI, which flatters line-dedup. Do not generalise the RTK
   figure without a real captured session.

## Definition of done

Fill this in and hand it back:

```
Engine parity
  session-dedup  [ ] faithful  [ ] differs: ...
  lite           [ ] faithful  [ ] differs: ...
  rtk            [ ] faithful  [ ] differs: ...
  headroom       [ ] faithful  [ ] differs: ...
  caveman        [ ] faithful  [ ] differs: ...

Omitted engines — was the omission correct?
  relevance      [ ] yes [ ] no, because ...
  aggressive     [ ] yes [ ] no, because ...
  ultra          [ ] yes [ ] no, because ...
  llmlingua      [ ] yes [ ] no, because ...
  ccr            [ ] yes [ ] no, because ...
  codex-responses[ ] yes [ ] no, because ...
  omniglyph      [ ] yes [ ] no, because ...
  ionizer/llm/mcpAccessibility/readLifecycle [ ] inspected? ...

Silently missing
  <anything ported-piece-by-piece and dropped in the middle>

Biggest single remaining gap, ranked, with the measurement that proves it
  1.
  2.
  3.
```

Known open items for that last section, to confirm or refute:

- `removeRedundantContent` (upstream `lite` pass 4) has no counterpart: the segment
  architecture sees individual strings, not consecutive same-role messages.
- `headroom` emits `# columns=` with no decoder, while upstream ships
  `reconstructHeadroom()`. Is the ported format model-readable?
- `min-bytes` crossing mid-session rewrites the whole prefix once (one cache miss).
- `headroom` only fires on a whole-JSON-array segment; upstream may fire on
  arrays embedded in prose.
