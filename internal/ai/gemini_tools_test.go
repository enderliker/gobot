package ai

import "testing"

func TestSanitizeGeminiSchemaRemovesAdditionalProperties(t *testing.T) {
	input := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"user": map[string]any{
				"type":                 "string",
				"additionalProperties": false,
			},
		},
	}

	got, ok := sanitizeGeminiSchema(input).(map[string]any)
	if !ok {
		t.Fatal("expected sanitized schema to remain a map")
	}
	if _, exists := got["additionalProperties"]; exists {
		t.Fatal("expected top-level additionalProperties to be removed")
	}

	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties to remain a map")
	}
	user, ok := props["user"].(map[string]any)
	if !ok {
		t.Fatal("expected nested property to remain a map")
	}
	if _, exists := user["additionalProperties"]; exists {
		t.Fatal("expected nested additionalProperties to be removed")
	}
}
