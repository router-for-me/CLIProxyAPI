package payloadcompress

import (
	"embed"
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
)

// Language packs are the caveman rule sets, one directory per language, taken
// from OmniRoute's rules/ tree. They are data, not code: loaded once and compiled
// on first use. Patterns Go's RE2 cannot express (backreferences) are skipped
// rather than approximated, because a wrong filter silently corrupts prompts.
//
//go:embed rules
var rulesFS embed.FS

// packCategories fixes the rule order inside a language. It matches the order in
// which the packs were authored: phrasing first, abbreviating last.
var packCategories = []string{"structural", "filler", "context", "dedup", "ultra"}

type packRule struct {
	Name           string            `json:"name"`
	Pattern        string            `json:"pattern"`
	Flags          string            `json:"flags"`
	Replacement    *string           `json:"replacement"`
	ReplacementMap map[string]string `json:"replacementMap"`
	MinIntensity   string            `json:"minIntensity"`
}

type packFile struct {
	Language string     `json:"language"`
	Category string     `json:"category"`
	Rules    []packRule `json:"rules"`
}

var (
	packsOnce  sync.Once
	packsByTag map[string][]cavemanRule
	packTags   []string
)

func loadPacks() {
	packsByTag = make(map[string][]cavemanRule, 12)
	byTagCategory := make(map[string]map[string][]cavemanRule, 12)

	entries, err := fs.ReadDir(rulesFS, "rules")
	if err != nil {
		log.Errorf("payloadcompress: read language packs: %v", err)
		return
	}
	skipped := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		tag := entry.Name()
		packTags = append(packTags, tag)
		byTagCategory[tag] = make(map[string][]cavemanRule, len(packCategories))
		for _, category := range packCategories {
			raw, err := rulesFS.ReadFile(path.Join("rules", tag, category+".json"))
			if err != nil {
				continue
			}
			var file packFile
			if errJSON := json.Unmarshal(raw, &file); errJSON != nil {
				log.Warnf("payloadcompress: language pack %s/%s: %v", tag, category, errJSON)
				continue
			}
			for _, raw := range file.Rules {
				rule, ok := compilePackRule(raw)
				if !ok {
					skipped++
					continue
				}
				byTagCategory[tag][category] = append(byTagCategory[tag][category], rule)
			}
		}
	}

	// A pack that ships fewer categories falls back to English for the missing
	// ones, which is what the upstream packs document.
	english := byTagCategory["en"]
	for tag, categories := range byTagCategory {
		var ordered []cavemanRule
		for _, category := range packCategories {
			rules := categories[category]
			if len(rules) == 0 {
				rules = english[category]
			}
			ordered = append(ordered, rules...)
		}
		packsByTag[tag] = ordered
	}
	if skipped > 0 {
		log.Infof("payloadcompress: %d language-pack rules skipped (not expressible in RE2)", skipped)
	}
}

func packs() map[string][]cavemanRule {
	packsOnce.Do(loadPacks)
	return packsByTag
}

// packRulesFor returns the ordered rules for a language tag, falling back to
// English for unknown tags.
func packRulesFor(tag string) []cavemanRule {
	all := packs()
	if rules, ok := all[tag]; ok {
		return rules
	}
	return all["en"]
}

// languageTags returns the shipped tags, sorted, for config validation.
func languageTags() []string {
	packs()
	tags := append([]string(nil), packTags...)
	for i := 1; i < len(tags); i++ {
		for j := i; j > 0 && tags[j] < tags[j-1]; j-- {
			tags[j], tags[j-1] = tags[j-1], tags[j]
		}
	}
	return tags
}

// compilePackRule converts one JS-flavoured pack rule into a Go rule. It reports
// false for patterns RE2 cannot express.
func compilePackRule(raw packRule) (cavemanRule, bool) {
	if raw.Pattern == "" {
		return cavemanRule{}, false
	}
	// Backreferences are the one construct with no safe rewrite.
	for i := 0; i+1 < len(raw.Pattern); i++ {
		if raw.Pattern[i] == '\\' && raw.Pattern[i+1] >= '1' && raw.Pattern[i+1] <= '9' {
			return cavemanRule{}, false
		}
	}

	pattern := raw.Pattern
	rule := cavemanRule{name: raw.Name, level: strings.ToLower(raw.MinIntensity)}

	for strings.HasPrefix(pattern, "(?<!") {
		end := strings.Index(pattern, ")")
		if end < 0 {
			return cavemanRule{}, false
		}
		inner := pattern[4:end]
		pattern = pattern[end+1:]
		switch {
		case isWordClass(inner):
			rule.notAfterWord = true
		case strings.HasSuffix(inner, `\s`):
			rule.notAfter = append(rule.notAfter, inner[:len(inner)-2])
		case isPlainLiteral(inner):
			rule.notAfter = append(rule.notAfter, inner)
		default:
			return cavemanRule{}, false
		}
	}

	if idx := strings.Index(pattern, `\s+(?=[a-z])`); idx >= 0 && idx == len(pattern)-len(`\s+(?=[a-z])`) {
		pattern = pattern[:idx+len(`\s+`)]
		rule.lowerOnly = true
	}

	for _, guard := range []string{`(?![\p{L}\p{N}_])`, `(?![\p{L}\p{N}])`} {
		idx := strings.Index(pattern, guard)
		if idx < 0 {
			continue
		}
		rest := pattern[idx+len(guard):]
		switch {
		case rest == "":
		case rest == `\s*`:
			rule.tail = tailSpace
		case rest == `[,.!?\s]*`:
			rule.tail = tailPunct
		case rest == `[,.]?\s*`:
			rule.tail = tailPunct
		default:
			return cavemanRule{}, false
		}
		pattern = pattern[:idx]
		rule.notBeforeWord = true
		break
	}

	if strings.Contains(pattern, "(?=") || strings.Contains(pattern, "(?!") ||
		strings.Contains(pattern, "(?<") || strings.Contains(pattern, "(?>") {
		return cavemanRule{}, false
	}

	// The packs omit the `i` flag that their TS originals carried, and list the
	// capitalised variants by hand only for some rules. Prose filler matching is
	// case-insensitive by intent, so it is always applied here.
	prefix := "(?i)"
	for _, flag := range raw.Flags {
		switch flag {
		case 'm':
			prefix += "(?m)"
		case 's':
			prefix += "(?s)"
		}
	}

	compiled, err := regexp.Compile(prefix + pattern)
	if err != nil {
		return cavemanRule{}, false
	}
	rule.re = compiled

	if len(raw.ReplacementMap) > 0 {
		rule.table = raw.ReplacementMap
	} else if raw.Replacement != nil {
		rule.repl = *raw.Replacement
	}
	return rule, true
}

func isWordClass(inner string) bool {
	switch inner {
	case `[\p{L}\p{N}_]`, `[\p{L}\p{N}]`, `[a-zA-Z0-9_]`, `\w`, `[a-z]`:
		return true
	}
	return false
}

func isPlainLiteral(inner string) bool {
	if inner == "" {
		return false
	}
	return !strings.ContainsAny(inner, `[].*+?()|\\{}^$`)
}

// isWordPrefix reports whether the rune before start is a letter, digit or
// underscore, mirroring the JS lookbehind classes used by the packs.
func isWordPrefix(text string, start int) bool {
	if start == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(text[:start])
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

func isWordNext(text string, end int) bool {
	if end >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[end:])
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// detectLanguage picks a pack from the prompt text. Han ideographs without kana
// are Chinese; otherwise the language with the most native marker hits wins, and
// no hits means English.
func detectLanguage(text string) string {
	var han, kana bool
	for _, r := range text {
		switch {
		case r >= 0x4e00 && r <= 0x9fff:
			han = true
		case r >= 0x3040 && r <= 0x30ff:
			kana = true
		}
		if han && kana {
			break
		}
	}
	if han && !kana {
		return "zh"
	}

	best, bestScore := "en", 0
	for _, tag := range []string{"it", "pt-BR", "es", "de", "fr", "ru", "ja", "hu", "id"} {
		score := 0
		for _, marker := range languageMarkers[tag] {
			score += strings.Count(text, marker)
		}
		if score > bestScore {
			best, bestScore = tag, score
		}
	}
	if kana && bestScore == 0 {
		return "ja"
	}
	return best
}

// languageMarkers are the distinctive native words from the upstream detector.
// English-ambiguous words are excluded so English text is never misclassified.
var languageMarkers = map[string][]string{
	"it":    {"perché", "perche", "però", "cioe", "cioè", "quindi", "potresti", "vorrei", "adesso", "errore", "grazie", "devo", "voglio", "questo", "quello", "anche", "sono", "molto"},
	"pt-BR": {"você", "voce", "preciso", "arquivo", "código", "codigo", "falha", "obrigado"},
	"es":    {"necesito", "archivo", "código", "codigo", "fallo", "gracias", "puedes"},
	"de":    {"ich", "datei", "fehler", "bitte", "kannst", "konfiguration", "danke"},
	"fr":    {"fichier", "erreur", "merci", "peux", "besoin"},
	"ru":    {"это", "что", "как", "если", "чтобы", "который", "может", "нужно", "есть", "было", "будет", "можно", "должен", "файл", "ошибка", "проблема", "данные"},
	"hu":    {"kérlek", "tudnád", "szeretném", "magyarázd", "mutasd", "miért", "hogyan", "függvény", "beállítás", "adatbázis", "hiba", "fájl"},
	"id":    {"saya", "kamu", "anda", "dengan", "untuk", "yang", "tidak", "bisa", "terima kasih", "dari"},
}
