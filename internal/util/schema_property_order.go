package util

import (
	"slices"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

type propertyOrders struct {
	byNames   map[string][]string
	ambiguous map[string]bool
}

func restorePropertyOrder(original, resolved string) string {
	orders := propertyOrders{byNames: make(map[string][]string), ambiguous: make(map[string]bool)}
	collectPropertyOrders(ojson.Parse(original), false, orders)
	if len(orders.byNames) == 0 {
		return resolved
	}
	out, changed := reorderSchemaValue(ojson.Parse(resolved), false, false, orders)
	if !changed {
		return resolved
	}
	return out
}

func collectPropertyOrders(value ojson.Result, inNameMap bool, orders propertyOrders) {
	forEachSchemaChild(value, inNameMap, func(name string, child ojson.Result, childInNameMap bool) {
		if name == "properties" && !inNameMap && child.IsObject() {
			orders.record(objectKeys(child))
		}
		collectPropertyOrders(child, childInNameMap, orders)
	})
}

func (o propertyOrders) record(keys []string) {
	names := propertySignature(keys)
	if known, seen := o.byNames[names]; seen && !slices.Equal(known, keys) {
		o.ambiguous[names] = true
		return
	}
	o.byNames[names] = keys
}

func (o propertyOrders) lookup(keys []string) ([]string, bool) {
	names := propertySignature(keys)
	want, known := o.byNames[names]
	if !known || o.ambiguous[names] {
		return nil, false
	}
	return want, true
}

func reorderSchemaValue(value ojson.Result, inNameMap, isProperties bool, orders propertyOrders) (string, bool) {
	if value.IsArray() {
		return reorderSchemaArray(value, orders)
	}
	if !value.IsObject() {
		return value.Raw, false
	}

	keys := objectKeys(value)
	members := make(map[string]string, len(keys))
	changed := false
	value.ForEach(func(key, child ojson.Result) bool {
		name := key.String()
		members[name] = child.Raw
		if !inNameMap && isLiteralValueKeyword(name) {
			return true
		}
		_, childInNameMap := schemaNameMapKeywords[name]
		raw, childChanged := reorderSchemaValue(child, !inNameMap && childInNameMap, !inNameMap && name == "properties", orders)
		if childChanged {
			members[name] = raw
			changed = true
		}
		return true
	})

	order := keys
	if isProperties {
		if want, known := orders.lookup(keys); known && !slices.Equal(want, keys) {
			order = want
			changed = true
		}
	}
	if !changed {
		return value.Raw, false
	}

	doc := ojson.NewDocString("{}")
	for _, name := range order {
		if err := doc.SetRaw(objectKeyWritePath(name), members[name]); err != nil {
			return value.Raw, false
		}
	}
	return doc.String(), true
}

func reorderSchemaArray(value ojson.Result, orders propertyOrders) (string, bool) {
	var items []string
	changed := false
	value.ForEach(func(_, item ojson.Result) bool {
		raw, itemChanged := reorderSchemaValue(item, false, false, orders)
		items = append(items, raw)
		changed = changed || itemChanged
		return true
	})
	if !changed {
		return value.Raw, false
	}

	doc := ojson.NewDocString("[]")
	for _, raw := range items {
		if err := doc.SetRaw("-1", raw); err != nil {
			return value.Raw, false
		}
	}
	return doc.String(), true
}

func forEachSchemaChild(value ojson.Result, inNameMap bool, visit func(name string, child ojson.Result, childInNameMap bool)) {
	if value.IsArray() {
		value.ForEach(func(_, item ojson.Result) bool {
			visit("", item, false)
			return true
		})
		return
	}
	if !value.IsObject() {
		return
	}
	value.ForEach(func(key, child ojson.Result) bool {
		name := key.String()
		if inNameMap {
			visit(name, child, false)
			return true
		}
		if isLiteralValueKeyword(name) {
			return true
		}
		_, childInNameMap := schemaNameMapKeywords[name]
		visit(name, child, childInNameMap)
		return true
	})
}

func objectKeyWritePath(key string) string {
	if key == "" {
		return ":"
	}
	return strings.ReplaceAll(ojson.Escape(key), ":", `\:`)
}

func objectKeys(object ojson.Result) []string {
	var keys []string
	object.ForEach(func(key, _ ojson.Result) bool {
		keys = append(keys, key.String())
		return true
	})
	return keys
}

func propertySignature(keys []string) string {
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	return strings.Join(sorted, "\x00")
}
