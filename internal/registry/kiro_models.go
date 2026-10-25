package registry

// GetKiroModels publishes Auto without guessing plan-dependent model IDs.
// Additional models are discovered from the account or configured in its model editor.
func GetKiroModels() []*ModelInfo {
	return []*ModelInfo{{ID: "auto", Object: "model", OwnedBy: "kiro", Type: "kiro", DisplayName: "Kiro Auto"}}
}
