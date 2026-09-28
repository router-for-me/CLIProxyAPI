// Command autorouter_eval compares the Auto Router's lexical heuristic against
// the Jev AI classifier over a labelled corpus.
//
// The heuristic and the classifier answer slightly different questions. The
// heuristic scores surface features (length, structure, code fences, reasoning
// markers); the classifier reasons about what the request actually asks for.
// This tool runs both over the same prompts and reports where each is right, so
// the confidence floor and the per-router opt-in can be set from evidence
// rather than from a guess.
//
// Two modes:
//
//	# Heuristic only — no API key, no network, no cost:
//	go run ./cmd/autorouter_eval -corpus cmd/autorouter_eval/testdata/corpus.jsonl
//
//	# Heuristic vs classifier (calls the TypeSafe API; costs money):
//	JEV_API_KEY=... go run ./cmd/autorouter_eval -corpus <corpus.jsonl> -classify
//
// The corpus is JSONL, one labelled request per line:
//
//	{"id":"p1","tier":"simple","text":"what is 2+2"}
//	{"id":"p2","tier":"complex","request":{...full request object...}}
//
// `text` is shorthand for an OpenAI-style single-user-message request. `format`
// selects the request dialect (default "openai") and is what the scorer and the
// state extractor key off.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevgate"
	log "github.com/sirupsen/logrus"
)

// caseSpec is one corpus line.
type caseSpec struct {
	ID      string          `json:"id"`
	Tier    string          `json:"tier"`
	Format  string          `json:"format"`
	Text    string          `json:"text"`
	Request json.RawMessage `json:"request"`
}

// Tier is the label as an autorouter tier.
func (c caseSpec) tier() autorouter.Tier {
	return autorouter.Tier(strings.ToLower(strings.TrimSpace(c.Tier)))
}

// body renders the request body the scorer will see.
func (c caseSpec) body() ([]byte, error) {
	if len(c.Request) > 0 {
		return c.Request, nil
	}
	if strings.TrimSpace(c.Text) == "" {
		return nil, fmt.Errorf("case %q has neither text nor request", c.ID)
	}
	payload := map[string]any{
		"model":    "router:eval",
		"messages": []map[string]any{{"role": "user", "content": c.Text}},
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("case %q: marshal synthesized request: %w", c.ID, errMarshal)
	}
	return raw, nil
}

// format returns the request dialect for this case.
func (c caseSpec) formatOf() string {
	if f := strings.TrimSpace(c.Format); f != "" {
		return f
	}
	return "openai"
}

// outcome is one row of the report.
type outcome struct {
	id         string
	want       autorouter.Tier
	heuristic  autorouter.Tier
	classifier autorouter.Tier
	confidence float64
	accepted   bool
	verdict    string
	latency    time.Duration
	inTokens   int
	err        string
}

// confusion counts how often an exact tier was predicted.
type confusion struct {
	n     int
	exact int
	// over is "predicted a harder tier than needed", i.e. wasted spend; under is
	// the opposite, i.e. a request that may have been under-served.
	over, under int
	// distance is the sum of |predicted - labelled| tier steps.
	distance int
}

func (c *confusion) add(want, got autorouter.Tier) {
	c.n++
	d := tierDistance(want, got)
	c.distance += d
	switch {
	case d == 0:
		c.exact++
	case d > 0:
		// got is harder than want
		c.over++
	default:
		c.under++
	}
}

func (c confusion) accuracy() float64 {
	if c.n == 0 {
		return 0
	}
	return float64(c.exact) / float64(c.n)
}

func (c confusion) meanDistance() float64 {
	if c.n == 0 {
		return 0
	}
	return float64(c.distance) / float64(c.n)
}

// tierDistance is the signed number of tier steps from want to got: positive
// means got is the harder (more expensive) tier.
func tierDistance(want, got autorouter.Tier) int {
	wi, okWant := tierIndex(want)
	gi, okGot := tierIndex(got)
	if !okWant || !okGot {
		return 0
	}
	return gi - wi
}

func tierIndex(t autorouter.Tier) (int, bool) {
	for i, candidate := range autorouter.TierOrder {
		if candidate == t {
			return i, true
		}
	}
	return 0, false
}

func main() {
	corpusPath := flag.String("corpus", "", "path to the JSONL corpus (required)")
	doClassify := flag.Bool("classify", false, "also run the Jev classifier (requires JEV_API_KEY; calls the API and costs money)")
	model := flag.String("model", "", "classifier model (default: store default)")
	baseURL := flag.String("base-url", "", "classifier API root (default: the public TypeSafe endpoint)")
	minConf := flag.Float64("min-confidence", 0.5, "confidence floor for accepting a classifier verdict")
	timeout := flag.Duration("timeout", 5*time.Second, "per-call classifier timeout (eval only; the request path uses its own bound)")
	concurrency := flag.Int("concurrency", 4, "parallel classifier calls; keep low to stay under API rate limits")
	asJSON := flag.Bool("json", false, "emit the report as JSON instead of text")
	verbose := flag.Bool("v", false, "print one line per case")
	flag.Parse()

	if strings.TrimSpace(*corpusPath) == "" {
		log.Error("autorouter_eval: -corpus is required")
		os.Exit(2)
	}
	cases, errLoad := loadCorpus(*corpusPath)
	if errLoad != nil {
		log.WithError(errLoad).Error("autorouter_eval: load corpus")
		os.Exit(1)
	}
	if len(cases) == 0 {
		log.Error("autorouter_eval: corpus is empty")
		os.Exit(2)
	}

	classificationModel := strings.TrimSpace(*model)
	if classificationModel == "" {
		classificationModel = jevclient.DefaultModel
	}

	var caller jevgate.Caller
	if *doClassify {
		key := strings.TrimSpace(os.Getenv("JEV_API_KEY"))
		if key == "" {
			log.Error("autorouter_eval: -classify requires JEV_API_KEY")
			os.Exit(2)
		}
		caller = jevclient.New(*baseURL, key, nil)
	}

	outcomes := run(cases, caller, classificationModel, *minConf, *timeout, *concurrency)

	heuristic, gated, classifier := summarize(outcomes)
	report := buildReport(*corpusPath, cases, outcomes, heuristic, gated, classifier, *doClassify, classificationModel, *minConf)

	if *verbose {
		printRows(outcomes)
	}
	if *asJSON {
		raw, errMarshal := json.MarshalIndent(report, "", "  ")
		if errMarshal != nil {
			log.WithError(errMarshal).Error("autorouter_eval: marshal report")
			os.Exit(1)
		}
		fmt.Println(string(raw))
		return
	}
	printReport(report)
}

// loadCorpus reads the JSONL corpus, skipping blank lines and comments.
func loadCorpus(path string) ([]caseSpec, error) {
	f, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, fmt.Errorf("open %s: %w", path, errOpen)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.WithError(errClose).Warn("autorouter_eval: close corpus")
		}
	}()

	var cases []caseSpec
	scanner := bufio.NewScanner(f)
	// Corpus lines can hold full request bodies, so raise the line cap well past
	// bufio's 64k default.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		var spec caseSpec
		if errDecode := json.Unmarshal([]byte(text), &spec); errDecode != nil {
			return nil, fmt.Errorf("line %d: %w", line, errDecode)
		}
		if _, okTier := tierIndex(spec.tier()); !okTier {
			return nil, fmt.Errorf("line %d: tier %q is not one of simple/medium/complex/reasoning", line, spec.Tier)
		}
		if spec.ID == "" {
			spec.ID = fmt.Sprintf("line-%d", line)
		}
		cases = append(cases, spec)
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, fmt.Errorf("read %s: %w", path, errScan)
	}
	return cases, nil
}

// run scores every case, consulting the classifier when a caller is supplied.
//
// The gate is deliberately used for the classifier leg rather than calling the
// client directly: the gate is what production runs, so its fail-open
// behaviour, confidence derivation, and caching are all part of what is being
// measured.
func run(cases []caseSpec, caller jevgate.Caller, model string, minConf float64, timeout time.Duration, concurrency int) []outcome {
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]outcome, len(cases))
	sem := make(chan struct{}, concurrency)
	done := make(chan int, len(cases))

	for i, spec := range cases {
		go func(i int, spec caseSpec) {
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = evaluate(cases[i], caller, model, minConf, timeout)
			done <- i
		}(i, spec)
	}
	for range cases {
		<-done
	}
	return results
}

// evaluate runs the heuristic and, when configured, the classifier for one case.
func evaluate(spec caseSpec, caller jevgate.Caller, model string, minConf float64, timeout time.Duration) outcome {
	out := outcome{id: spec.ID, want: spec.tier()}

	body, errBody := spec.body()
	if errBody != nil {
		out.err = errBody.Error()
		return out
	}
	format := spec.formatOf()

	// The heuristic leg replays exactly what the request path scores.
	scored := autorouter.ScoreWithProfileCompiled(body, format, nil)
	out.heuristic = scored.EffectiveTier

	if caller == nil {
		return out
	}

	// One gate per case would defeat the cache; a shared gate would leak state
	// across runs. Per-case is correct here because the eval is measuring
	// accuracy, not cache behaviour, and a cold cache is the honest baseline.
	gate := jevgate.NewGate(caller, jevgate.NewCache(0), jevgate.NewBreaker(0))
	cfg := jevgate.Config{
		GlobalEnabled: true,
		APIKeySet:     true,
		RouterEnabled: true,
		Model:         model,
		MinConfidence: minConf,
		Timeout:       timeout,
	}
	ext := autorouter.ExtractJevStateInput(body, format)
	state := jevgate.BuildState(jevgate.StateInput{
		LatestUserText:      ext.LatestUserText,
		MessageCount:        ext.MessageCount,
		HistoryWordEstimate: ext.HistoryWordEstimate,
		HasTools:            ext.HasTools,
		HasCodeFence:        ext.HasCodeFence,
		HasImages:           ext.HasImages,
	})

	start := time.Now()
	verdict, accepted := gate.Decide(context.Background(), cfg, format, "eval", state)
	out.latency = time.Since(start)
	out.verdict = verdict.Verdict
	out.confidence = verdict.Confidence
	out.accepted = accepted
	out.inTokens = verdict.InputTokens
	if accepted {
		if tier, okTier := tierFromString(verdict.Choice); okTier {
			out.classifier = tier
		}
	}
	if verdict.Verdict == jevgate.VerdictError || verdict.Verdict == jevgate.VerdictBreakerOpen {
		out.err = verdict.Verdict
	}
	return out
}

func tierFromString(s string) (autorouter.Tier, bool) {
	t := autorouter.Tier(strings.TrimSpace(s))
	_, ok := tierIndex(t)
	return t, ok
}

// summarize folds the outcomes into the three confusion matrices the report
// compares: the heuristic alone, the classifier alone, and the hybrid (the
// classifier where it was accepted, the heuristic elsewhere) — the hybrid being
// what production actually routes on.
func summarize(outcomes []outcome) (heuristic, hybrid, classifier confusion) {
	for _, out := range outcomes {
		if out.err != "" || out.want == "" {
			continue
		}
		heuristic.add(out.want, out.heuristic)

		effective := out.heuristic
		if out.accepted && out.classifier != "" {
			effective = out.classifier
		}
		hybrid.add(out.want, effective)

		// The classifier leg only counts calls that actually produced a verdict,
		// so an API outage does not read as classifier inaccuracy.
		if out.classifier != "" {
			classifier.add(out.want, out.classifier)
		}
	}
	return heuristic, hybrid, classifier
}

type report struct {
	Corpus        string          `json:"corpus"`
	Cases         int             `json:"cases"`
	Classified    bool            `json:"classify"`
	Model         string          `json:"classifier_model,omitempty"`
	MinConfidence float64         `json:"min_confidence"`
	Heuristic     reportSection   `json:"heuristic"`
	Hybrid        *reportSection  `json:"hybrid,omitempty"`
	Classifier    *reportSection  `json:"classifier,omitempty"`
	Agreement     *reportAgree    `json:"agreement,omitempty"`
	PerTier       []tierBreakdown `json:"per_tier"`
}

type reportSection struct {
	Cases    int     `json:"cases"`
	Exact    int     `json:"exact"`
	Accuracy float64 `json:"accuracy"`
	// MeanTierError is signed: positive means the predicted tier was harder
	// (more expensive) than the label, negative means cheaper.
	MeanTierError float64 `json:"mean_tier_error"`
	OverRouted    int     `json:"over_routed"`
	UnderRouted   int     `json:"under_routed"`
	PctOverRouted float64 `json:"pct_over_routed"`
}

type reportAgree struct {
	Comparable      int     `json:"comparable"`
	SameTier        int     `json:"same_tier"`
	AgreementRate   float64 `json:"agreement_rate"`
	HeuristicBetter int     `json:"heuristic_closer"`
	ClassifBetter   int     `json:"classifier_closer"`
	HybridBetter    int     `json:"hybrid_closer_than_heuristic"`
	HybridWorse     int     `json:"hybrid_worse_than_heuristic"`
	Accepted        int     `json:"verdicts_accepted"`
	LowConfidence   int     `json:"verdicts_low_confidence"`
	Errors          int     `json:"verdicts_error"`
	MeanLatencyMs   float64 `json:"mean_latency_ms"`
	TotalInTokens   int     `json:"total_input_tokens"`
}

type tierBreakdown struct {
	Tier      string  `json:"tier"`
	Cases     int     `json:"cases"`
	Heuristic float64 `json:"heuristic_accuracy"`
	Hybrid    float64 `json:"hybrid_accuracy"`
	Classif   float64 `json:"classifier_accuracy"`
}

func sectionOf(c confusion) reportSection {
	s := reportSection{
		Cases: c.n, Exact: c.exact, Accuracy: c.accuracy(),
		MeanTierError: c.meanDistance(), OverRouted: c.over, UnderRouted: c.under,
	}
	if c.n > 0 {
		s.PctOverRouted = float64(c.over) / float64(c.n)
	}
	return s
}

func buildReport(corpus string, cases []caseSpec, outcomes []outcome, heuristic, hybrid, classifier confusion, classified bool, model string, minConf float64) report {
	rep := report{
		Corpus: corpus, Cases: len(cases), Classified: classified, MinConfidence: minConf,
		Heuristic: sectionOf(heuristic), PerTier: perTier(cases, outcomes),
	}
	if classified {
		rep.Model = model
		h := sectionOf(hybrid)
		c := sectionOf(classifier)
		rep.Hybrid = &h
		rep.Classifier = &c
		rep.Agreement = agreement(outcomes)
	}
	return rep
}

func perTier(cases []caseSpec, outcomes []outcome) []tierBreakdown {
	type acc struct{ h, hybrid, c confusion }
	byTier := map[autorouter.Tier]*acc{}
	for _, out := range outcomes {
		if out.err != "" || out.want == "" {
			continue
		}
		a := byTier[out.want]
		if a == nil {
			a = &acc{}
			byTier[out.want] = a
		}
		a.h.add(out.want, out.heuristic)
		effective := out.heuristic
		if out.accepted && out.classifier != "" {
			effective = out.classifier
			a.c.add(out.want, out.classifier)
		}
		a.hybrid.add(out.want, effective)
	}

	out := make([]tierBreakdown, 0, len(autorouter.TierOrder))
	for _, tier := range autorouter.TierOrder {
		a := byTier[tier]
		if a == nil {
			continue
		}
		out = append(out, tierBreakdown{
			Tier: string(tier), Cases: a.h.n,
			Heuristic: a.h.accuracy(), Hybrid: a.hybrid.accuracy(), Classif: a.c.accuracy(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ii, _ := tierIndex(autorouter.Tier(out[i].Tier))
		jj, _ := tierIndex(autorouter.Tier(out[j].Tier))
		return ii < jj
	})
	return out
}

func agreement(outcomes []outcome) *reportAgree {
	ag := &reportAgree{}
	var latencyTotal time.Duration
	for _, out := range outcomes {
		switch out.verdict {
		case jevgate.VerdictAccepted:
			ag.Accepted++
		case jevgate.VerdictLowConfidence:
			ag.LowConfidence++
		case jevgate.VerdictError, jevgate.VerdictBreakerOpen:
			ag.Errors++
		}
		if out.err != "" || out.want == "" {
			continue
		}
		ag.Comparable++
		latencyTotal += out.latency
		ag.TotalInTokens += out.inTokens
		if out.classifier == out.heuristic {
			ag.SameTier++
		}

		hd := abs(tierDistance(out.want, out.heuristic))
		effective := out.heuristic
		if out.accepted && out.classifier != "" {
			effective = out.classifier
		}
		ed := abs(tierDistance(out.want, effective))

		switch {
		case out.classifier == "":
			// No classifier verdict for this case; only the heuristic counts.
		default:
			cd := abs(tierDistance(out.want, out.classifier))
			if cd < hd {
				ag.ClassifBetter++
			} else if cd > hd {
				ag.HeuristicBetter++
			}
		}
		if ed < hd {
			ag.HybridBetter++
		} else if ed > hd {
			ag.HybridWorse++
		}
	}
	if ag.Comparable > 0 {
		ag.AgreementRate = float64(ag.SameTier) / float64(ag.Comparable)
		ag.MeanLatencyMs = float64(latencyTotal.Milliseconds()) / float64(ag.Comparable)
	}
	return ag
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func printRows(outcomes []outcome) {
	for _, out := range outcomes {
		fmt.Printf("%-16s want=%-9s heuristic=%-9s", out.id, out.want, out.heuristic)
		if out.verdict != "" {
			fmt.Printf(" classifier=%-9s conf=%.2f verdict=%-22s %dms",
				orDash(string(out.classifier)), out.confidence, out.verdict, out.latency.Milliseconds())
		}
		if out.err != "" {
			fmt.Printf(" err=%s", out.err)
		}
		fmt.Println()
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printReport(rep report) {
	fmt.Printf("corpus: %s (%d cases)\n\n", rep.Corpus, rep.Cases)
	fmt.Println("HEURISTIC (always available, no cost)")
	printSection(rep.Heuristic)

	if rep.Classified {
		fmt.Printf("\nCLASSIFIER alone (%s, calls that returned a verdict)\n", rep.Model)
		if rep.Classifier != nil {
			printSection(*rep.Classifier)
		}
		fmt.Printf("\nHYBRID (what the request path actually routes on)\n")
		if rep.Hybrid != nil {
			printSection(*rep.Hybrid)
		}
		if rep.Agreement != nil {
			a := rep.Agreement
			fmt.Printf("\nAGREEMENT\n")
			fmt.Printf("  heuristic == classifier      : %d/%d (%.1f%%)\n", a.SameTier, a.Comparable, a.AgreementRate*100)
			fmt.Printf("  classifier strictly closer   : %d\n", a.ClassifBetter)
			fmt.Printf("  heuristic strictly closer    : %d\n", a.HeuristicBetter)
			fmt.Printf("  hybrid better / worse vs heur : %d / %d\n", a.HybridBetter, a.HybridWorse)
			fmt.Printf("  verdicts accepted/lowconf/err : %d / %d / %d\n", a.Accepted, a.LowConfidence, a.Errors)
			fmt.Printf("  mean classifier latency      : %.1f ms\n", a.MeanLatencyMs)
			fmt.Printf("  classifier input tokens      : %d\n", a.TotalInTokens)
		}
	}

	fmt.Printf("\nPER TIER\n")
	if rep.Classified {
		fmt.Printf("  %-10s %6s %10s %10s %11s\n", "tier", "cases", "heuristic", "hybrid", "classifier")
		for _, t := range rep.PerTier {
			fmt.Printf("  %-10s %6d %9.1f%% %9.1f%% %10.1f%%\n",
				t.Tier, t.Cases, t.Heuristic*100, t.Hybrid*100, t.Classif*100)
		}
		return
	}
	// Heuristic-only runs have no classifier data; showing a 0% column would
	// read as "the classifier got everything wrong" rather than "not run".
	fmt.Printf("  %-10s %6s %10s\n", "tier", "cases", "heuristic")
	for _, t := range rep.PerTier {
		fmt.Printf("  %-10s %6d %9.1f%%\n", t.Tier, t.Cases, t.Heuristic*100)
	}
}

func printSection(s reportSection) {
	fmt.Printf("  exact tier       : %d/%d (%.1f%%)\n", s.Exact, s.Cases, s.Accuracy*100)
	fmt.Printf("  mean tier error  : %+.2f steps (negative = routed cheaper than the label)\n", s.MeanTierError)
	fmt.Printf("  over-routed      : %d (%.1f%%) — paid for a harder model than needed\n", s.OverRouted, s.PctOverRouted*100)
	fmt.Printf("  under-routed     : %d — may have been under-served\n", s.UnderRouted)
}
