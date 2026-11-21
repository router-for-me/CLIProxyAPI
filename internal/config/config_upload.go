package config

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/MoeForge/moego/base64"
	"gopkg.in/yaml.v3"
)

var errConfigSaveOrphansSecret = errors.New("save would leave a plaintext remote management key without its identifying setting")

// ValidateConfigSave prevents a rewrite from orphaning known plaintext in a
// copy or comment. An unchanged plaintext setting remains identifiable and
// will still be checked (and, if safe, rewritten) by upload preparation.
func ValidateConfigSave(original, replacement []byte) error {
	if bytes.Equal(original, replacement) {
		return nil
	}
	// Allow repairing invalid input, but keep any known secrets from the
	// validated prefix even if a trailing document was invalid.
	docs, _ := decodeConfigStream(original)
	secrets, _, _ := inspectConfigSecrets(docs)
	if len(secrets) == 0 {
		return nil
	}
	updated, err := decodeConfigStream(replacement)
	if err != nil {
		return err
	}
	remaining, _, exempt := inspectConfigSecrets(updated)
	for secret := range remaining {
		delete(secrets, secret)
	}
	if containsPlaintextConfigSecret(updated, replacement, secrets, exempt) {
		return errConfigSaveOrphansSecret
	}
	return nil
}

// Upload validation covers both spellings in every document, including
// shadowed merge values and copies that survive as archived comments.
func verifyConfigUpload(original []*yaml.Node, prepared []byte) error {
	secrets, malformed, _ := inspectConfigSecrets(original)
	if malformed {
		return errors.New("remote management key in config stream must be a scalar")
	}
	if len(secrets) == 0 {
		return nil
	}
	docs, err := decodeConfigStream(prepared)
	if err != nil {
		return err
	}
	remaining, badType, exempt := inspectConfigSecrets(docs)
	if len(remaining) != 0 || badType {
		return errors.New("plaintext remote management key is still set in the config stream")
	}
	if containsPlaintextConfigSecret(docs, prepared, secrets, exempt) {
		return errors.New("plaintext remote management key is still present elsewhere in the config stream")
	}
	return nil
}

func containsPlaintextConfigSecret(docs []*yaml.Node, data []byte, secrets map[string]bool, exempt map[*yaml.Node]bool) bool {
	if len(secrets) == 0 {
		return false
	}
	values := configPlaintextValues(secrets)
	commentValues := make([]string, 0, len(values)*3)
	var multilineValues []string
	for value := range values {
		quoted := strconv.Quote(value)
		commentValues = append(commentValues, value, quoted[1:len(quoted)-1], base64.StdEncoding.EncodeToString([]byte(value)))
		if strings.ContainsAny(value, "\r\n\u0085\u2028\u2029") {
			if compact := stripConfigWhitespace(value); compact != "" {
				multilineValues = append(multilineValues, compact)
			}
		}
	}
	containsComment := func(comment string) bool {
		if comment == "" {
			return false
		}
		expanded := os.ExpandEnv(comment)
		for _, value := range commentValues {
			if strings.Contains(comment, value) || strings.Contains(expanded, value) {
				return true
			}
		}
		return len(multilineValues) != 0 && (containsConfigCommentBlock(comment, multilineValues) ||
			(expanded != comment && containsConfigCommentBlock(expanded, multilineValues)))
	}
	var containsSecret func(*yaml.Node) bool
	containsSecret = func(node *yaml.Node) bool {
		if containsComment(node.HeadComment) || containsComment(node.LineComment) || containsComment(node.FootComment) {
			return true
		}
		resolved := derefConfigNode(node)
		if isConfigNullScalar(resolved) {
			exempt[node] = true
		} else if !exempt[node] && containsConfigScalarSecret(resolved, values) {
			return true
		}
		// Non-scalar alias targets are already covered by their physical definitions.
		for _, child := range node.Content {
			if containsSecret(child) {
				return true
			}
		}
		return false
	}
	for _, doc := range docs {
		if containsSecret(doc) {
			return true
		}
	}
	if codec := configUTF16Encoding(data); codec != nil {
		data, _ = codec.NewDecoder().Bytes(data) // The stream was already validated.
	}
	source := strings.TrimPrefix(string(data), "\ufeff")
	if containsConfigSourceSecret(source, values, exempt) {
		return true
	}
	// yaml.v3 may not attach comments outside a document to any node.
	for line := range strings.FieldsFuncSeq(source, isConfigLineBreak) {
		line = strings.TrimPrefix(line, "\ufeff")
		if yamlDocumentMarker([]byte(line), "...") || yamlDocumentMarker([]byte(line), "---") {
			_, comment, found := strings.Cut(line, "#")
			if found && containsComment(comment) {
				return true
			}
		}
		if comment, found := strings.CutPrefix(strings.TrimLeft(line, " \t"), "#"); found && containsComment(comment) {
			return true
		}
	}
	return false
}

func configPlaintextValues(secrets map[string]bool) map[string]bool {
	values := make(map[string]bool, len(secrets))
	for secret := range secrets {
		values[secret] = true
		if value := os.ExpandEnv(secret); value != "" {
			values[value] = true
		}
	}
	return values
}

func containsConfigScalarSecret(node *yaml.Node, values map[string]bool) bool {
	if node.Kind != yaml.ScalarNode || len(values) == 0 {
		return false
	}
	value := configScalarString(node)
	expanded := os.ExpandEnv(value)
	for candidate := range values {
		if strings.Contains(node.Value, candidate) || strings.Contains(value, candidate) || strings.Contains(expanded, candidate) {
			return true
		}
	}
	return false
}

// Check the original spelling, including non-scalar tokens and unattached text.
// An occurrence is safe only if it lies entirely within a known safe scalar's
// source span. Never remove matching text globally or skip the surrounding line.
func containsConfigSourceSecret(source string, values map[string]bool, exempt map[*yaml.Node]bool) bool {
	type span struct{ start, end int }
	var spans []span
	lineStarts := []int{0}
	for i, r := range source {
		if isConfigLineBreak(r) && !(r == '\r' && i+1 < len(source) && source[i+1] == '\n') {
			lineStarts = append(lineStarts, i+len(string(r)))
		}
	}
	for node := range exempt {
		if node.Kind != yaml.ScalarNode || node.Line < 1 || node.Line > len(lineStarts) {
			continue
		}
		lineStart := lineStarts[node.Line-1]
		column := node.Column
		for offset := range source[lineStart:] {
			if column == 1 {
				scalar := configSourceScalar(source[lineStart+offset:], node)
				if size := configSourceScalarLen(scalar, node); size != 0 {
					start := len(source) - len(scalar)
					spans = append(spans, span{start, start + size})
				}
				break
			}
			column--
		}
	}
	slices.SortFunc(spans, func(a, b span) int { return a.start - b.start })
	for candidate := range values {
		start, nextSpan := 0, 0
		for {
			offset := strings.Index(source[start:], candidate)
			if offset < 0 {
				break
			}
			start += offset
			for nextSpan < len(spans) && spans[nextSpan].end <= start {
				nextSpan++
			}
			if nextSpan == len(spans) || spans[nextSpan].start > start || start+len(candidate) > spans[nextSpan].end {
				return true
			}
			start++
		}
	}
	return false
}

func isConfigLineBreak(r rune) bool {
	return r == '\n' || r == '\r' || r == '\u0085' || r == '\u2028' || r == '\u2029'
}

func configSourceScalar(source string, node *yaml.Node) string {
	// Preserve anchors and tags in the raw scan; only the scalar is exempt.
	for range 2 {
		if rest, ok := strings.CutPrefix(source, "&"+node.Anchor); node.Anchor != "" && ok {
			source = strings.TrimLeft(rest, " \t")
		} else if rest, ok := strings.CutPrefix(source, node.ShortTag()); node.Style&yaml.TaggedStyle != 0 && ok {
			source = strings.TrimLeft(rest, " \t")
		}
	}
	return source
}

func configSourceScalarLen(source string, node *yaml.Node) int {
	literal := node.Value
	switch node.Style &^ yaml.TaggedStyle {
	case yaml.SingleQuotedStyle:
		literal = "'" + strings.ReplaceAll(literal, "'", "''") + "'"
	case yaml.DoubleQuotedStyle:
		literal = strconv.Quote(literal)
	case 0:
	default:
		// Do not guess the end of a multiline safe scalar.
		return 0
	}
	if strings.IndexFunc(literal, isConfigLineBreak) < 0 && strings.HasPrefix(source, literal) {
		return len(literal)
	}
	return 0
}

// Archived block scalars retain indentation and gain a comment marker on each
// line. Compare their contents without either.
func containsConfigCommentBlock(comment string, values []string) bool {
	var block strings.Builder
	for line := range strings.SplitSeq(comment, "\n") {
		block.WriteString(stripConfigWhitespace(strings.TrimPrefix(strings.TrimSpace(line), "#")))
	}
	for _, value := range values {
		if strings.Contains(block.String(), value) {
			return true
		}
	}
	return false
}

func stripConfigWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

// Only structural management keys and verified-format hashes in those settings
// are exempt from substring matching. Do not exempt equal text elsewhere.
func inspectConfigSecrets(docs []*yaml.Node) (map[string]bool, bool, map[*yaml.Node]bool) {
	candidates := make(map[string]bool)
	exempt := make(map[*yaml.Node]bool)
	malformed := false
	for _, doc := range docs {
		for _, path := range managementSecretPaths {
			visitConfigEntries(doc.Content[0], path[0], func(section, mapping *yaml.Node) bool {
				if isConfigStringLiteral(section, path[0]) {
					exempt[section] = true
				}
				visitConfigEntries(mapping, path[1], func(key, raw *yaml.Node) bool {
					if isConfigStringLiteral(key, path[1]) {
						exempt[key] = true
					}
					node := derefConfigNode(raw)
					if node.Kind != yaml.ScalarNode {
						malformed = true
					} else if !isConfigNullScalar(node) {
						value := configScalarString(node)
						if (node.ShortTag() == "!!str" || node.ShortTag() == "!!binary") && looksLikeBcrypt(value) {
							if isConfigStringLiteral(raw, value) {
								exempt[raw] = true
							}
						} else if value != "" {
							candidates[node.Value] = true
							candidates[value] = true
						}
					}
					return true
				})
				return true
			})
		}
	}
	return candidates, malformed, exempt
}

func isConfigStringLiteral(node *yaml.Node, value string) bool {
	return node.Kind == yaml.ScalarNode && node.ShortTag() == "!!str" && node.Value == value
}

func isConfigNullScalar(node *yaml.Node) bool {
	if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!null" {
		return false
	}
	// A merge can hide a scalar with an explicit tag and an invalid value
	// from the YAML decoder. Do not treat its tag as proof that it is null.
	var value any
	return node.Decode(&value) == nil && value == nil
}
