package util

import "encoding/json"

const emptyClaudeToolInputSchema = `{"type":"object","properties":{}}`

// NormalizeClaudeToolInputSchema makes a JSON Schema compatible with Claude's
// requirement that a tool input schema is an object without root-level unions.
func NormalizeClaudeToolInputSchema(schema []byte) []byte {
	var root map[string]json.RawMessage
	if len(schema) == 0 || json.Unmarshal(schema, &root) != nil || root == nil {
		return []byte(emptyClaudeToolInputSchema)
	}

	properties := claudeSchemaObject(root["properties"])
	for _, unionName := range []string{"anyOf", "oneOf", "allOf"} {
		unionRaw, exists := root[unionName]
		if !exists {
			continue
		}
		delete(root, unionName)

		var branches []json.RawMessage
		if json.Unmarshal(unionRaw, &branches) != nil {
			continue
		}
		for _, branchRaw := range branches {
			var branch map[string]json.RawMessage
			if json.Unmarshal(branchRaw, &branch) != nil || !claudeSchemaCanBeObject(branch) {
				continue
			}
			for name, property := range claudeSchemaObject(branch["properties"]) {
				if _, exists = properties[name]; !exists {
					properties[name] = property
				}
			}
			if unionName == "allOf" {
				mergeClaudeSchemaRequired(root, branch["required"])
			}
		}
	}

	root["type"] = json.RawMessage(`"object"`)
	propertiesRaw, errMarshalProperties := json.Marshal(properties)
	if errMarshalProperties != nil {
		return []byte(emptyClaudeToolInputSchema)
	}
	root["properties"] = propertiesRaw

	normalized, errMarshalRoot := json.Marshal(root)
	if errMarshalRoot != nil {
		return []byte(emptyClaudeToolInputSchema)
	}
	return normalized
}

func claudeSchemaObject(raw json.RawMessage) map[string]json.RawMessage {
	object := make(map[string]json.RawMessage)
	if len(raw) == 0 {
		return object
	}
	if errUnmarshal := json.Unmarshal(raw, &object); errUnmarshal != nil || object == nil {
		return make(map[string]json.RawMessage)
	}
	return object
}

func claudeSchemaCanBeObject(schema map[string]json.RawMessage) bool {
	typeRaw, exists := schema["type"]
	if !exists {
		return true
	}

	var schemaType string
	if json.Unmarshal(typeRaw, &schemaType) == nil {
		return schemaType == "object"
	}

	var schemaTypes []string
	if json.Unmarshal(typeRaw, &schemaTypes) != nil {
		return false
	}
	for _, candidate := range schemaTypes {
		if candidate == "object" {
			return true
		}
	}
	return false
}

func mergeClaudeSchemaRequired(root map[string]json.RawMessage, branchRequired json.RawMessage) {
	var required []string
	if rootRequired, exists := root["required"]; exists {
		if errUnmarshal := json.Unmarshal(rootRequired, &required); errUnmarshal != nil {
			required = nil
		}
	}

	var branchNames []string
	if json.Unmarshal(branchRequired, &branchNames) != nil {
		return
	}

	seen := make(map[string]struct{}, len(required)+len(branchNames))
	for _, name := range required {
		seen[name] = struct{}{}
	}
	for _, name := range branchNames {
		if _, exists := seen[name]; exists {
			continue
		}
		required = append(required, name)
		seen[name] = struct{}{}
	}
	if len(required) == 0 {
		return
	}
	requiredRaw, errMarshal := json.Marshal(required)
	if errMarshal == nil {
		root["required"] = requiredRaw
	}
}
