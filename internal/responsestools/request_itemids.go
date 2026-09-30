package responsestools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"
)

// inputIDChange is one planned id migration inside the top-level input array.
type inputIDChange struct {
	Index int
	NewID string
}

// itemIDPrefixLiterals lists every known namespace prefix as it can appear
// inside an unescaped raw request body. Unicode escapes fall back to parsing
// because they may encode a prefix without containing its literal bytes.
var itemIDPrefixLiterals = []string{
	itemIDFunctionCallOutput,
	itemIDCustomToolCallOutput,
	itemIDToolSearchOutput,
	itemIDFunctionCall,
	itemIDCustomToolCall,
	itemIDToolSearchCall,
}

// requestItemStructuralKeys are the root and item fields whose repetition would
// make the migration target ambiguous. Business text inside arguments is never
// inspected, so a repeated key there stays the client's own content.
var requestItemStructuralKeys = map[string]struct{}{
	"type": {}, "id": {}, "call_id": {}, "execution": {},
}

var requestRootStructuralKeys = map[string]struct{}{
	"input": {}, "previous_response_id": {}, "previous_item_id": {},
}

// RepairInputItemIDs moves replayed input item ids back into the namespace
// their own item type requires. A client stores output items exactly as the
// proxy emitted them, so an id minted for one type is replayed verbatim on the
// next turn and rejected by strict upstreams. The repair stays on the
// pass-through path: no attempt, no tool identity decision, and no byte outside
// the migrated id values changes.
func RepairInputItemIDs(body []byte) ([]byte, bool, error) {
	out, changed, _, err := repairInputItemIDs(body)
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

// repairInputItemIDs performs the repair once and also returns the decoded root
// so Prepare can continue from the same parse. The returned root already
// mirrors the patched bytes.
func repairInputItemIDs(body []byte) (out []byte, changed bool, root map[string]any, err error) {
	if !mayCarryMigratedItemID(body) {
		return body, false, nil, nil
	}
	value, ok := decodeValue(body)
	if !ok {
		// Malformed JSON keeps its existing validation entry point; this
		// helper never turns a syntax problem into an identity problem.
		return body, false, nil, nil
	}
	decoded, ok := value.(map[string]any)
	if !ok {
		return body, false, nil, nil
	}
	input, _ := decoded["input"].([]any)
	changes := planInputItemIDChanges(input)
	if len(changes) == 0 {
		return body, false, decoded, nil
	}
	if err := rejectAmbiguousItemStructure(body); err != nil {
		return nil, false, nil, err
	}
	if hasOpaqueResponseHistory(decoded) {
		// The proxy cannot prove which stored item an opaque reference points
		// at, so it refuses instead of guessing or dropping the parent link.
		return nil, false, nil, unprocessableError(ReasonOpaqueHistory,
			fmt.Errorf("tool item id repair cannot resolve opaque response or item history"))
	}
	planned := make(map[int]string, len(changes))
	for _, change := range changes {
		planned[change.Index] = change.NewID
	}
	if err := checkInputItemIDCollisions(input, planned); err != nil {
		return nil, false, nil, err
	}
	patched := bytes.Clone(body)
	for _, change := range changes {
		path := "input." + strconv.Itoa(change.Index) + ".id"
		updated, setErr := sjson.SetBytes(patched, path, change.NewID)
		if setErr != nil {
			// A half-migrated body would reach the upstream with two identities
			// for one conversation, so nothing is returned at all.
			return nil, false, nil, unprocessableError(ReasonHistoryLink,
				fmt.Errorf("input item %d id could not be migrated", change.Index))
		}
		patched = updated
	}
	// Keep the decoded view in step with the bytes Prepare continues to use.
	for index, newID := range planned {
		if item, okItem := input[index].(map[string]any); okItem {
			item["id"] = newID
		}
	}
	return patched, true, decoded, nil
}

// mayCarryMigratedItemID is the cheap gate in front of the JSON walk.
func mayCarryMigratedItemID(body []byte) bool {
	for _, prefix := range itemIDPrefixLiterals {
		if bytes.Contains(body, []byte(prefix)) {
			return true
		}
	}
	return bytes.Contains(body, []byte(`\u`))
}

// planInputItemIDChanges proposes one migration per direct input item whose
// current type and known id prefix form an allowed pair. Server-executed search,
// unknown ids, absent or non-string ids, and non-tool items are left alone: the
// proxy has no evidence that it owns those identities.
func planInputItemIDChanges(input []any) []inputIDChange {
	var changes []inputIDChange
	for index, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if IsServerExecutedItem(item) {
			continue
		}
		itemType := strings.TrimSpace(stringField(item, "type"))
		if ItemIDPrefixForType(itemType) == "" {
			continue
		}
		id, isText := item["id"].(string)
		if !isText || id == "" {
			continue
		}
		owner := itemIDOwnerType(id)
		if owner == "" || owner == itemType || !toolIDTransitionAllowed(owner, itemType) {
			continue
		}
		converted, err := ConvertedItemID(owner, itemType, id)
		if err != nil {
			continue
		}
		changes = append(changes, inputIDChange{Index: index, NewID: converted})
	}
	return changes
}

// checkInputItemIDCollisions reserves every final id, including the unchanged
// ones, so a migration can never land on an id another item already owns.
func checkInputItemIDCollisions(input []any, planned map[int]string) error {
	var registry ItemIDRegistry
	for index, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		id, isText := item["id"].(string)
		if !isText || id == "" {
			continue
		}
		final := id
		if migrated, isPlanned := planned[index]; isPlanned {
			final = migrated
		}
		owner := ItemIDOwner{
			WireID:   id,
			WireType: strings.TrimSpace(stringField(item, "type")),
			CallID:   strings.TrimSpace(stringField(item, "call_id")),
		}
		if err := registry.Register(final, owner); err != nil {
			return unprocessableError(ReasonAmbiguousIdentity,
				fmt.Errorf("input item %d would share one client item id with another item", index))
		}
	}
	return nil
}

// rejectAmbiguousItemStructure proves that the fields this repair reads occur
// exactly once. A map decode cannot show a repeated key, and a repeated input,
// type, or id would let the repair act on an item the upstream may never see.
func rejectAmbiguousItemStructure(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return syntaxError(fmt.Errorf("request body is not valid JSON"))
	}
	if delim, isDelim := token.(json.Delim); !isDelim || delim != '{' {
		return nil
	}
	seen := make(map[string]struct{}, len(requestRootStructuralKeys))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return syntaxError(fmt.Errorf("request body is not valid JSON"))
		}
		key, isText := keyToken.(string)
		if !isText {
			return syntaxError(fmt.Errorf("request body contains a non-string key"))
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return syntaxError(fmt.Errorf("request body is not valid JSON"))
		}
		if _, relevant := requestRootStructuralKeys[key]; relevant {
			if _, duplicate := seen[key]; duplicate {
				return syntaxError(fmt.Errorf("request body repeats the %q field", key))
			}
			seen[key] = struct{}{}
		}
		if key == "input" {
			if err := rejectAmbiguousInputItems(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectAmbiguousInputItems(raw json.RawMessage) error {
	items, ok := rawItemArray(raw)
	if !ok {
		// A string or absent input carries no item this repair can address.
		return nil
	}
	for index, item := range items {
		counts, err := jsonObjectKeyCounts(item)
		if err != nil {
			return nil
		}
		for key := range requestItemStructuralKeys {
			if counts[key] > 1 {
				return syntaxError(fmt.Errorf("input item %d repeats the %q field", index, key))
			}
		}
	}
	return nil
}

// rawItemArray splits a raw input value into its elements. A non-array value
// reports false so the caller keeps its existing behavior.
func rawItemArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, false
	}
	return items, true
}

func jsonObjectKeyCounts(raw json.RawMessage) (map[string]int, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim || delim != '{' {
		return nil, fmt.Errorf("value is not a JSON object")
	}
	counts := make(map[string]int)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, isText := keyToken.(string)
		if !isText {
			return nil, fmt.Errorf("object contains a non-string key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		counts[key]++
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return counts, nil
}
