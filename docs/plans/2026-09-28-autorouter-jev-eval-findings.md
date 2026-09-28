# Auto Router: Jev AI vs the Lexical Heuristic — measured findings

**Date:** 2026-09-28
**Branch:** `feat/autorouter-jev-hybrid-gate` (`56972469..76cdf0ab` off `10f1fb33`)
**Tool:** `cmd/autorouter_eval` with `-classify` against the live TypeSafe API (`jev-1.13.0`)
**Corpus:** 60 hand-labelled cases (21 from `cmd/autorouter_eval/testdata/corpus.jsonl`, 39 added for this run, 15 per tier)

This answers the question the feature was built on: does the Jev AI classifier
route better than the lexical heuristic it was layered on top of?

## Headline

| | exact tier | mean tier error | over-routed | under-routed |
|---|---|---|---|---|
| **Heuristic** (baseline, no cost) | 25/60 — **41.7%** | −0.73 | 6 | **29** |
| **Classifier alone** | 51/60 — **85.0%** | +0.15 | 9 | **0** |
| **Hybrid** (what the request path routes on) | 50/60 — 83.3% at floor 0.5<br>51/60 — **85.0%** at floor ≤0.35 | +0.15 | 9 | 0 |

Per tier (heuristic → hybrid → classifier):

```
simple      73.3%  →   86.7%  →   86.7%
medium      68.8%  →  100.0%  →  100.0%
complex      7.1%  →   50.0%  →   50.0%
reasoning   13.3%  →  100.0%  →  100.0%
```

The classifier agrees with the heuristic on only 38% of cases. Where they
differ, the classifier is strictly closer to the label 32 times and the
heuristic 2 times.

## The shape of the errors matters more than the rate

**Every one of the classifier's 9 errors is over-routing** — it picks one tier
harder than the label, never softer:

| direction | count | confidence | examples |
|---|---|---|---|
| `complex` → `reasoning` | 7 | 0.93–0.99 | `api-redesign`, `data-pipeline`, `feature-plan`, `legacy-migrate`, `migration-plan`, `perf-investigate`, `security-audit` |
| `simple` → `medium` | 2 | 0.46, 0.59 | `diff-two`, `regex-simple` |
| under-routing | **0** | — | — |

This asymmetry is the actual win. Under-routing is the failure the heuristic
commits 29 times out of 60 — it marks `reasoning`-labelled work as `medium` or
`simple` and hands a hard request to a weak model. Over-routing costs money;
under-routing costs capability. The classifier trades the second failure mode
for the first, and only 7 of its 9 over-routes are on the `complex`/`reasoning`
boundary — the boundary where the corpus labels are themselves the most
contestable.

Note also that the classifier is most *confident* on exactly the over-routes it
gets "wrong": 0.99 on six of them. If those labels were generous rather than the
classifier being wrong, the real accuracy is higher than 85%.

## The confidence floor: 0.5 was measurably harmful

The shipped default of 0.5 rejects verdicts below it and hands the request back
to the heuristic. That is the wrong default, because classifier confidence is
**weakly related to correctness here**: the cases it answers at 0.32–0.43 are
disproportionately the ones where the heuristic is *wrong*.

The two cases the 0.5 floor rejected:

| case | label | heuristic | classifier | confidence |
|---|---|---|---|---|
| `debug-with-context` | complex | **simple** (wrong, −2 tiers) | **complex** (right) | 0.41–0.43 |
| `concurrency-bug` | complex | **simple** (wrong, −2 tiers) | **complex** (right) | 0.32–0.35 |

In both, the floor discarded a correct answer and kept an incorrect one.

Floor sweep (derived from the per-case confidences):

```
floor   exact        accepted   over   under
0.00    51/60 85.0%     60        9      0
0.30    51/60 85.0%     60        9      0
0.35    51/60 85.0%     60        9      0
0.40    50/60 83.3%     59        9      1
0.42    49/60 81.7%     58        9      2
0.50    50/60 83.3%     55        7      3     <- shipped default
0.70    50/60 83.3%     53        7      3
```

Everything at or below 0.35 is identical; accuracy drops as soon as the floor
passes 0.40. **The default was changed from 0.5 to 0.35 as a result.**

### Why not lower still

Because the floor is not the binding constraint below 0.35 — nothing is
rejected there, so lowering it further changes nothing. And it cannot be
tuned finely, because **classifier confidence is not deterministic**:

- **Tier choice was stable in 60/60 cases across two full runs.**
- **Confidence drifted by 0.01–0.04 in 22/60 cases.**

`concurrency-bug` produced 0.32 and 0.35 on successive runs; `debug-with-context`
produced 0.41 and 0.43. Any floor in roughly the 0.41–0.47 band would therefore
admit or reject the same case depending on the run. 0.35 sits in the wide stable
region below that band; it is a measured choice, not a fitted one.

## Latency and cost

120 classifier calls, measured end to end:

```
min 258ms   p50 283ms   p95 441ms   max 627ms   mean 300ms
```

This is the tax the gate adds to every classified request, synchronously. It is
2–3× the ~100 ms the TypeSafe documentation suggests.

**The shipped 400 ms default timeout is too tight**: 7 of 120 calls exceeded it,
and a timeout means a silent fallback to the heuristic — the request is routed
by the weaker scorer with nothing surfaced to the caller beyond a log line.

*This was raised with the operator, who chose to keep 400 ms.* If you are
tuning this deployment: raising it to ~1 s would cover p95 at the cost of a
slower tail on classifier failures.

Cost: ~521 input tokens per call ⇒ **≈$0.022 per 1000 routed requests** at
$0.042/Mtok, output free.

## Caveats — read before acting on these numbers

1. **The 60 labels are one operator's judgement, not benchmark ground truth.**
   They were written by the same person who designed the tier criteria, and the
   `complex`/`reasoning` boundary is precisely where the classifier most often
   disagrees with them. Treat the rates as indicative and the *directions* as
   the reliable signal.
2. **The corpus is small and synthetic.** 15 cases per tier cannot support
   fine-grained threshold tuning; the floor choice above deliberately targets
   the coarse structure (reject-nothing vs reject-some) rather than a
   fitted optimum.
3. **Failures were excluded from every leg**, so these numbers describe routing
   quality when the classifier is reachable, not availability. Fail-open
   behaviour is covered by unit tests and was observed live during the smoke
   test.
4. **One operator, one domain.** Most cases are software-engineering requests.
   Routing quality on other workloads is unmeasured.

## Reproducing

```bash
export JEV_API_KEY=...
go run ./cmd/autorouter_eval -corpus cmd/autorouter_eval/testdata/corpus.jsonl \
  -classify -concurrency 3 -min-confidence 0.35 -v
```

`-concurrency` is kept low deliberately: the endpoint is rate-limited and a 429
is indistinguishable from an outage in the report.
