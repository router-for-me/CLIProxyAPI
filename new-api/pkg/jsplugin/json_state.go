package jsplugin

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/grafana/sobek"
)

const (
	CapabilityJSONClone      = "json-clone@1"
	CapabilitySubmitSSEDelta = "submit-sse-delta@1"
	MaxJSONToolBytes         = 1 << 20
	maxJSONToolDepth         = 32
	maxJSONToolNodes         = 32768
	maxJSONChanges           = 256
)

// HasCapability describes host APIs independently of a plugin's mutable globals.
func HasCapability(name string) bool {
	return name == CapabilityJSONClone || name == CapabilitySubmitSSEDelta
}

// JSONState owns a request-local JSON result. Appended strings stay in Go and
// are materialized only when the stream finishes, independently of JS runtimes.
type JSONState struct {
	root   *jsonStateNode
	limit  int
	failed error
}

type jsonStateNode struct {
	scalar any
	object map[string]*jsonStateNode
	array  []*jsonStateNode
	text   *strings.Builder
	bytes  int
	nodes  int
}

type jsonStateBudget struct {
	ctx   context.Context
	bytes int
	nodes int
}

func (b *jsonStateBudget) spend(bytes, nodes int) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if bytes > b.bytes || nodes > b.nodes {
		return fmt.Errorf("JSON state exceeds size or node limit")
	}
	b.bytes -= bytes
	b.nodes -= nodes
	return nil
}

func newJSONStateNode(value any, depth int, budget *jsonStateBudget) (*jsonStateNode, error) {
	if depth > maxJSONToolDepth {
		return nil, fmt.Errorf("JSON state exceeds depth limit")
	}
	if js, ok := value.(sobek.Value); ok {
		if sobek.IsUndefined(js) {
			return nil, fmt.Errorf("undefined is not a JSON value")
		}
		if object, ok := js.(*sobek.Object); ok {
			switch object.ClassName() {
			case "Object":
				keys := object.Keys()
				if len(keys) > budget.nodes {
					return nil, fmt.Errorf("JSON object exceeds node limit")
				}
				fields := make(map[string]any, len(keys))
				for _, key := range keys {
					fields[key] = object.Get(key)
				}
				value = fields
			case "Array":
				length := object.Get("length").ToInteger()
				if length < 0 || length > int64(budget.nodes) {
					return nil, fmt.Errorf("JSON array exceeds node limit")
				}
				items := make([]any, int(length))
				for index := range items {
					item := object.Get(strconv.Itoa(index))
					if item == nil {
						return nil, fmt.Errorf("sparse arrays are not JSON values")
					}
					items[index] = item
				}
				value = items
			default:
				return nil, fmt.Errorf("json.clone accepts only plain objects, arrays and JSON scalars")
			}
		} else {
			value = js.Export()
		}
	}
	beforeBytes, beforeNodes := budget.bytes, budget.nodes
	if err := budget.spend(0, 1); err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case map[string]any:
		if typed == nil {
			value = nil
		}
	case []any:
		if typed == nil {
			value = nil
		}
	}
	node := &jsonStateNode{}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > budget.nodes {
			return nil, fmt.Errorf("JSON object exceeds node limit")
		}
		if err := budget.spend(2, 0); err != nil {
			return nil, err
		}
		node.object = make(map[string]*jsonStateNode, len(typed))
		for key, child := range typed {
			if !utf8.ValidString(key) || len(key) > budget.bytes {
				return nil, fmt.Errorf("invalid or oversized JSON object key")
			}
			encoded, err := common.Marshal(key)
			if err != nil {
				return nil, err
			}
			cost := len(encoded) + 1
			if len(node.object) != 0 {
				cost++
			}
			if err = budget.spend(cost, 0); err != nil {
				return nil, err
			}
			node.object[key], err = newJSONStateNode(child, depth+1, budget)
			if err != nil {
				return nil, err
			}
		}
	case []any:
		if len(typed) > budget.nodes {
			return nil, fmt.Errorf("JSON array exceeds node limit")
		}
		if err := budget.spend(2+max(0, len(typed)-1), 0); err != nil {
			return nil, err
		}
		node.array = make([]*jsonStateNode, 0, len(typed))
		for _, child := range typed {
			item, err := newJSONStateNode(child, depth+1, budget)
			if err != nil {
				return nil, err
			}
			node.array = append(node.array, item)
		}
	default:
		switch number := value.(type) {
		case int:
			value = float64(number)
		case int64:
			value = float64(number)
		}
		switch scalar := value.(type) {
		case nil, bool:
		case float64:
			if math.IsNaN(scalar) || math.IsInf(scalar, 0) {
				return nil, fmt.Errorf("JSON numbers must be finite")
			}
		case string:
			if len(scalar) > budget.bytes {
				return nil, fmt.Errorf("JSON string exceeds size limit")
			}
			if !utf8.ValidString(scalar) {
				value = string([]rune(scalar))
			}
		default:
			return nil, fmt.Errorf("value must contain only JSON objects, arrays and scalars")
		}
		encoded, err := common.Marshal(value)
		if err != nil {
			return nil, err
		}
		if err = budget.spend(len(encoded), 0); err != nil {
			return nil, err
		}
		node.scalar = value
	}
	node.bytes, node.nodes = beforeBytes-budget.bytes, beforeNodes-budget.nodes
	return node, nil
}

func NewJSONState(limit int) *JSONState {
	return &JSONState{root: &jsonStateNode{bytes: 4, nodes: 1}, limit: max(0, min(limit, MaxJSONToolBytes))}
}

// Apply accepts a bounded batch of set, append and appendText operations.
// Failure invalidates the stream: callers must discard the state rather than
// continue after an error. No partially updated result may be published.
func (s *JSONState) Apply(ctx context.Context, changes any) (err error) {
	if s.failed != nil {
		return s.failed
	}
	defer func() {
		if err != nil {
			s.failed = err
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	items, ok := changes.([]any)
	if !ok || len(items) > maxJSONChanges {
		return fmt.Errorf("changes must be an array of at most %d operations", maxJSONChanges)
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		change, ok := item.(map[string]any)
		if !ok || len(change) != 3 {
			return fmt.Errorf("each change must contain only op, path and value")
		}
		op, _ := change["op"].(string)
		path, ok := change["path"].([]any)
		value, hasValue := change["value"]
		if !ok || !hasValue || len(path) > maxJSONToolDepth {
			return fmt.Errorf("change requires a JSON value and a path of at most %d segments", maxJSONToolDepth)
		}
		if op != "set" && op != "append" && op != "appendText" {
			return fmt.Errorf("unsupported JSON change operation %q", op)
		}
		ancestors := make([]*jsonStateNode, 0, len(path))
		target := s.root
		var key string
		var index int
		for offset, segment := range path {
			if target == nil {
				return fmt.Errorf("JSON change parent does not exist")
			}
			ancestors = append(ancestors, target)
			switch {
			case target.object != nil:
				key, ok = segment.(string)
				if !ok || !utf8.ValidString(key) || len(key) > s.limit {
					return fmt.Errorf("JSON object paths require valid string keys")
				}
				target = target.object[key]
			case target.array != nil:
				var number float64
				switch typed := segment.(type) {
				case int:
					number = float64(typed)
				case int64:
					number = float64(typed)
				case float64:
					number = typed
				default:
					return fmt.Errorf("JSON array paths require integer indices")
				}
				if math.IsNaN(number) || number < 0 || number >= float64(len(target.array)) || math.Trunc(number) != number {
					return fmt.Errorf("JSON array index is out of range")
				}
				index = int(number)
				target = target.array[index]
			default:
				return fmt.Errorf("JSON change path traverses a scalar")
			}
			if target == nil && (offset != len(path)-1 || op != "set") {
				return fmt.Errorf("JSON change target does not exist")
			}
		}
		addedBytes, addedNodes := 0, 0
		switch op {
		case "set":
			oldBytes, oldNodes := 0, 0
			if target != nil {
				oldBytes, oldNodes = target.bytes, target.nodes
			} else {
				encodedKey, err := common.Marshal(key)
				if err != nil {
					return err
				}
				addedBytes = len(encodedKey) + 1
				if len(ancestors[len(ancestors)-1].object) != 0 {
					addedBytes++
				}
			}
			budget := &jsonStateBudget{ctx: ctx, bytes: s.limit - s.root.bytes + oldBytes - addedBytes, nodes: maxJSONToolNodes - s.root.nodes + oldNodes}
			next, err := newJSONStateNode(value, len(path), budget)
			if err != nil {
				return err
			}
			addedBytes += next.bytes - oldBytes
			addedNodes = next.nodes - oldNodes
			if len(ancestors) == 0 {
				s.root = next
			} else if parent := ancestors[len(ancestors)-1]; parent.object != nil {
				parent.object[key] = next
			} else {
				parent.array[index] = next
			}
		case "append":
			if target.array == nil {
				return fmt.Errorf("append requires an array target")
			}
			if len(target.array) != 0 {
				addedBytes = 1
			}
			budget := &jsonStateBudget{ctx: ctx, bytes: s.limit - s.root.bytes - addedBytes, nodes: maxJSONToolNodes - s.root.nodes}
			next, err := newJSONStateNode(value, len(path)+1, budget)
			if err != nil {
				return err
			}
			addedBytes += next.bytes
			addedNodes = next.nodes
			target.array = append(target.array, next)
			target.bytes += addedBytes
			target.nodes += addedNodes
		case "appendText":
			previous, isText := target.scalar.(string)
			text, ok := value.(string)
			if !isText || !ok || len(text) > s.limit-s.root.bytes {
				return fmt.Errorf("appendText requires strings within the state size limit")
			}
			if !utf8.ValidString(text) {
				text = string([]rune(text))
			}
			encoded, err := common.Marshal(text)
			if err != nil {
				return err
			}
			addedBytes = len(encoded) - 2
			if addedBytes > s.limit-s.root.bytes {
				return fmt.Errorf("JSON state exceeds size limit")
			}
			if text != "" {
				if target.text == nil {
					target.text = &strings.Builder{}
					target.text.WriteString(previous)
					target.scalar = ""
				}
				target.text.WriteString(text)
			}
			target.bytes += addedBytes
		}
		for _, ancestor := range ancestors {
			ancestor.bytes += addedBytes
			ancestor.nodes += addedNodes
		}
	}
	return nil
}

// Value returns the final ordinary JSON tree and verifies the incremental byte
// accounting against the configured codec before callers can persist it.
func (s *JSONState) Value() (any, error) {
	if s.failed != nil {
		return nil, s.failed
	}
	value := s.root.value()
	encoded, err := common.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded) != s.root.bytes || len(encoded) > s.limit {
		return nil, fmt.Errorf("JSON state encoded size does not match its bounded representation")
	}
	return value, nil
}

func (n *jsonStateNode) value() any {
	if n.object != nil {
		result := make(map[string]any, len(n.object))
		for key, child := range n.object {
			result[key] = child.value()
		}
		return result
	}
	if n.array != nil {
		result := make([]any, len(n.array))
		for index, child := range n.array {
			result[index] = child.value()
		}
		return result
	}
	if n.text != nil {
		return n.text.String()
	}
	return n.scalar
}

// Native JS containers are essential here: wrapping Go slices in ToValue would
// retain host-backed array mutation behavior instead of producing a JS clone.
func (n *jsonStateNode) jsValue(runtime *sobek.Runtime) (sobek.Value, error) {
	if n.object != nil {
		result := runtime.NewObject()
		for key, child := range n.object {
			value, err := child.jsValue(runtime)
			if err != nil {
				return nil, err
			}
			if err = result.DefineDataProperty(key, value, sobek.FLAG_TRUE, sobek.FLAG_TRUE, sobek.FLAG_TRUE); err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	if n.array != nil {
		items := make([]any, len(n.array))
		for index, child := range n.array {
			value, err := child.jsValue(runtime)
			if err != nil {
				return nil, err
			}
			items[index] = value
		}
		return runtime.NewArray(items...), nil
	}
	return runtime.ToValue(n.value()), nil
}
