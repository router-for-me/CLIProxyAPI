package chat_completions

import "github.com/tidwall/gjson"

type toolFamily uint8

const (
	toolFamilyFunction toolFamily = iota
	toolFamilyCustom
)

type toolCatalog struct {
	shortByOriginal map[string]string
	originalByShort map[string]string
	familiesByName  map[string]map[toolFamily]struct{}
}

func buildToolCatalog(rawJSON []byte) toolCatalog {
	catalog := toolCatalog{
		shortByOriginal: make(map[string]string),
		originalByShort: make(map[string]string),
		familiesByName:  make(map[string]map[toolFamily]struct{}),
	}

	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return catalog
	}

	var names []string
	seenNames := make(map[string]struct{})
	for _, tool := range tools.Array() {
		var name string
		var family toolFamily
		switch tool.Get("type").String() {
		case "function":
			name = tool.Get("function.name").String()
			family = toolFamilyFunction
		case "custom":
			name = tool.Get("name").String()
			family = toolFamilyCustom
		default:
			continue
		}
		if name == "" {
			continue
		}

		catalog.addFamily(name, family)
		if _, exists := seenNames[name]; !exists {
			seenNames[name] = struct{}{}
			names = append(names, name)
		}
	}

	catalog.shortByOriginal = buildShortNameMap(names)
	for original, short := range catalog.shortByOriginal {
		catalog.originalByShort[short] = original
		for family := range catalog.familiesByName[original] {
			catalog.addFamily(short, family)
		}
	}
	return catalog
}

func (catalog toolCatalog) addFamily(name string, family toolFamily) {
	families := catalog.familiesByName[name]
	if families == nil {
		families = make(map[toolFamily]struct{})
		catalog.familiesByName[name] = families
	}
	families[family] = struct{}{}
}

func (catalog toolCatalog) shorten(name string) string {
	if short, ok := catalog.shortByOriginal[name]; ok {
		return short
	}
	return shortenNameIfNeeded(name)
}

func (catalog toolCatalog) restore(name string) string {
	if original, ok := catalog.originalByShort[name]; ok {
		return original
	}
	return name
}

func (catalog toolCatalog) familyForChatCall(name string) toolFamily {
	families := catalog.familiesByName[name]
	if len(families) != 1 {
		return toolFamilyFunction
	}
	if _, ok := families[toolFamilyCustom]; ok {
		return toolFamilyCustom
	}
	return toolFamilyFunction
}
