package responsestools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Item id namespaces used by the Responses protocol. A strict upstream derives
// the expected namespace from the item type, so every item whose type this
// package changes must move its id with it. A tool_search_call that still
// carries an fc_ id is accepted in the response that produced it and rejected
// on the next request, because clients replay output items verbatim.
//
// The prefixes cover the six tool item types only. Non-tool items such as
// message and reasoning never take part in a proxy identity conversion: the
// proxy has no reason to re-identify them, and guessing would rewrite upstream
// state it does not own.
const (
	itemIDFunctionCall         = "fc_"
	itemIDFunctionCallOutput   = "fco_"
	itemIDCustomToolCall       = "ctc_"
	itemIDCustomToolCallOutput = "ctco_"
	itemIDToolSearchCall       = "tsc_"
	itemIDToolSearchOutput     = "tso_"
)

// itemIDTypeOwners maps every known id prefix back to the item type that owns
// it. Prefixes keep their trailing underscore, so fco_ is never read as fc_.
var itemIDTypeOwners = []struct {
	prefix   string
	itemType string
}{
	{itemIDFunctionCallOutput, "function_call_output"},
	{itemIDCustomToolCallOutput, "custom_tool_call_output"},
	{itemIDToolSearchOutput, "tool_search_output"},
	{itemIDFunctionCall, "function_call"},
	{itemIDCustomToolCall, "custom_tool_call"},
	{itemIDToolSearchCall, "tool_search_call"},
}

// toolIDPeers lists the item types one tool item type may legally become when
// this package re-identifies it. Anything outside these pairs is never
// converted in either direction.
var toolIDPeers = map[string][]string{
	"function_call":           {"custom_tool_call", "tool_search_call"},
	"custom_tool_call":        {"function_call"},
	"tool_search_call":        {"function_call"},
	"function_call_output":    {"custom_tool_call_output", "tool_search_output"},
	"custom_tool_call_output": {"function_call_output"},
	"tool_search_output":      {"function_call_output"},
}

// errItemIDConflict is returned when two different items would carry the same
// client-visible id. The message deliberately names no id so request bodies
// and upstream identities never reach logs or error frames.
var errItemIDConflict = errors.New("two items would share one client item id")

// ItemIDPrefixForType returns the id namespace one Responses tool item type
// must use. Non-tool and unknown types report an empty prefix and keep
// whatever id the upstream chose.
func ItemIDPrefixForType(itemType string) string {
	for _, owner := range itemIDTypeOwners {
		if owner.itemType == strings.TrimSpace(itemType) {
			return owner.prefix
		}
	}
	return ""
}

// itemIDOwnerType returns the item type whose namespace id carries, or an
// empty string when the id uses no known namespace.
func itemIDOwnerType(id string) string {
	for _, owner := range itemIDTypeOwners {
		if strings.HasPrefix(id, owner.prefix) {
			return owner.itemType
		}
	}
	return ""
}

// toolIDTransitionAllowed reports whether fromType may become toType.
func toolIDTransitionAllowed(fromType, toType string) bool {
	for _, peer := range toolIDPeers[strings.TrimSpace(fromType)] {
		if peer == strings.TrimSpace(toType) {
			return true
		}
	}
	return false
}

// ConvertedItemID moves one id into the namespace its target item type
// requires. An id that already uses no known namespace keeps its value: an
// upstream may use any id shape, and only an explicit type conversion mints a
// new one. The mapping is pure, so the same id always normalizes to the same
// value across the added, done, and completed events of one response.
func ConvertedItemID(fromType, toType, id string) (string, error) {
	fromType = strings.TrimSpace(fromType)
	toType = strings.TrimSpace(toType)
	if fromType == toType {
		return id, nil
	}
	if !toolIDTransitionAllowed(fromType, toType) {
		return "", fmt.Errorf("item type %q cannot be converted to %q", fromType, toType)
	}
	if id == "" {
		return "", nil
	}
	want := ItemIDPrefixForType(toType)
	if want == "" {
		return "", fmt.Errorf("item type %q has no id namespace", toType)
	}
	if strings.HasPrefix(id, want) {
		return id, nil
	}
	owner := itemIDOwnerType(id)
	if owner == fromType {
		return want + strings.TrimPrefix(id, ItemIDPrefixForType(fromType)), nil
	}
	if owner != "" {
		return "", fmt.Errorf("item id namespace %q does not match item type %q",
			ItemIDPrefixForType(owner), fromType)
	}
	// An unknown id keeps its full identity: the deterministic digest covers
	// the source type and the original value only, never the route,
	// credential, output index, or call id.
	sum := sha256.Sum256([]byte(fromType + "\x00" + id))
	return want + "proxy_" + hex.EncodeToString(sum[:]), nil
}

// ReidentifyItem moves one item to toType together with its id namespace. The
// item is left untouched when the conversion is not allowed or the id cannot
// be migrated, so a failed conversion never emits a half-rewritten item.
func ReidentifyItem(item map[string]any, toType string) (bool, error) {
	if item == nil {
		return false, nil
	}
	fromType := stringField(item, "type")
	rawID, hasID := item["id"]
	if hasID {
		if _, isText := rawID.(string); !isText {
			return false, fmt.Errorf("item id is not a string")
		}
	}
	toType = strings.TrimSpace(toType)
	if fromType == toType {
		return false, nil
	}
	converted, err := ConvertedItemID(fromType, toType, stringField(item, "id"))
	if err != nil {
		return false, err
	}
	if hasID {
		item["id"] = converted
	}
	item["type"] = toType
	return true, nil
}

// ItemIDOwner is the upstream identity behind one client-visible id. WireID
// and WireType are stable for the whole response; CallID is a consistency
// field that some events omit and others supply late.
type ItemIDOwner struct {
	WireID   string
	WireType string
	CallID   string
}

// ItemIDRegistry rejects two different upstream items that would reach the
// client under the same id. The zero value is ready to use.
type ItemIDRegistry struct {
	owners map[string]ItemIDOwner
}

// Register claims one client id for one upstream item. Repeating the same
// owner is the normal added/done/completed pattern and is allowed; a repeated
// non-empty call_id that disagrees with the stored one is not.
func (r *ItemIDRegistry) Register(clientID string, owner ItemIDOwner) error {
	if r == nil || clientID == "" {
		return nil
	}
	if r.owners == nil {
		r.owners = make(map[string]ItemIDOwner)
	}
	existing, exists := r.owners[clientID]
	if !exists {
		r.owners[clientID] = owner
		return nil
	}
	if existing.WireID != owner.WireID || existing.WireType != owner.WireType {
		return errItemIDConflict
	}
	switch {
	case existing.CallID == "" && owner.CallID != "":
		existing.CallID = owner.CallID
		r.owners[clientID] = existing
	case existing.CallID != "" && owner.CallID != "" && existing.CallID != owner.CallID:
		return errItemIDConflict
	}
	return nil
}
