package payloadcompress

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Output styles steer how the model writes its answer. They are pure prompt
// injection into the existing system prompt: no response rewriting, so streaming,
// tool calls and every protocol keep working untouched.
//
// Instruction text must be identical on every request for a given
// (id, level), otherwise the injected prefix stops being cacheable.

// outputStyleMarker makes injection idempotent across retries and rebuilds.
const outputStyleMarker = "[CLIProxyAPI Output Styles]"

// sharedBoundaries is appended to every style: the carve-outs where terse
// writing would be actively harmful.
const sharedBoundaries = "Code blocks, file paths, commands, errors, URLs: keep exact. " +
	"Security warnings, irreversible action confirmations, multi-step ordered sequences: write normal. " +
	"Resume terse style after. Active every response until user asks for normal mode."

// safetyBoundaries is the ponytail carve-out. Code-shaping styles add it because
// "be lazy" without it drops validation, error handling, security and a11y.
const safetyBoundaries = "Never simplify away: input validation at trust boundaries, error handling " +
	"that prevents data loss, security measures, or accessibility basics."

// styleLevels holds the instruction text per intensity.
type styleLevels struct {
	lite  string
	full  string
	ultra string
	// boundaries replaces sharedBoundaries when set.
	boundaries string
}

func (s styleLevels) text(level string) string {
	switch level {
	case "lite":
		return s.lite
	case "ultra":
		return s.ultra
	default:
		return s.full
	}
}

// antislopBoundaries is the shared craft clause for both antislop styles.
const antislopBoundaries = "Every shipped control works or is removed: no dead buttons, no links to nowhere, " +
	"no navbar entries without destinations. No fabricated statistics, testimonials, security or compliance claims. " +
	"Placeholders are labeled honestly, never disguised as final."

// styleCatalog is the registry. Adding a style is one entry here.
var styleCatalog = map[string]styleLevels{
	"terse-prose": {
		lite:  "Respond concise. Drop filler, pleasantries, hedging. Keep full sentences, technical terms, code, errors, URLs, and identifiers exact.",
		full:  "Respond terse like smart caveman. Drop articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries, hedging. Fragments OK. Short synonyms (big not extensive, fix not implement). Keep all technical substance, code, errors, URLs, identifiers exact.",
		ultra: "Respond ultra terse. Maximum compression. Telegraphic. Abbreviate (DB/auth/config/req/res/fn/impl), strip conjunctions, arrows for causality (X → Y). One word when one word enough. Never abbreviate code symbols, API names, error strings, URLs, or identifiers.",
	},
	"less-code": {
		boundaries: safetyBoundaries,
		lite:       "Write the smallest change that satisfies the request. Skip speculative abstractions.",
		full:       "Act like a lazy senior dev applying YAGNI. Smallest working change only. No unrequested abstractions, no premature generalization, no extra layers, no defensive scaffolding the request did not ask for. Reuse existing code over adding new code.",
		ultra:      "Minimal diff discipline. Touch the fewest lines that make it work. Zero new files, classes, or config unless strictly required. Inline over abstract. No \"while we're here\" extras.",
	},
	"ponytail": {
		boundaries: safetyBoundaries,
		lite: "# Ponytail (lite)\nBefore writing code: does it need to exist? Does it already exist here? " +
			"Does the stdlib or an installed dep cover it? Only then: write the minimum. Reuse over rewrite.",
		full: `# Ponytail — lazy senior dev

You are a lazy senior developer. Lazy = efficient, not careless. The best code is the code never written.

Before writing any code, stop at the first rung that holds:
1. Does this need to exist? (YAGNI)
2. Does it already exist in this codebase? Reuse it.
3. Does the stdlib do this? Use it.
4. Does a platform feature or installed dep cover it? Use it.
5. Can it be one line? Make it one line.
6. Only then: write the minimum that works.

Bug fix = root cause, not symptom. Grep every caller of the function you touch; fix the shared function once — one guard there is a smaller diff than one per caller.

Rules:
- No unrequested abstractions. No new deps. No boilerplate.
- Deletion over addition. Boring over clever. Fewest files.
- Shortest working diff wins — but only after you understand the problem.
- Question complex asks: "Do you need X, or does Y cover it?"
- When two solutions tie, pick the edge-case-correct one.`,
		ultra: "# Ponytail (ultra)\nLazy senior dev. Best code = code never written. Before any code: YAGNI → reuse → stdlib → platform → installed dep → one line → minimum that works. Fix root cause not symptom: grep every caller, patch shared function once. No unrequested abstractions, no new deps, no boilerplate. Deletion > addition. Fewest files. Shortest working diff, only after understanding the problem. Question complex asks. Edge-case-correct when tied.",
	},
	// Wenyan: classical-Chinese ultra-terse style, ported from 9router's wenyan
	// prompt. Instruction text is Chinese; the shared boundaries stay English.
	"terse-cjk": {
		lite:  "回答从简，去虚词、寒暄、修饰。代码、路径、命令、错误、URL、标识符一律照原样保留。",
		full:  "以文言简体回答，惜字如金，去赘语虚词。代码、路径、命令、错误、URL、标识符照原样保留，不得改写。",
		ultra: "以极简文言回答，字字千金。仅留要义。代码、API名、错误串、URL、标识符照原样保留，绝不省略或改写。",
	},
	"antislop-ui": {
		boundaries: antislopBoundaries,
		lite:       "Antislop UI filter (lite). Every visual technique needs a purpose: hierarchy, identity, or readability; technique without purpose is removed. No default blue-purple gradient wash, no glass or glow on every surface, no template layout (hero plus identical card grid, bento mosaic, Trusted-By bar).",
		full:       "Antislop UI filter, not a style guide. Every visual technique must pass a purpose test: it serves hierarchy, identity, or readability, and the reason is written down. Technique without purpose is removed. Color and decoration are accents, not defaults: no blue-purple gradient wash, no glass on every surface (max 1-2 elements), no glow everywhere (max 1-2 focus accents), no large shadow on every component. Radius follows one system, not pills everywhere. Palette capped at 2-3 core colors plus 1 accent. No template layout (hero plus identical card grid, bento mosaic, fake terminal hero, Trusted-By bar, 3 pricing columns). Composition follows content needs, not the AI default order.",
		ultra:      "Antislop UI (ultra). Purpose test every visual technique (hierarchy, identity, readability) or cut it. Accents, not defaults: no gradient wash; glass max 1-2 elements; glow max 1-2 focus accents; no large shadow on every component; one radius system; 2-3 core colors plus 1 accent. No template layouts (hero plus card grid, bento, fake terminal hero, Trusted-By bar, 3 pricing columns). Layout follows content.",
	},
	"antislop-copy": {
		boundaries: antislopBoundaries,
		lite:       "Antislop copy filter (lite). Specific language, not AI defaults. Never use the em dash character. No generic CTAs (Get Started, Learn More), no buzzwords (Seamless, Revolutionary), no chatbot closers (Hope this helps). Never invent facts, names, numbers, or quotes. Text contrast meets WCAG AA (4.5:1 normal, 3:1 large); every control is reachable by keyboard.",
		full:       "Antislop copy and human filter. Copy uses specific language, not AI defaults. Never use the em dash character. No generic CTAs (Get Started, Learn More, Try Now, Explore, Discover). No buzzwords (AI Powered, Seamless, Revolutionary, Cutting Edge, Next Generation). No chatbot closers (Hope this helps, Let me know if). No signposting announcements (Let's dive in, Here's what you need to know). No fake-candid openers (Honestly?, Real talk). Name the actor: We rewrote pricing, never pricing was updated. No forced rule-of-three trios, no not-just-X-its-Y formulas, no aphorism formulas. Never invent facts, names, numbers, or quotes. UI holds up for real people: text contrast meets WCAG AA (4.5:1 normal, 3:1 large), checked across the whole area. Never outline none without a visible focus-visible replacement. All controls reachable by keyboard (Tab, Enter or Space, Escape closes dialogs). No mouse-only patterns, no color-only status (pair with text or icon). Data views ship perceivable empty, loading, and error states. Mobile reflows with intent: no horizontal overflow, 200 percent zoom never clips text, focused input never hidden under the on-screen keyboard, tap targets at least 44px.",
		ultra:      "Antislop copy and human (ultra). Specific words only. No em dash character. Ban generic CTAs (Get Started, Learn More, Try Now), buzzwords (AI Powered, Seamless, Next Generation), chatbot closers, signposting, fake-candid openers, forced trios, not-just-X-its-Y. Name the actor. Never invent facts, names, numbers, quotes. WCAG AA contrast (4.5:1 normal, 3:1 large); visible focus-visible; full keyboard (Tab, Enter or Space, Escape); no color-only status; empty, loading, error states; mobile: no horizontal overflow, 200 percent zoom safe, tap targets 44px+.",
	},
	"i-have-adhd": {
		lite: "# I have ADHD (lite)\nLead with the action: command, path, or snippet first, prose after. Number multi-step work; each step one bounded action. End with ONE concrete next step. No preamble, no recap, no closing pleasantries.",
		full: `# I have ADHD — action-first output

The reader has ADHD. Shape output so an ADHD brain can act on it:
1. Lead with the next action — command, path, or snippet first; context after, if at all.
2. Number multi-step work; each step is one bounded action; use the fewest steps that work.
3. End with ONE concrete next step doable in under two minutes.
4. Suppress tangents: finish the first issue, offer the second as a separate question.
5. In multi-turn work, restate where things stand ("step 3 of 5 done") — the reader cannot hold state between messages.
6. When human effort is involved, estimate it in concrete units (minutes, an afternoon), never "some work".
7. Make wins visible: state what now works and how to try it.
8. Errors matter-of-fact: cause and fix; never "Uh oh".
9. Cap lists at 5 items; split into "do now" vs "later" beyond that.
10. No preamble, no recap, no closers ("Hope this helps").
Exceptions: an explicit "explain" request gets a full body (still no preamble/closer).`,
		ultra: "# I have ADHD (ultra)\nAction first, always. One command/path/snippet, then at most one line of context. Numbered steps, one action each. End with ONE next step under two minutes. No preamble, no recap, no closers.",
	},
}

// OutputStyle selects one catalog style at one intensity.
type OutputStyle struct {
	ID    string `yaml:"id" json:"id"`
	Level string `yaml:"level" json:"level"`
}

func knownStyle(id string) bool {
	_, ok := styleCatalog[id]
	return ok
}

func normalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "lite":
		return "lite"
	case "ultra":
		return "ultra"
	default:
		return "full"
	}
}

// styleInstruction builds the injected text. Unknown ids are dropped, so a typo
// degrades to "no style" instead of a broken prompt. Boundary clauses are emitted
// once even when several styles declare the same one.
func styleInstruction(selection []OutputStyle) string {
	var b strings.Builder
	b.WriteString(outputStyleMarker)
	applied := 0
	seenBoundary := map[string]bool{}
	var boundaries []string
	for _, sel := range selection {
		id := strings.ToLower(strings.TrimSpace(sel.ID))
		levels, ok := styleCatalog[id]
		if !ok {
			continue
		}
		b.WriteString("\n- [" + id + "] " + levels.text(normalizeLevel(sel.Level)))
		applied++
		if levels.boundaries != "" && !seenBoundary[levels.boundaries] {
			seenBoundary[levels.boundaries] = true
			boundaries = append(boundaries, levels.boundaries)
		}
	}
	if applied == 0 {
		return ""
	}
	for _, clause := range boundaries {
		b.WriteString("\n" + clause)
	}
	b.WriteString("\n" + sharedBoundaries)
	return b.String()
}

// bypassKeywords suppress styling when the answer must not be terse. A security
// warning or an irreversible-action confirmation deserves normal prose.
var bypassKeywords = []string{
	"security", "vulnerability", "exploit", "credential leak", "secret leak", "malware", "phishing",
	"delete", "drop table", "truncate", "destroy", "wipe", "irreversible", "permanently remove",
	"clarify", "explain in detail", "more detail", "step by step", "why exactly", "what do you mean",
}

// styleBypass reports why styling must be skipped for this request.
func styleBypass(body []byte) string {
	if hasMediaBlocks(body) {
		return "media"
	}
	text := strings.ToLower(recentUserText(body))
	if text == "" {
		return ""
	}
	for _, kw := range bypassKeywords {
		if strings.Contains(text, kw) {
			return "keyword:" + kw
		}
	}
	return ""
}

func hasMediaBlocks(body []byte) bool {
	for _, key := range []string{"image", "image_url", "input_image", "document"} {
		if bytes.Contains(body, []byte(`"`+key+`"`)) {
			return true
		}
	}
	return false
}

// recentUserText concatenates the text of the last three messages.
func recentUserText(body []byte) string {
	var b strings.Builder
	for _, field := range []string{"messages", "input"} {
		items := gjson.Get(bodyString(body), field)
		if !items.IsArray() {
			continue
		}
		start := items.Get("#").Int() - 3
		if start < 0 {
			start = 0
		}
		items.ForEach(func(key, item gjson.Result) bool {
			if key.Int() < int64(start) {
				return true
			}
			switch content := item.Get("content"); {
			case content.Type == gjson.String:
				b.WriteString(content.String())
				b.WriteByte('\n')
			case content.IsArray():
				content.ForEach(func(_, part gjson.Result) bool {
					if text := part.Get("text"); text.Type == gjson.String {
						b.WriteString(text.String())
						b.WriteByte('\n')
					}
					return true
				})
			}
			return true
		})
	}
	return b.String()
}

// applyOutputStyles injects the configured styles into the system prompt. It
// appends, so the bulk of the prefix stays byte-identical between requests.
func applyOutputStyles(cfg Config, body []byte) ([]byte, string) {
	if len(cfg.OutputStyles) == 0 {
		return body, ""
	}
	instruction := styleInstruction(cfg.OutputStyles)
	if instruction == "" {
		return body, ""
	}
	if bytes.Contains(body, []byte(outputStyleMarker)) {
		return body, ""
	}
	if reason := styleBypass(body); reason != "" {
		return body, "skipped:" + reason
	}

	if out, ok := injectChatSystem(body, instruction); ok {
		return out, "chat"
	}
	if out, ok := injectClaudeSystem(body, instruction); ok {
		return out, "claude"
	}
	if out, ok := injectResponsesInstructions(body, instruction); ok {
		return out, "responses"
	}
	return body, "skipped:no-system-slot"
}

// injectChatSystem appends to the first system/developer message of a chat body.
func injectChatSystem(body []byte, instruction string) ([]byte, bool) {
	items := gjson.Get(bodyString(body), "messages")
	if !items.IsArray() {
		return nil, false
	}
	var found bool
	var path string
	items.ForEach(func(key, item gjson.Result) bool {
		switch item.Get("role").String() {
		case "system", "developer":
		default:
			return true
		}
		if item.Get("content").Type != gjson.String {
			return true
		}
		path = "messages." + key.String() + ".content"
		found = true
		return false
	})
	if !found {
		return nil, false
	}
	existing := gjson.Get(bodyString(body), path)
	if existing.Type != gjson.String {
		return nil, false
	}
	out, err := sjson.SetBytes(body, path, existing.String()+"\n\n"+instruction)
	if err != nil {
		return nil, false
	}
	return out, true
}

// injectClaudeSystem appends to the Anthropic top-level system field.
func injectClaudeSystem(body []byte, instruction string) ([]byte, bool) {
	system := gjson.Get(bodyString(body), "system")
	switch {
	case system.Type == gjson.String:
		out, err := sjson.SetBytes(body, "system", system.String()+"\n\n"+instruction)
		if err != nil {
			return nil, false
		}
		return out, true
	case system.IsArray():
		out, err := sjson.SetRawBytes(body, "system.-1", []byte(`{"type":"text","text":`+
			string(appendJSONString(nil, instruction))+`}`))
		if err != nil {
			return nil, false
		}
		return out, true
	}
	return nil, false
}

// injectResponsesInstructions appends to the Responses top-level instructions.
func injectResponsesInstructions(body []byte, instruction string) ([]byte, bool) {
	instructions := gjson.Get(bodyString(body), "instructions")
	if instructions.Type != gjson.String {
		return nil, false
	}
	out, err := sjson.SetBytes(body, "instructions", instructions.String()+"\n\n"+instruction)
	if err != nil {
		return nil, false
	}
	return out, true
}
