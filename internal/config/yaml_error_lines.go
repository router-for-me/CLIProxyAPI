package config

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var yamlTypeErrorEntry = regexp.MustCompile(`(?s)^line (\d+): (.*)$`)

var yamlUnknownField = regexp.MustCompile(`^field (.+) not found(?: in type \S+)?$`)

var yamlCollectionTypeError = regexp.MustCompile(`^cannot unmarshal !!(map|seq) `)

// yamlLocation is a node's dotted path and the node holding its position: the
// key, or its value when the key was generated and has no position.
type yamlLocation struct {
	path string
	at   *yaml.Node
}

type yamlVisit struct {
	rendered, node *yaml.Node
	// For mapping keys, the value in rendered and in tree.
	renderedValue, value *yaml.Node
	path                 string
}

// walkYAMLTwins visits rendered and tree in document order, skipping subtrees
// whose shapes differ.
func walkYAMLTwins(visit yamlVisit, report func(yamlVisit)) {
	rendered, tree := visit.rendered, visit.node
	if rendered == nil || tree == nil || rendered.Kind != tree.Kind || len(rendered.Content) != len(tree.Content) {
		return
	}
	report(visit)
	for i := range rendered.Content {
		child := yamlVisit{rendered: rendered.Content[i], node: tree.Content[i], path: visit.path}
		switch tree.Kind {
		case yaml.MappingNode:
			child.path = joinYAMLPath(visit.path, tree.Content[i-i%2].Value)
			if i%2 == 0 {
				child.renderedValue, child.value = rendered.Content[i+1], tree.Content[i+1]
			}
		case yaml.SequenceNode:
			child.path = visit.path + "[" + strconv.Itoa(i) + "]"
		}
		walkYAMLTwins(child, report)
	}
}

func joinYAMLPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// locateYAMLProblem finds the node of tree behind a yaml.TypeError entry at line
// of rendered, the re-parsed yaml.Marshal(tree). Without a column, a collection
// error names the innermost collection of that kind on the line, which is wrong
// for same-kind collections nested on one line.
func locateYAMLProblem(rendered, tree *yaml.Node, line int, problem string) *yamlLocation {
	find := func(match func(yamlVisit) bool, last bool) *yamlLocation {
		var found *yamlLocation
		walkYAMLTwins(yamlVisit{rendered: rendered, node: tree}, func(visit yamlVisit) {
			if !match(visit) || found != nil && !last {
				return
			}
			found = &yamlLocation{path: visit.path, at: visit.node}
			if visit.value != nil && visit.node.Line == 0 {
				found.at = visit.value
			}
		})
		return found
	}
	onLine := func(visit yamlVisit) bool { return visit.rendered.Line == line }
	if field := yamlUnknownField.FindStringSubmatch(problem); field != nil {
		return find(func(visit yamlVisit) bool {
			return visit.value != nil && visit.rendered.Value == field[1] && (onLine(visit) || visit.renderedValue.Line == line)
		}, false)
	}
	if collection := yamlCollectionTypeError.FindStringSubmatch(problem); collection != nil {
		kind := yaml.MappingNode
		if collection[1] == "seq" {
			kind = yaml.SequenceNode
		}
		return find(func(visit yamlVisit) bool { return onLine(visit) && visit.rendered.Kind == kind }, true)
	}
	if found := find(func(visit yamlVisit) bool { return onLine(visit) && visit.rendered.Kind == yaml.ScalarNode }, false); found != nil {
		return found
	}
	return find(onLine, false)
}

// describeV8DecodeError restates the strict decode error of ValidateV8Config
// with input lines, taken from flat, the flattened tree that encoded renders.
// Flattening leaves only unknown keys in a v8 section, so each is reported at
// its own line instead of at the section.
func describeV8DecodeError(err error, encoded []byte, flat *yaml.Node) error {
	var typeErr *yaml.TypeError
	var rendered yaml.Node
	if !errors.As(err, &typeErr) || yaml.Unmarshal(encoded, &rendered) != nil || len(rendered.Content) == 0 {
		return err
	}
	at := func(node *yaml.Node) string {
		if node == nil || node.Line == 0 {
			return ""
		}
		return "line " + strconv.Itoa(node.Line) + ": "
	}
	entries := make([]string, 0, len(typeErr.Errors))
	for _, entry := range typeErr.Errors {
		match := yamlTypeErrorEntry.FindStringSubmatch(entry)
		if match == nil {
			entries = append(entries, entry)
			continue
		}
		line, _ := strconv.Atoi(match[1])
		problem := match[2]
		found := locateYAMLProblem(rendered.Content[0], flat, line, problem)
		field := yamlUnknownField.FindStringSubmatch(problem)
		switch {
		case found == nil:
			entries = append(entries, entry)
		case field == nil:
			entries = append(entries, at(found.at)+problem)
		case !isV8Section(found.path):
			entries = append(entries, at(found.at)+"field "+field[1]+" not found")
		default:
			unknown := unknownV8Keys(yamlPath(flat, found.path), found.path)
			if len(unknown) == 0 {
				entries = append(entries, entry)
			}
			for _, key := range unknown {
				entries = append(entries, at(key.at)+"field "+key.name+" not found")
			}
		}
	}
	return &yaml.TypeError{Errors: entries}
}

type yamlUnknownKey struct {
	name string
	at   *yaml.Node
}

// unknownV8Keys returns the keys under node that are not v8 sections.
func unknownV8Keys(node *yaml.Node, path string) []yamlUnknownKey {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	var keys []yamlUnknownKey
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		child := joinYAMLPath(path, key.Value)
		if value.Kind == yaml.MappingNode && isV8Section(child) {
			keys = append(keys, unknownV8Keys(value, child)...)
			continue
		}
		at := key
		if at.Line == 0 {
			at = value
		}
		keys = append(keys, yamlUnknownKey{name: key.Value, at: at})
	}
	return keys
}

// isV8Section reports whether path is a proper prefix of a known v8 field path,
// including aliases and shared struct paths.
func isV8Section(path string) bool {
	prefix := path + "."
	for _, candidate := range v8Paths {
		if strings.HasPrefix(candidate.current, prefix) {
			return true
		}
	}
	for _, candidate := range append(append([]configPath(nil), v8Aliases...), v8SharedStructPaths...) {
		if strings.HasPrefix(candidate.current, prefix) || strings.HasPrefix(candidate.old, prefix) {
			return true
		}
	}
	return false
}
