package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/MoeForge/moego/json"
)

// rejectDuplicateCatalogKeys fails when body contains an object with a repeated
// member name. A restricted catalog is accepted or rejected on one occurrence,
// while JSON clients differ on which duplicate they keep, so an ambiguous
// catalog can hand a denied entry to a restricted caller. Duplicate "data" or
// "models" members are ambiguous for the same reason.
func rejectDuplicateCatalogKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	type frame struct {
		object bool
		keys   map[string]struct{}
	}
	var stack []frame
	expectKey := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				stack = append(stack, frame{object: true, keys: make(map[string]struct{}, 4)})
				expectKey = true
			case '[':
				stack = append(stack, frame{})
			case '}', ']':
				if len(stack) == 0 {
					return fmt.Errorf("unexpected %q", delim)
				}
				stack = stack[:len(stack)-1]
				// The container was a value; the next token of an object is a key.
				expectKey = len(stack) > 0 && stack[len(stack)-1].object
			}
			continue
		}
		if len(stack) == 0 {
			continue
		}
		top := &stack[len(stack)-1]
		if !top.object {
			continue
		}
		if !expectKey {
			expectKey = true
			continue
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("unexpected object key %v", token)
		}
		if _, duplicate := top.keys[key]; duplicate {
			return fmt.Errorf("duplicate object key %q", key)
		}
		top.keys[key] = struct{}{}
		expectKey = false
	}
}
