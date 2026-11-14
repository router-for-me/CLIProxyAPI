package modelrouting

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalProjectionJSON returns the compact canonical JSON used by AI Hub and
// CLIProxy to identify a routing projection. The root projection-digest field is
// deliberately omitted so the digest is acyclic. JSON object keys are sorted
// recursively by encoding/json after decoding with json.Number preservation.
func CanonicalProjectionJSON(cfg *Config) ([]byte, error) {
	if cfg == nil {
		return nil, fmt.Errorf("model-routing projection is nil")
	}
	raw, errMarshal := json.Marshal(cfg)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal model-routing projection: %w", errMarshal)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, fmt.Errorf("decode model-routing projection for canonicalization: %w", errDecode)
	}
	delete(value, "projection-digest")

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if errEncode := encoder.Encode(value); errEncode != nil {
		return nil, fmt.Errorf("encode canonical model-routing projection: %w", errEncode)
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

// ProjectionDigest recomputes the exact projection identity.
func ProjectionDigest(cfg *Config) (string, error) {
	canonical, errCanonical := CanonicalProjectionJSON(cfg)
	if errCanonical != nil {
		return "", errCanonical
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ConfigDigest identifies the canonical YAML bytes of the activated routing resource.
func ConfigDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}
