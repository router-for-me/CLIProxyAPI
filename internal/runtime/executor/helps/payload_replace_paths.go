package helps

import (
	"strconv"
	"strings"
	"unsafe"

	"github.com/MoeForge/moego/ojson"
)

// payloadReplacePathStep retains a node's location within its immediate parent,
// or an array index. Concrete paths are materialized only for changed strings.
type payloadReplacePathStep struct {
	parent string
	value  ojson.Result
	index  int
	path   string
}

// visitPayloadReplacePath keeps query results attached to their selected parent.
// Suffixes are read relative to each selected node, never from the document root.
func visitPayloadReplacePath(source ojson.Result, path string, visit func(ojson.Result, []payloadReplacePathStep)) {
	type selector struct {
		path, query            string
		prefix                 []string
		array, all, projection bool
	}
	parts := splitPayloadRulePath(path)
	var selectorBuf [4]selector
	selectors := selectorBuf[:0]
	start := 0
	wildcard := false
	for i, part := range parts {
		if part == "@this" {
			// Read identity suffixes separately so modifiers do not reset offsets.
			if i > start {
				selectors = append(selectors, selector{path: strings.Join(parts[start:i], ".")})
			}
			start = i + 1
			wildcard = false
			continue
		}
		projection := part == "#" && i < len(parts)-1
		query, all, ok := parsePayloadQueryPathPart(part)
		if !ok && !projection {
			wildcard = wildcard || payloadReplaceWildcardKey(part)
			continue
		}
		if ok && !all && query != "" && len(splitPayloadLogical(query, "||")) == 1 && len(splitPayloadLogical(query, "&&")) == 1 {
			// Keep native first-match queries with their prefix and suffix so Get
			// can select the first wildcard parent satisfying the query. Compound
			// predicates still need the payload-specific logical matcher below.
			continue
		}
		var prefix []string
		if wildcard {
			// Reuse the parsed prefix across every selected outer-array element.
			prefix = parts[start:i]
		}
		selectors = append(selectors, selector{path: strings.Join(parts[start:i], "."), query: query, prefix: prefix, array: true, all: all || projection, projection: projection})
		start = i + 1
		wildcard = false
	}
	selectors = append(selectors, selector{path: strings.Join(parts[start:], ".")})
	// One backing array retains lazily resolved parent paths across all children.
	steps := make([]payloadReplacePathStep, 0, 2*len(selectors))
	var walk func(ojson.Result, int, []payloadReplacePathStep)
	walk = func(node ojson.Result, at int, steps []payloadReplacePathStep) {
		sel := selectors[at]
		if sel.path != "" {
			var selected ojson.Result
			if len(sel.prefix) > 0 {
				query := ""
				if !sel.all {
					query = sel.query
				}
				selected, _ = findPayloadReplaceArrayParent(node, sel.prefix, query)
			} else {
				selected = node.Get(sel.path)
			}
			if !payloadReplaceSourceChild(node, selected) {
				return
			}
			relative := selected
			relative.Index -= node.Index
			steps = append(steps, payloadReplacePathStep{parent: node.Raw, value: relative, index: -1})
			node = selected
		}
		if !sel.array {
			if at == len(selectors)-1 {
				visit(node, steps)
			} else {
				walk(node, at+1, steps)
			}
			return
		}
		if !node.IsArray() {
			return
		}
		node.ForEach(func(index, item ojson.Result) bool {
			if !sel.projection && !payloadQueryMatches(item, sel.query) {
				return true
			}
			childSteps := append(steps, payloadReplacePathStep{index: int(index.Int())})
			walk(item, at+1, childSteps)
			// First-match queries stop even if their selected item has no suffix.
			return sel.all
		})
	}
	walk(source, 0, steps)
}

// payloadReplaceWildcardKey recognizes only object-key glob components, not
// queries, modifiers, literals, multipaths or pipes containing wildcard bytes.
func payloadReplaceWildcardKey(part string) bool {
	if part == "" || strings.ContainsAny(part[:1], "@![{") || strings.HasPrefix(part, "#(") {
		return false
	}
	wildcard := false
	for i := 0; i < len(part); i++ {
		switch part[i] {
		case '\\':
			i++
		case '|':
			return false
		case '*', '?':
			wildcard = true
		}
	}
	return wildcard
}

// payloadReplaceSourceChild checks provenance in constant time without comparing
// payload bytes. Equal text at a coincident offset is not proof of ownership.
func payloadReplaceSourceChild(parent, child ojson.Result) bool {
	offset := child.Index - parent.Index
	return child.Raw != "" && offset >= 0 && offset <= len(parent.Raw)-len(child.Raw) &&
		unsafe.StringData(child.Raw) == unsafe.StringData(parent.Raw[offset:])
}

// findPayloadReplaceArrayParent includes the trailing array and optional first-
// match predicate when selecting wildcard parents. matched also commits an
// earlier first-match query even when its remaining suffix cannot resolve.
func findPayloadReplaceArrayParent(node ojson.Result, parts []string, query string) (array ojson.Result, matched bool) {
	if len(parts) == 0 {
		if node.IsArray() {
			matched = query == ""
			if !matched {
				node.ForEach(func(_, item ojson.Result) bool {
					matched = payloadQueryMatches(item, query)
					return !matched
				})
			}
			if matched {
				return node, true
			}
		}
		return ojson.Result{}, false
	}
	if !node.IsObject() && !node.IsArray() {
		return ojson.Result{}, false
	}
	part := parts[0]
	if node.IsObject() && payloadReplaceWildcardKey(part) {
		node.ForEach(func(key, item ojson.Result) bool {
			// A single-key view delegates wildcard and escape semantics to ojson.
			if !ojson.Get("{"+key.Raw+":null}", part).Exists() {
				return true
			}
			array, matched = findPayloadReplaceArrayParent(item, parts[1:], query)
			return !matched
		})
		return array, matched
	}
	selected := node.Get(part)
	if !payloadReplaceSourceChild(node, selected) {
		return ojson.Result{}, false
	}
	array, matched = findPayloadReplaceArrayParent(selected, parts[1:], query)
	if node.IsArray() {
		if _, all, isQuery := parsePayloadQueryPathPart(part); isQuery && !all {
			matched = true
		}
	}
	return array, matched
}

func payloadReplaceWritePath(steps []payloadReplacePathStep) string {
	var path strings.Builder
	for i := range steps {
		step := &steps[i]
		if step.path == "" {
			if step.index >= 0 {
				step.path = strconv.Itoa(step.index)
			} else {
				// Path resolves only within this parent, not the entire document.
				// Colons also need escaping because writes interpret leading colons.
				step.path = strings.ReplaceAll(step.value.Path(step.parent), ":", `\:`)
			}
		}
		if step.path == "" {
			// Computed values without a source node are not editable fields.
			return ""
		}
		if step.path == "@this" {
			// The node is its parent, not a child field named @this.
			continue
		}
		if path.Len() > 0 {
			path.WriteByte('.')
		}
		path.WriteString(step.path)
	}
	return path.String()
}
