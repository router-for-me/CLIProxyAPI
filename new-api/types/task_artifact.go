package types

// TaskArtifact is the transport-neutral identity of one generated task output.
// relay/channel re-exports this type for adaptor compatibility.
type TaskArtifact struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	MimeType string `json:"mimeType,omitempty"`
}
