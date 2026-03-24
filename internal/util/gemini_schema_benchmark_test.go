package util

import "testing"

var benchmarkGeminiSchema = `{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "minLength": 1
    },
    "tags": {
      "type": "array",
      "uniqueItems": true,
      "items": {
        "type": "string",
        "pattern": "^[a-z0-9_-]+$"
      },
      "minItems": 1,
      "maxItems": 32
    },
    "files": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "path": {
            "type": "string",
            "format": "uri"
          },
          "size": {
            "type": "integer",
            "exclusiveMinimum": 0
          }
        },
        "required": ["path", "size"],
        "additionalProperties": false
      }
    }
  },
  "required": ["project", "tags"]
}`

var benchmarkGeminiSchemaWithoutUniqueItems = `{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "minLength": 1
    },
    "tags": {
      "type": "array",
      "items": {
        "type": "string",
        "pattern": "^[a-z0-9_-]+$"
      },
      "minItems": 1,
      "maxItems": 32
    },
    "files": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "path": {
            "type": "string",
            "format": "uri"
          },
          "size": {
            "type": "integer",
            "exclusiveMinimum": 0
          }
        },
        "required": ["path", "size"],
        "additionalProperties": false
      }
    }
  },
  "required": ["project", "tags"]
}`

func BenchmarkCleanJSONSchemaForGemini(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = CleanJSONSchemaForGemini(benchmarkGeminiSchema)
	}
}

func BenchmarkCleanJSONSchemaForGemini_WithoutUniqueItems(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = CleanJSONSchemaForGemini(benchmarkGeminiSchemaWithoutUniqueItems)
	}
}
