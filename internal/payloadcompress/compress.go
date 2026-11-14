package payloadcompress

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
	"unsafe"

	log "github.com/sirupsen/logrus"
)

// Stats reports what one Apply call did.
type Stats struct {
	Combo    string
	BytesIn  int
	BytesOut int
	Segments int
	// Saved holds per-engine byte deltas, in pipeline order.
	Saved map[string]int
	// Applied reports that the returned body differs from the input. It stays
	// false in shadow mode, where the caller gets the original body back.
	Applied bool
	// Style reports the injected output style, its target, or why it was skipped.
	Style string
	// StyleBytes is the byte delta of the system prompt injection.
	StyleBytes int
	// EngineBytesIn and EngineBytesOut track the decoded-segment bytes each
	// pipeline step saw and produced, including steps that saved nothing.
	EngineBytesIn  map[string]int
	EngineBytesOut map[string]int
	// Passes counts the lite sub-passes that fired, by name.
	Passes map[string]int
}

// EngineSaved reports the byte delta of one engine, whether or not it saved.
func (s Stats) EngineSaved(name string) int {
	return s.EngineBytesIn[name] - s.EngineBytesOut[name]
}

// tokenEstimate turns a byte count into a token estimate (roughly four bytes
// per token), signed so an engine that grew the payload reports a negative
// saving.
func tokenEstimate(n int) int {
	if n < 0 {
		return -((-n + 3) / 4)
	}
	return (n + 3) / 4
}

// TokensIn estimates the input payload in tokens.
func (s Stats) TokensIn() int { return tokenEstimate(s.BytesIn) }

// TokensOut estimates the output payload in tokens.
func (s Stats) TokensOut() int { return tokenEstimate(s.BytesOut) }

// StyleTokens estimates the style prompt injection in tokens.
func (s Stats) StyleTokens() int { return tokenEstimate(s.StyleBytes) }

// Engines renders the engines that actually saved tokens, as
// "lite=33,rtk=653" in name order. Engines that matched nothing are omitted,
// so the string answers "what ran" rather than "what was configured".
func (s Stats) Engines() string {
	if len(s.Saved) == 0 {
		return ""
	}
	names := make([]string, 0, len(s.Saved))
	for name := range s.Saved {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(tokenEstimate(s.Saved[name])))
	}
	return b.String()
}

// Steps renders every pipeline step that ran as "lite:2560>2000" in token
// estimates, in name order, so a step that ran and saved nothing stays visible
// instead of being omitted the way Engines omits it.
func (s Stats) Steps() string {
	if len(s.EngineBytesIn) == 0 {
		return ""
	}
	names := make([]string, 0, len(s.EngineBytesIn))
	for name := range s.EngineBytesIn {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(tokenEstimate(s.EngineBytesIn[name])))
		b.WriteByte('>')
		b.WriteString(strconv.Itoa(tokenEstimate(s.EngineBytesOut[name])))
	}
	return b.String()
}

// PassesString renders the lite sub-passes that fired as
// "whitespace=12,tool-cap=3" in name order. A pass that ran and changed
// nothing is omitted, so the string answers "what did lite actually do".
func (s Stats) PassesString() string {
	if len(s.Passes) == 0 {
		return ""
	}
	names := make([]string, 0, len(s.Passes))
	for name := range s.Passes {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(s.Passes[name]))
	}
	return b.String()
}

// SavedPct is the percentage of the payload removed, rounded down, in tokens.
func (s Stats) SavedPct() int {
	in, out := s.TokensIn(), s.TokensOut()
	if in <= 0 || out <= 0 {
		return 0
	}
	return 100 * (in - out) / in
}

// payloadEngine is a whole-body transform that runs before the segment scanner.
type payloadEngine interface {
	engine
	applyBody(body []byte) []byte
}

// payloadEngineFactories maps an engine name to its constructor.
var payloadEngineFactories = map[string]func() payloadEngine{
	EngineAggressive:     func() payloadEngine { return newAggressiveEngine() },
	EngineUltra:          func() payloadEngine { return newUltraEngine() },
	EngineCCR:            func() payloadEngine { return newCCREngine() },
	EngineCodexResponses: func() payloadEngine { return newCodexResponsesEngine() },
	EngineIonizer:        func() payloadEngine { return newIonizerEngine() },
	EngineLLM:            func() payloadEngine { return newLLMEngine() },
}

// engine is the identity every pipeline step shares.
type engine interface {
	name() string
}

// textEngine rewrites one decoded segment.
type textEngine interface {
	engine
	compress(text string) (string, bool)
}

// kindAware is implemented by engines that must know whether a segment is human
// prose or captured machine output.
// arrayAware handles text taken from content arrays separately: upstream's
// whitespace pass skips arrays, but tool-result truncation still applies.
type arrayAware interface {
	compressArray(kind segmentKind, text string) (string, bool)
}

type kindAware interface {
	compressKind(kind segmentKind, text string) (string, bool)
}

// commandAware is implemented by engines that rewrite captured tool output
// using the shell command behind it and must skip human prose. The command is
// empty for tool results whose originating call is not in the payload, and an
// engine implementing this must treat that as "decide from the text alone".
type commandAware interface {
	// toolName is empty when the payload carries no call record for the result.
	compressCommand(kind segmentKind, toolName, command, text string) (string, bool)
}

// bodyEngine rewrites the whole payload at once, for transforms that depend on
// repeated content across segments. It returns replacement text by segment index.
// skips marks the segments it must leave alone: the current turn, and everything
// when the payload carries no assistant item.
type bodyEngine interface {
	engine
	rewrite(texts []string, kinds []segmentKind, skips []bool) map[int]string
}

// Apply compresses a prepared request payload. It is a pure function: the same
// bytes always yield the same bytes, which is what keeps provider prompt caches
// warm across retries, fallbacks and websocket continuations.
//
// The returned body aliases the input when nothing was compressed.
func Apply(cfg Config, model string, body []byte) ([]byte, Stats) {
	return applyCore(cfg, model, "", body)
}

// ApplySession compresses one request of a known session. With live-zone off it
// is exactly Apply. With it on, the transform a previous request already
// produced for this session's unchanged prefix is reused and only the appended
// tail is compressed. See livezone.go.
func ApplySession(cfg Config, model, sessionID string, body []byte, provider ...string) ([]byte, Stats) {
	return applyCore(cfg, model, sessionID, body, provider...)
}

func applyCore(cfg Config, model, sessionID string, body []byte, provider ...string) (out []byte, stats Stats) {
	// Compression must never fail a request: a panic anywhere in the pipeline
	// falls back to the untouched body.
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("payloadcompress: pipeline panic, returning original body: %v", r)
			out, stats = body, Stats{BytesIn: len(body)}
		}
	}()
	p := ""
	if len(provider) > 0 {
		p = provider[0]
	}
	return applyCoreInner(cfg, model, p, sessionID, body)
}

func applyCoreInner(cfg Config, model, provider, sessionID string, body []byte) ([]byte, Stats) {
	stats := Stats{BytesIn: len(body)}
	if !cfg.Enabled {
		return body, stats
	}
	if isExcluded(model, cfg.Exclusions, provider) {
		return body, stats
	}
	combo := cfg.resolve(model)
	if cfg.AutoTriggerTokens > 0 && approxTokensBytes(body) >= cfg.AutoTriggerTokens {
		if auto := cfg.comboByName(cfg.AutoTriggerCombo); auto != nil {
			combo = auto
		}
	}
	if combo == nil {
		return body, stats
	}
	stats.Combo = combo.Name
	inBody := body
	inBodyLen := len(body)
	// read-lifecycle is opt-in upstream and runs before the scanner: it edits
	// whole tool-result messages, which the per-segment scanner does not model.
	if comboHasEngine(combo, EngineReadLifecycle) && readLifecycleEnabled(combo) {
		body = applyReadLifecycle(body)
	}
	// Payload engines rewrite the whole body, so they run before the scanner in
	// pipeline order. Savings are attributed to each step by name.
	for _, step := range combo.Pipeline {
		factory, ok := payloadEngineFactories[step.Engine]
		if !ok {
			continue
		}
		before := len(body)
		body = factory().applyBody(body)
		if delta := before - len(body); delta != 0 {
			if stats.Saved == nil {
				stats.Saved = map[string]int{}
			}
			stats.Saved[step.Engine] += delta
		}
	}
	// Resolve the cache-protection decision once per request. The lite body
	// passes and the scanner must agree, and each resolution advances the
	// prefix-freeze counter.
	preserveSystem := cfg.shouldPreserveSystemPrompt(model, body)
	liteBody := comboHasEngine(combo, EngineLite) && liteBodyHasArray(body)
	liteOpts := StepOptions{}
	var beforeChanged bool
	if liteBody {
		liteOpts = liteStepOptions(combo)
		body, beforeChanged = applyLiteBodyBeforePreserve(preserveSystem, body)
	}

	s := &scanner{
		body:          body,
		includeSystem: !preserveSystem,
	}
	if !s.scan() {
		return finishPrepass(cfg, combo, inBody, restoreLiteBody(preserveSystem, liteOpts, model, provider, liteBody, body), stats)
	}

	steps, passes := buildEngines(combo.Pipeline)
	stats.Passes = passes
	if beforeChanged && passes != nil {
		passes["dedup-system"]++
	}
	if len(steps) == 0 {
		return finishPrepass(cfg, combo, inBody, restoreLiteBody(preserveSystem, liteOpts, model, provider, liteBody, body), stats)
	}
	stats.Segments = len(s.segs)

	texts := make([]string, len(s.segs))
	kinds := make([]segmentKind, len(s.segs))
	commands := make([]string, len(s.segs))
	tools := make([]string, len(s.segs))
	skips := make([]bool, len(s.segs))
	arrays := make([]bool, len(s.segs))
	for i := range s.segs {
		texts[i] = s.segs[i].value.String()
		arrays[i] = s.segs[i].array
		kinds[i] = s.segs[i].kind
		commands[i] = s.segs[i].command
		tools[i] = s.segs[i].toolName
		skips[i] = s.segs[i].current || s.noAssistant
	}
	originals := append(make([]string, 0, len(texts)), texts...)

	// start is the first segment the pipeline may rewrite. Live-zone reuse
	// raises it past the prefix a previous request already compressed.
	start := 0
	var zone *liveZoneEntry
	if sessionID != "" && cfg.LiveZone.Enabled {
		zone, start = liveZoneReuse(cfg, model, sessionID, body, s.segs)
		if start > 0 {
			for i := 0; i < start; i++ {
				texts[i] = zone.transformed[i]
			}
		}
	}

	// Quantum lock stabilizes the first string-content system message only:
	// volatile spans become placeholders and their values move to an in-band
	// tail, keeping the cacheable prefix stable.
	// Upstream only locks caching providers; unknown models keep plain text.
	if cfg.QuantumLock && modelCachesPrefix(model) {
		for i := range texts {
			if kinds[i] == kindSystem && !s.segs[i].array {
				texts[i] = lockQuantumSpans(texts[i])
				break
			}
		}
	}

	saved := make(map[string]int, len(steps))
	for _, step := range steps {
		before := append(make([]string, 0, len(texts)), texts...)
		inBytes := textBytes(before)
		runEngine(step, texts, kinds, arrays, commands, tools, skips, start)
		if step.opts.FidelityGate != nil && *step.opts.FidelityGate && isLossyEngine(step.eng.name()) {
			for i := range texts {
				if kinds[i] == kindSystem || texts[i] == before[i] {
					continue
				}
				// applyFidelityGate already returns the original text when it
				// reverted; assign its choice unconditionally.
				texts[i], _ = applyFidelityGate(before[i], texts[i])
			}
		}
		delta := 0
		for i := range texts {
			delta += len(before[i]) - len(texts[i])
		}
		if delta < 0 {
			// Inflation guard: a step that grew its input is reverted.
			log.Warnf("payloadcompress: step %q grew its input, reverting", step.eng.name())
			copy(texts, before)
			delta = 0
		}
		name := step.eng.name()
		if stats.EngineBytesIn == nil {
			stats.EngineBytesIn = make(map[string]int, len(steps))
			stats.EngineBytesOut = make(map[string]int, len(steps))
		}
		stats.EngineBytesIn[name] += inBytes
		stats.EngineBytesOut[name] += textBytes(texts)
		if delta == 0 {
			continue
		}
		saved[name] += delta
	}
	if len(saved) > 0 {
		stats.Saved = saved
	}

	changed := false
	for i := range texts {
		if texts[i] != originals[i] {
			changed = true
			break
		}
	}

	out := body
	if changed {
		out = make([]byte, 0, len(body))
		prev := 0
		for i := range s.segs {
			if texts[i] == originals[i] {
				continue
			}
			out = append(out, body[prev:s.segs[i].start]...)
			out = appendJSONString(out, texts[i])
			prev = s.segs[i].end
		}
		out = append(out, body[prev:]...)
	}

	// The budget is an independent post-pass: a config with only a budget and
	// no engine-level change must still enforce it.
	if cfg.HardBudgetTokens > 0 {
		out = []byte(applyHardBudget(string(out), cfg.HardBudgetTokens, !s.includeSystem))
	}
	if liteBody {
		out = applyLiteBodyAfterPreserve(preserveSystem, liteOpts, model, provider, out, passes)
	}
	// Whole-pipeline guard: never ship growth or a body that is no longer JSON.
	if len(out) >= inBodyLen || !json.Valid(out) {
		stats.Saved = nil
		stats.BytesOut = inBodyLen
		// The guard reverted the compression, but a marker already present in the
		// original body (a prior turn's note was trimmed away, a caller-supplied
		// history) still has to be explained. Its fixed size must never count
		// against the guard, so it is added after the decision.
		if cfg.Shadow {
			return inBody, stats
		}
		return applyMarkerProtocolNote(inBody), stats
	}
	stats.BytesOut = len(out)
	if cfg.Shadow {
		return inBody, stats
	}
	liveZoneStore(cfg, model, sessionID, body, s.segs, texts)
	stats.Applied = true
	return applyMarkerProtocolNote(out), stats
}

// engineStep pairs one pipeline entry with its per-step options.
type engineStep struct {
	eng  engine
	opts StepOptions
}

// runEngine applies one step's engine from start onwards. The opt-in risk gate
// masks risky spans before the engine sees them and restores them afterwards.
// Engines that need body-wide context still see every segment, so a reused
// prefix informs session dedup without being rewritten.
func runEngine(step engineStep, texts []string, kinds []segmentKind, arrays []bool, commands, tools []string, skips []bool, start int) {
	eng := step.eng
	var masks []func(string) string
	if step.opts.RiskGate != nil && *step.opts.RiskGate {
		masks = make([]func(string) string, len(texts))
		for i := range texts {
			var unmask func(string) string
			texts[i], unmask = maskRisks(texts[i])
			masks[i] = unmask
		}
	}
	// Lite's whitespace cleanup mirrors upstream's string-content-only pass, so
	// it uses a separate array path. Other engines process arrays normally.
	arrayEng, skipArray := eng.(arrayAware)
	switch typed := eng.(type) {
	case bodyEngine:
		for i, repl := range typed.rewrite(texts, kinds, skips) {
			if i < start || i >= len(texts) || repl == texts[i] {
				continue
			}
			texts[i] = repl
		}
	case commandAware:
		for i := start; i < len(texts); i++ {
			if kinds[i] == kindAssistant {
				continue
			}
			if out, hit := typed.compressCommand(kinds[i], tools[i], commands[i], texts[i]); hit {
				texts[i] = out
			}
		}
	case kindAware:
		for i := start; i < len(texts); i++ {
			if skipArray && arrays[i] {
				if out, hit := arrayEng.compressArray(kinds[i], texts[i]); hit {
					texts[i] = out
				}
				continue
			}
			if out, hit := typed.compressKind(kinds[i], texts[i]); hit {
				texts[i] = out
			}
		}
	case textEngine:
		for i := start; i < len(texts); i++ {
			if out, hit := typed.compress(texts[i]); hit {
				texts[i] = out
			}
		}
	}
	for i := range masks {
		if masks[i] != nil {
			texts[i] = masks[i](texts[i])
		}
	}
}

// isLossyEngine reports whether an engine deliberately drops content, so the
// fidelity gate can reject an over-aggressive rewrite.
func isLossyEngine(name string) bool {
	switch name {
	case EngineCaveman, EngineRTK, EngineLite, EngineHeadroom, EngineMCPAccessibility, EngineRelevance:
		return true
	}
	return false
}

// comboHasEngine reports whether a resolved combo runs the named engine.
func comboHasEngine(combo *Combo, name string) bool {
	for _, step := range combo.Pipeline {
		if step.Engine == name {
			return true
		}
	}
	return false
}

// finishPrepass returns the body when the pipeline had no segment engines, so a
// combo made only of body-level engines still applies and reports its saving.
func finishPrepass(cfg Config, combo *Combo, inBody, out []byte, stats Stats) ([]byte, Stats) {
	if bytes.Equal(out, inBody) || len(out) >= len(inBody) {
		stats.BytesOut = len(inBody)
		if cfg.Shadow {
			return inBody, stats
		}
		return applyMarkerProtocolNote(inBody), stats
	}
	stats.BytesOut = len(out)
	if stats.Saved == nil {
		stats.Saved = map[string]int{}
	}
	for _, step := range combo.Pipeline {
		if step.Engine == EngineReadLifecycle {
			stats.Saved[step.Engine] = len(inBody) - len(out)
		}
	}
	if cfg.Shadow {
		return inBody, stats
	}
	stats.Applied = true
	return applyMarkerProtocolNote(out), stats
}

// readLifecycleEnabled reports whether the read-lifecycle step opted in. The
// engine is default-off upstream.
func readLifecycleEnabled(combo *Combo) bool {
	for _, step := range combo.Pipeline {
		if step.Engine == EngineReadLifecycle {
			return step.Options.ReadLifecycleEnabled != nil && *step.Options.ReadLifecycleEnabled
		}
	}
	return false
}

// restoreLiteBody undoes the lite-only body pass on an early return.
func restoreLiteBody(preserveSystem bool, opts StepOptions, model, provider string, enabled bool, body []byte) []byte {
	if !enabled {
		return body
	}
	return applyLiteBodyAfterPreserve(preserveSystem, opts, model, provider, body, nil)
}

// liteStepOptions returns the options of the combo's lite step.
func liteStepOptions(combo *Combo) StepOptions {
	for _, step := range combo.Pipeline {
		if step.Engine == EngineLite {
			return step.Options
		}
	}
	return StepOptions{}
}

// textBytes sums the decoded segment bytes a pipeline step sees.
func textBytes(texts []string) int {
	n := 0
	for _, text := range texts {
		n += len(text)
	}
	return n
}

// ApplyAll runs the whole transform chain: payload compression, then output-style
// injection. Both are idempotent, so applying it again to its own output is a no-op.
func ApplyAll(cfg Config, model string, body []byte) ([]byte, Stats) {
	return ApplyAllSession(cfg, model, "", body)
}

// ApplyAllSession is ApplyAll for a request that carries a session identity, so
// the live zone can reuse the previous turn's compressed prefix.
func ApplyAllSession(cfg Config, model, sessionID string, body []byte, provider ...string) ([]byte, Stats) {
	out, stats := ApplySession(cfg, model, sessionID, body, provider...)
	if combo := cfg.resolve(model); combo != nil && len(combo.OutputStyles) > 0 {
		cfg.OutputStyles = combo.OutputStyles
	}
	styled, label := applyOutputStyles(cfg, out, body)
	stats.Style = label
	if label == "" || strings.HasPrefix(label, "skipped:") {
		return out, stats
	}
	stats.StyleBytes = len(styled) - len(out)
	if cfg.Shadow {
		return out, stats
	}
	stats.Applied = true
	return styled, stats
}

// testEngineHook injects a synthetic engine role for tests; production leaves it nil.
var testEngineHook func(Step) engine

func buildEngines(steps []Step) ([]engineStep, map[string]int) {
	// The lite pass counter exists only when a lite step can fire, so a combo
	// without lite stays allocation-free.
	var passes map[string]int
	for _, step := range steps {
		if step.Engine == EngineLite {
			passes = map[string]int{}
			break
		}
	}
	engines := make([]engineStep, 0, len(steps))
	for _, step := range steps {
		var eng engine
		if testEngineHook != nil {
			eng = testEngineHook(step)
		}
		switch step.Engine {
		case EngineLite:
			eng = liteEngine{opts: step.Options, passes: passes}
		case EngineRTK:
			eng = rtkEngine{opts: step.Options}
		case EngineHeadroom:
			eng = headroomEngine{opts: step.Options}
		case EngineCaveman:
			eng = cavemanEngine{level: step.Level, opts: step.Options}
		case EngineSessionDedup:
			eng = &dedupEngine{opts: step.Options}
		case EngineMCPAccessibility:
			eng = mcpAccessibilityEngine{opts: step.Options}
		case EngineRelevance:
			eng = relevanceEngine{opts: step.Options}
		}
		if eng != nil {
			engines = append(engines, engineStep{eng: eng, opts: step.Options})
		}
	}
	return engines, passes
}

// bodyString aliases body as a string without copying. The alias never outlives
// the Apply call, so the caller's slice stays owned by the caller.
func bodyString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

var jsonHex = "0123456789abcdef"

// appendJSONString writes s as a JSON string literal. Non-ASCII bytes pass
// through unescaped: valid JSON, and it keeps savings intact for non-Latin text.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			dst = append(dst, '\\', c)
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', jsonHex[c>>4], jsonHex[c&0xf])
		default:
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}

// hasFence reports whether line opens or closes a Markdown code fence.
func hasFence(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// charLen counts characters rather than bytes, so the char-named option caps
// mean the same thing for non-ASCII text as they do for ASCII.
func charLen(s string) int {
	return utf8.RuneCountInString(s)
}
