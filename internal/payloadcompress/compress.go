package payloadcompress

import (
	"strings"
	"unsafe"
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

// bodyEngine rewrites the whole payload at once, for transforms that depend on
// repeated content across segments. It returns replacement text by segment index.
type bodyEngine interface {
	engine
	rewrite(texts []string) map[int]string
}

// Apply compresses a prepared request payload. It is a pure function: the same
// bytes always yield the same bytes, which is what keeps provider prompt caches
// warm across retries, fallbacks and websocket continuations.
//
// The returned body aliases the input when nothing was compressed.
func Apply(cfg Config, model string, body []byte) ([]byte, Stats) {
	stats := Stats{BytesIn: len(body)}
	if !cfg.Enabled || len(body) < cfg.MinBytes {
		return body, stats
	}
	combo := cfg.resolve(model)
	if combo == nil {
		return body, stats
	}
	stats.Combo = combo.Name

	s := &scanner{body: body}
	if !s.scan() {
		return body, stats
	}

	engines := buildEngines(combo.Pipeline)
	if len(engines) == 0 {
		return body, stats
	}
	stats.Segments = len(s.segs)

	texts := make([]string, len(s.segs))
	for i := range s.segs {
		texts[i] = s.segs[i].value.String()
	}
	changed := make([]bool, len(texts))
	dirty := false

	for _, eng := range engines {
		saved := 0
		if be, ok := eng.(bodyEngine); ok {
			for i, repl := range be.rewrite(texts) {
				if i < 0 || i >= len(texts) || repl == texts[i] {
					continue
				}
				saved += len(texts[i]) - len(repl)
				texts[i] = repl
				changed[i] = true
			}
		} else if te, ok := eng.(textEngine); ok {
			for i := range texts {
				out, hit := te.compress(texts[i])
				if !hit || out == texts[i] {
					continue
				}
				saved += len(texts[i]) - len(out)
				texts[i] = out
				changed[i] = true
			}
		}
		if saved != 0 {
			if stats.Saved == nil {
				stats.Saved = make(map[string]int, len(engines))
			}
			stats.Saved[eng.name()] = saved
			dirty = true
		}
	}

	if !dirty {
		return body, stats
	}

	out := make([]byte, 0, len(body))
	prev := 0
	for i := range s.segs {
		if !changed[i] {
			continue
		}
		out = append(out, body[prev:s.segs[i].start]...)
		out = appendJSONString(out, texts[i])
		prev = s.segs[i].end
	}
	out = append(out, body[prev:]...)

	stats.BytesOut = len(out)
	if cfg.Shadow {
		return body, stats
	}
	stats.Applied = true
	return out, stats
}

// ApplyAll runs the whole transform chain: payload compression, then output-style
// injection. Both are idempotent, so applying it again to its own output is a no-op.
func ApplyAll(cfg Config, model string, body []byte) ([]byte, Stats) {
	out, stats := Apply(cfg, model, body)
	styled, label := applyOutputStyles(cfg, out)
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

func buildEngines(steps []Step) []engine {
	engines := make([]engine, 0, len(steps))
	for _, step := range steps {
		switch step.Engine {
		case EngineLite:
			engines = append(engines, liteEngine{})
		case EngineRTK:
			engines = append(engines, rtkEngine{})
		case EngineHeadroom:
			engines = append(engines, headroomEngine{})
		case EngineSessionDedup:
			engines = append(engines, &dedupEngine{})
		}
	}
	return engines
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
