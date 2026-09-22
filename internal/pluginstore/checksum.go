package pluginstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func ParseChecksums(data []byte) (map[string]string, error) {
	out := map[string]string{}
	for lineNumber, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("line %d: invalid checksum entry", lineNumber+1)
		}
		hash := strings.ToLower(strings.TrimSpace(fields[0]))
		if len(hash) != sha256.Size*2 {
			return nil, fmt.Errorf("line %d: invalid sha256 length", lineNumber+1)
		}
		if _, errDecode := hex.DecodeString(hash); errDecode != nil {
			return nil, fmt.Errorf("line %d: invalid sha256: %w", lineNumber+1, errDecode)
		}
		name := strings.TrimPrefix(strings.TrimSpace(fields[1]), "*")
		out[name] = hash
	}
	return out, nil
}

func VerifyChecksum(name string, data []byte, checksums map[string]string) error {
	expected := strings.ToLower(strings.TrimSpace(checksums[name]))
	if expected == "" {
		return fmt.Errorf("checksum for %s not found", name)
	}
	actualBytes := sha256.Sum256(data)
	actual := hex.EncodeToString(actualBytes[:])
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	return nil
}
