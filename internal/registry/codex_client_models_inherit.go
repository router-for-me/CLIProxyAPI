package registry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CodexClientModelsInheritKeyword is the reserved key inside a model override
// patch that points fields at another model configuration, for example:
//
//	{
//	  "$inherit": "gpt-5.6-sol",
//	  "display_name": "My Fast Sol"
//	}
//
// A string value is the shorthand for inheriting the whole entry. An object value
// maps a dotted field path to the model that supplies it, and the empty path ""
// inherits the whole entry from a specific model:
//
//	{
//	  "$inherit": {
//	    "": "my-fast-sol",
//	    "model_messages.guardian_v2": "gpt-6-astra"
//	  }
//	}
//
// Sources are applied shallow to deep, the local patch is applied last over the
// inherited values, and a null local value still removes the field. Model identity
// fields (slug, display_name, description) are never taken from a source.
const CodexClientModelsInheritKeyword = "$inherit"

// maxCodexClientModelsInheritDepth bounds how many inheritance hops may be used to
// resolve a single field.
const maxCodexClientModelsInheritDepth = 3

// codexClientModelsIdentityFields describe a model itself. Inheritance sources never
// supply them, so a model keeps its own name and description.
var codexClientModelsIdentityFields = []string{"slug", "display_name", "description"}

// CodexClientModelsOverrideIssue reports one override entry the catalog could not
// apply. The entry keeps its base value, or stays out of the catalog when it is a
// custom model.
type CodexClientModelsOverrideIssue struct {
	Slug  string `json:"slug"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error"`
}

func (i CodexClientModelsOverrideIssue) String() string {
	if i.Path == "" {
		return fmt.Sprintf("model %q: %s", i.Slug, i.Error)
	}
	return fmt.Sprintf("model %q field %q: %s", i.Slug, i.Path, i.Error)
}

// codexClientModelsInheritSource is one inheritance directive: the value at path
// inside slug is used for the same path of the model declaring the directive.
type codexClientModelsInheritSource struct {
	path     string
	segments []string
	slug     string
}

// splitCodexClientModelsInherit separates the inheritance directive from the local
// patch of one override entry.
func splitCodexClientModelsInherit(patch map[string]any) ([]codexClientModelsInheritSource, map[string]any, error) {
	local := make(map[string]any, len(patch))
	for key, value := range patch {
		if key == CodexClientModelsInheritKeyword {
			continue
		}
		local[key] = value
	}

	raw, hasDirective := patch[CodexClientModelsInheritKeyword]
	if !hasDirective || raw == nil {
		return nil, local, nil
	}

	directives := make(map[string]string, 1)
	switch typed := raw.(type) {
	case string:
		slug := strings.TrimSpace(typed)
		if slug == "" {
			return nil, nil, fmt.Errorf("field %q must name a model", CodexClientModelsInheritKeyword)
		}
		directives[""] = slug
	case map[string]any:
		for inheritPath, value := range typed {
			slug, ok := value.(string)
			if !ok {
				return nil, nil, fmt.Errorf("field %q path %q must name a model", CodexClientModelsInheritKeyword, inheritPath)
			}
			slug = strings.TrimSpace(slug)
			if slug == "" {
				return nil, nil, fmt.Errorf("field %q path %q must name a model", CodexClientModelsInheritKeyword, inheritPath)
			}
			directives[inheritPath] = slug
		}
	default:
		return nil, nil, fmt.Errorf("field %q must be a model slug or an object of field paths", CodexClientModelsInheritKeyword)
	}

	sources := make([]codexClientModelsInheritSource, 0, len(directives))
	for inheritPath, slug := range directives {
		segments, errPath := parseCodexClientModelsInheritPath(inheritPath)
		if errPath != nil {
			return nil, nil, errPath
		}
		sources = append(sources, codexClientModelsInheritSource{path: inheritPath, segments: segments, slug: slug})
	}
	// Shallow paths first: a deeper directive refines a subtree a shallower one
	// already replaced, and the local patch still wins over all of them.
	sort.Slice(sources, func(i, j int) bool {
		if len(sources[i].segments) != len(sources[j].segments) {
			return len(sources[i].segments) < len(sources[j].segments)
		}
		return sources[i].path < sources[j].path
	})
	return sources, local, nil
}

// parseCodexClientModelsInheritPath splits a dotted inheritance path. It rejects
// array indices, empty segments and the model identity fields.
func parseCodexClientModelsInheritPath(inheritPath string) ([]string, error) {
	if inheritPath == "" {
		return nil, nil
	}
	if strings.ContainsAny(inheritPath, "[]") {
		return nil, fmt.Errorf("inheritance path %q must not use array indices", inheritPath)
	}
	segments := strings.Split(inheritPath, ".")
	for _, segment := range segments {
		if segment == "" {
			return nil, fmt.Errorf("inheritance path %q has an empty segment", inheritPath)
		}
		if segment == CodexClientModelsInheritKeyword {
			return nil, fmt.Errorf("inheritance path %q must not target %s", inheritPath, CodexClientModelsInheritKeyword)
		}
	}
	if len(segments) == 1 {
		for _, field := range codexClientModelsIdentityFields {
			if segments[0] == field {
				return nil, fmt.Errorf("field %q cannot be inherited", field)
			}
		}
	}
	return segments, nil
}

// codexClientModelsInheritance resolves override entries that may inherit fields
// from other models. Results are memoised per (slug, path) pair, which also keys
// cycle detection: A.field1 <- B and B.field2 <- A stays legal, while resolving the
// same pair twice is a cycle.
type codexClientModelsInheritance struct {
	base    map[string]map[string]any
	local   map[string]map[string]any
	sources map[string][]codexClientModelsInheritSource
	memo    map[string]codexClientModelsInheritValue
	active  map[string]bool
}

type codexClientModelsInheritValue struct {
	value any
	found bool
	err   error
}

func newCodexClientModelsInheritance(
	base map[string]map[string]any,
	local map[string]map[string]any,
	sources map[string][]codexClientModelsInheritSource,
) *codexClientModelsInheritance {
	return &codexClientModelsInheritance{
		base:    base,
		local:   local,
		sources: sources,
		memo:    make(map[string]codexClientModelsInheritValue),
		active:  make(map[string]bool),
	}
}

// entry returns the fully resolved configuration of one model.
func (r *codexClientModelsInheritance) entry(slug string) (map[string]any, error) {
	value, found, errValue := r.value(slug, "")
	if errValue != nil {
		return nil, errValue
	}
	if !found {
		return nil, fmt.Errorf("model %q has no configuration", slug)
	}
	entry, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("model %q configuration must be a JSON object", slug)
	}
	return entry, nil
}

func (r *codexClientModelsInheritance) value(slug, fieldPath string) (any, bool, error) {
	key := slug + "\x00" + fieldPath
	if cached, ok := r.memo[key]; ok {
		return cached.value, cached.found, cached.err
	}
	if r.active[key] {
		return nil, false, fmt.Errorf("inheritance cycle detected at model %q field %s", slug, describeCodexClientModelsPath(fieldPath))
	}
	r.active[key] = true
	value, found, errResolve := r.resolve(slug, fieldPath)
	delete(r.active, key)
	r.memo[key] = codexClientModelsInheritValue{value: value, found: found, err: errResolve}
	return value, found, errResolve
}

func (r *codexClientModelsInheritance) resolve(slug, fieldPath string) (any, bool, error) {
	segments := splitCodexClientModelsPath(fieldPath)
	localPatch := r.local[slug]

	var current any
	found := false
	if baseEntry, ok := r.base[slug]; ok {
		current, found = lookupCodexClientModelsPath(baseEntry, segments)
		current = cloneCodexClientModelsValue(current)
	}

	for _, source := range r.sources[slug] {
		if !codexClientModelsPathsOverlap(segments, source.segments) {
			continue
		}
		if codexClientModelsPathHasPrefix(segments, source.segments) {
			// The source replaces this path or an ancestor of it, so the whole
			// subtree comes from the source.
			value, ok, errValue := r.value(source.slug, fieldPath)
			if errValue != nil {
				return nil, false, fmt.Errorf("inherited from model %q: %w", source.slug, errValue)
			}
			if !ok {
				return nil, false, fmt.Errorf("source model %q does not define %s", source.slug, describeCodexClientModelsPath(fieldPath))
			}
			current, found = cloneCodexClientModelsValue(value), true
			continue
		}

		// The source writes inside the requested subtree.
		value, ok, errValue := r.value(source.slug, source.path)
		if errValue != nil {
			return nil, false, fmt.Errorf("inherited from model %q: %w", source.slug, errValue)
		}
		if !ok {
			return nil, false, fmt.Errorf("source model %q does not define %s", source.slug, describeCodexClientModelsPath(source.path))
		}
		object, okObject := current.(map[string]any)
		if !found || !okObject {
			object = make(map[string]any, 1)
		}
		relative := codexClientModelsPathSuffix(source.segments, segments)
		setCodexClientModelsPath(object, relative, cloneCodexClientModelsValue(value))
		current, found = object, true
	}

	// A local patch that deletes or replaces an ancestor with something that cannot
	// contain this path removes the whole subtree, so neither base nor inherited
	// values below it may survive. The exact-path lookup below cannot see such an
	// ancestor patch on its own.
	if codexClientModelsPatchBlocksPath(localPatch, segments) {
		current, found = nil, false
	}

	if localValue, okLocal := lookupCodexClientModelsPath(localPatch, segments); okLocal {
		switch {
		case localValue == nil:
			current, found = nil, false
		default:
			if object, okObject := current.(map[string]any); okObject {
				if patchObject, okPatch := localValue.(map[string]any); okPatch {
					current, found = mergeCodexClientModelsPatch(object, patchObject), true
					break
				}
			}
			current, found = cloneCodexClientModelsValue(localValue), true
		}
	}

	if len(segments) == 0 {
		entry, okEntry := current.(map[string]any)
		if !okEntry {
			entry = make(map[string]any, 3)
		}
		for _, field := range codexClientModelsIdentityFields {
			if baseEntry, ok := r.base[slug]; ok {
				if value, exists := baseEntry[field]; exists {
					entry[field] = cloneCodexClientModelsValue(value)
					continue
				}
			}
			delete(entry, field)
		}
		for _, field := range codexClientModelsIdentityFields {
			value, exists := localPatch[field]
			if !exists {
				continue
			}
			if value == nil {
				delete(entry, field)
				continue
			}
			entry[field] = cloneCodexClientModelsValue(value)
		}
		// The document key always wins: an override can never rename a model.
		entry["slug"] = slug
		current, found = entry, true
	}

	return current, found, nil
}

func describeCodexClientModelsPath(fieldPath string) string {
	if fieldPath == "" {
		return "the whole entry"
	}
	return fmt.Sprintf("%q", fieldPath)
}

func splitCodexClientModelsPath(fieldPath string) []string {
	if fieldPath == "" {
		return nil
	}
	return strings.Split(fieldPath, ".")
}

func codexClientModelsPathHasPrefix(fieldPath, prefix []string) bool {
	if len(prefix) > len(fieldPath) {
		return false
	}
	for i, segment := range prefix {
		if fieldPath[i] != segment {
			return false
		}
	}
	return true
}

func codexClientModelsPathsOverlap(a, b []string) bool {
	return codexClientModelsPathHasPrefix(a, b) || codexClientModelsPathHasPrefix(b, a)
}

// codexClientModelsPatchBlocksPath reports whether the patch deletes or replaces an
// ancestor of segments with a value that cannot contain the path: an explicit null
// deletion or a non-object value. Both remove everything below that ancestor.
func codexClientModelsPatchBlocksPath(patch map[string]any, segments []string) bool {
	var current any = patch
	for index := 0; index < len(segments)-1; index++ {
		object, okObject := current.(map[string]any)
		if !okObject {
			return false
		}
		value, exists := object[segments[index]]
		if !exists {
			return false
		}
		if value == nil {
			return true
		}
		if _, okValue := value.(map[string]any); !okValue {
			return true
		}
		current = value
	}
	return false
}

func codexClientModelsPathSuffix(fieldPath, prefix []string) []string {
	return fieldPath[len(prefix):]
}

func lookupCodexClientModelsPath(document map[string]any, segments []string) (any, bool) {
	var current any = document
	for _, segment := range segments {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, exists := object[segment]
		if !exists {
			return nil, false
		}
		current = value
	}
	return current, true
}

func setCodexClientModelsPath(document map[string]any, segments []string, value any) {
	current := document
	for _, segment := range segments[:len(segments)-1] {
		child, ok := current[segment].(map[string]any)
		if !ok {
			child = make(map[string]any, 1)
			current[segment] = child
		}
		current = child
	}
	current[segments[len(segments)-1]] = value
}

func cloneCodexClientModelsValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = cloneCodexClientModelsValue(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneCodexClientModelsValue(item)
		}
		return cloned
	default:
		return value
	}
}

// codexClientModelsInheritFieldDepth returns the longest inheritance chain that
// resolving one field of slug can follow, counting one hop per source reference.
// It walks only the source references the resolver follows for that field, so a
// model that lends a different field to a different path does not add a hop. A
// cycle reports ok = false and is left to the resolver, which names the model and
// field it re-entered.
func codexClientModelsInheritFieldDepth(
	slug, fieldPath string,
	sources map[string][]codexClientModelsInheritSource,
	memo map[string]int,
	visiting map[string]bool,
) (int, bool) {
	key := slug + "\x00" + fieldPath
	if depth, ok := memo[key]; ok {
		return depth, true
	}
	if visiting[key] {
		return 0, false
	}
	visiting[key] = true
	defer delete(visiting, key)

	segments := splitCodexClientModelsPath(fieldPath)
	longest := 0
	resolved := true
	for _, source := range sources[slug] {
		if !codexClientModelsPathsOverlap(segments, source.segments) {
			continue
		}
		// The resolver continues with the requested path when the source replaces
		// it, and with the source's own path when the source writes inside it.
		next := fieldPath
		if !codexClientModelsPathHasPrefix(segments, source.segments) {
			next = source.path
		}
		child, okChild := codexClientModelsInheritFieldDepth(source.slug, next, sources, memo, visiting)
		if !okChild {
			resolved = false
			continue
		}
		if child+1 > longest {
			longest = child + 1
		}
	}
	if resolved {
		memo[key] = longest
	}
	return longest, resolved
}

// CodexClientModelsInheritUsage counts how many override entries use a model as an
// inheritance source, keyed by the source slug.
func CodexClientModelsInheritUsage(doc map[string]json.RawMessage) map[string]int {
	usage := make(map[string]int)
	for _, patch := range doc {
		var decoded map[string]any
		if errUnmarshal := json.Unmarshal(patch, &decoded); errUnmarshal != nil {
			continue
		}
		sources, _, errSplit := splitCodexClientModelsInherit(decoded)
		if errSplit != nil {
			continue
		}
		seen := make(map[string]struct{}, len(sources))
		for _, source := range sources {
			if _, ok := seen[source.slug]; ok {
				continue
			}
			seen[source.slug] = struct{}{}
			usage[source.slug]++
		}
	}
	return usage
}
