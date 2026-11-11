package auth

import "testing"

func TestApplyAuthPriorityMetadataClearsUntrustedFileMarker(t *testing.T) {
	for _, metadata := range []map[string]any{
		{},
		{"priority": "bad"},
	} {
		auth := &Auth{
			Attributes: map[string]string{"priority": "7", AttributeFilePriority: "true"},
			Metadata:   map[string]any{"priority": float64(7)},
		}
		ApplyAuthPriorityMetadata(auth, metadata)
		if _, inherited := auth.Attributes[AttributeFilePriority]; inherited {
			t.Errorf("source %v kept untrusted file priority marker", metadata)
		}
		if auth.Attributes["priority"] != "7" || auth.Metadata["priority"] != float64(7) {
			t.Errorf("source %v changed plugin priority %q/%v", metadata, auth.Attributes["priority"], auth.Metadata["priority"])
		}
	}
}

func TestApplyAuthPriorityMetadataIgnoresInvalidFilePriority(t *testing.T) {
	auth := &Auth{
		Attributes: map[string]string{"priority": "7"},
		Metadata:   map[string]any{"priority": float64(7)},
	}
	ApplyAuthPriorityMetadata(auth, map[string]any{"priority": "bad"})
	if auth.Attributes["priority"] != "7" || auth.Metadata["priority"] != float64(7) {
		t.Fatalf("priority = %q/%v, want 7/7", auth.Attributes["priority"], auth.Metadata["priority"])
	}
	if _, inherited := auth.Attributes[AttributeFilePriority]; inherited {
		t.Fatal("invalid file priority marked as inherited")
	}
}

func TestApplyAuthPriorityMetadataPreservesBuiltinNumericConversion(t *testing.T) {
	auth := &Auth{}
	ApplyAuthPriorityMetadata(auth, map[string]any{"priority": float64(1.5)})
	if got := auth.Attributes["priority"]; got != "1" {
		t.Errorf("priority attribute = %q, want truncated value 1", got)
	}
	if got := auth.Metadata["priority"]; got != float64(1.5) {
		t.Errorf("priority metadata = %v, want original value", got)
	}
}
