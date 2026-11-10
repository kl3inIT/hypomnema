package antigravity

import (
	"encoding/json"
	"strconv"
	"strings"
)

// The backend takes a small part of JSON Schema for a tool's parameters and
// refuses the whole request for one keyword it does not know. cleanSchema
// makes any schema acceptable, following 9router-go's cleanGeminiSchema
// (internal/translator/schema.go, commit 64c156d): unknown keywords go,
// unions keep their first real branch, enums become strings, and an object
// with no property gets one, which a model in VALIDATED mode needs.

// refused are the keywords the backend's schema has no field for.
var refused = []string{
	"exclusiveMinimum", "exclusiveMaximum", "format", "multipleOf",
	"uniqueItems", "contains", "unevaluatedProperties", "unevaluatedItems", "contentSchema",
	"default", "examples", "$schema", "$defs", "definitions", "const", "$ref", "$comment", "$id", "$anchor",
	"deprecated", "readOnly", "writeOnly",
	"additionalItems", "additionalProperties", "propertyNames", "patternProperties", "enumDescriptions",
	"allOf", "not", "dependencies", "dependentSchemas", "dependentRequired",
	"title", "optional", "if", "then", "else", "contentMediaType", "contentEncoding",
	"errorMessage", "errorMessages", "markdownDescription", "minProperties", "maxProperties",
}

// integerLimits are limits a generator may have written as strings.
var integerLimits = []string{"minLength", "maxLength", "minItems", "maxItems", "minimum", "maximum"}

func placeholder() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": "Brief explanation of why you are calling this tool",
	}
}

// cleanSchema rewrites a schema in place and returns it.
func cleanSchema(schema map[string]any) map[string]any {
	if schema == nil {
		schema = map[string]any{}
	}

	strip(schema)

	// A union is not taken: its first branch that is not null stands for it.
	for _, union := range []string{"anyOf", "oneOf"} {
		if branches, ok := schema[union].([]any); ok {
			for _, raw := range branches {
				if branch, ok := raw.(map[string]any); ok && branch["type"] != "null" {
					for key, value := range branch {
						schema[key] = value
					}

					break
				}
			}
		}

		delete(schema, union)
	}

	strip(schema)

	if types, ok := schema["type"].([]any); ok {
		schema["type"] = "string"

		for _, raw := range types {
			if t, ok := raw.(string); ok && t != "null" {
				schema["type"] = t

				break
			}
		}
	}

	cleanEnum(schema)

	if tuple, ok := schema["prefixItems"].([]any); ok && schema["items"] == nil && len(tuple) > 0 {
		schema["items"] = tuple[0]
	}

	delete(schema, "prefixItems")

	properties, _ := schema["properties"].(map[string]any)
	if properties != nil && schema["type"] == nil {
		schema["type"] = "object"
	}

	for name, raw := range properties {
		if property, ok := raw.(map[string]any); ok {
			properties[name] = cleanSchema(property)
		}
	}

	if schema["type"] == "array" {
		items, _ := schema["items"].(map[string]any)
		if items == nil {
			items = map[string]any{"type": "string"}
		} else {
			items = cleanSchema(items)
		}

		schema["items"] = items
	}

	cleanRequired(schema, properties)

	// An object with nothing in it, or a schema that says nothing at all.
	if (schema["type"] == "object" && len(properties) == 0) || len(schema) == 0 {
		schema["type"] = "object"
		schema["properties"] = map[string]any{"reason": placeholder()}
		schema["required"] = []string{"reason"}
	}

	return schema
}

func strip(schema map[string]any) {
	for _, key := range refused {
		delete(schema, key)
	}

	for key := range schema {
		if strings.HasPrefix(key, "x-") {
			delete(schema, key)
		}
	}

	for _, key := range integerLimits {
		if text, ok := schema[key].(string); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
				schema[key] = n
			} else {
				delete(schema, key)
			}
		}
	}
}

// cleanEnum makes an enum a list of strings, as the backend wants one.
func cleanEnum(schema map[string]any) {
	values, ok := schema["enum"].([]any)
	if !ok {
		return
	}

	texts := make([]string, 0, len(values))

	for _, value := range values {
		text, isText := value.(string)
		if !isText {
			encoded, err := json.Marshal(value)
			if err != nil {
				continue
			}

			text = string(encoded)
		}

		if text != "" {
			texts = append(texts, text)
		}
	}

	if len(texts) == 0 {
		delete(schema, "enum")

		return
	}

	schema["enum"] = texts
	schema["type"] = "string"
}

// cleanRequired keeps the required names that are properties.
func cleanRequired(schema, properties map[string]any) {
	names, _ := schema["required"].([]any)

	kept := make([]string, 0, len(names))

	for _, raw := range names {
		if name, ok := raw.(string); ok && properties[name] != nil {
			kept = append(kept, name)
		}
	}

	if len(kept) == 0 {
		delete(schema, "required")

		return
	}

	schema["required"] = kept
}
