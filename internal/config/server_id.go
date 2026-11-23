package config

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

var serverIDPath = []string{"server", "server-id"}

func configuredServerID(root *yaml.Node) (string, error) {
	value := effectiveConfigValue(root, serverIDPath...)
	if value == nil || value.ShortTag() == "!!null" {
		return "", nil
	}
	if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" {
		return "", fmt.Errorf("server.server-id must be a UUID string")
	}
	id := strings.TrimSpace(value.Value)
	if id == "" {
		return "", nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil || len(id) != 36 || parsed == uuid.Nil {
		return "", fmt.Errorf("server.server-id must be a nonzero hyphenated UUID")
	}
	return parsed.String(), nil
}

// ProjectConfigServerID canonicalizes an explicit identity in a read-only YAML
// view. If no identity was persisted, it can project the runtime's ephemeral ID.
func ProjectConfigServerID(root *yaml.Node, fallback string) (string, error) {
	id, err := configuredServerID(root)
	if err != nil {
		return "", err
	}
	if id == "" {
		id = fallback
	}
	if id != "" {
		if errSet := setManagementServerID(root, id); errSet != nil {
			return "", errSet
		}
	}
	return id, nil
}

// EnsureManagementServerID canonicalizes the candidate identity or allocates a
// new UUID when it is missing or invalid. Startup intentionally remains strict.
func EnsureManagementServerID(root *yaml.Node) (string, error) {
	id, _ := configuredServerID(root)
	if id == "" {
		var err error
		id, err = newServerID()
		if err != nil {
			return "", err
		}
	}
	if errSet := setManagementServerID(root, id); errSet != nil {
		return "", errSet
	}
	return id, nil
}

func setManagementServerID(root *yaml.Node, id string) error {
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config must be a mapping")
	}
	if server := effectiveConfigValue(root, "server"); server != nil && server.Kind != yaml.MappingNode && server.ShortTag() != "!!null" {
		return fmt.Errorf("server must be a mapping")
	}
	if value := effectiveConfigValue(root, serverIDPath...); value == nil || value.ShortTag() != "!!str" || value.Value != id {
		setConfigScalar(root, serverIDPath, id)
	}
	return nil
}

// EnsureServerIDInConfigBytes validates the complete stream and sets only the
// first document's identity. Other startup rewrites are deliberately not applied.
func EnsureServerIDInConfigBytes(data []byte) ([]byte, string, error) {
	return EnsureServerIDInConfigBytesWithOptional(data, false)
}

// EnsureServerIDInConfigBytesWithOptional returns updated bytes and their ID. On
// optional standby it returns (nil, "", nil); no identifier has been allocated.
func EnsureServerIDInConfigBytesWithOptional(data []byte, optional bool) ([]byte, string, error) {
	parsed, errParse := validateStartupConfig(data, optional)
	if errParse != nil || len(parsed.docs) == 0 {
		return nil, "", errParse
	}
	id := parsed.cfg.ServerID
	if id == "" {
		id, errParse = newServerID()
		if errParse != nil {
			return nil, "", errParse
		}
	}
	updated, errUpdate := renderStartupConfig(data, parsed.docs, id, "", false)
	if errUpdate != nil {
		return nil, "", errUpdate
	}
	return updated, id, nil
}

func newServerID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate server ID: %w", err)
	}
	return id.String(), nil
}

var ephemeralServerIDs struct {
	sync.Mutex
	entries []ephemeralServerID
}

type ephemeralServerID struct {
	info os.FileInfo
	id   string
}

func lookupEphemeralServerID(info os.FileInfo) string {
	ephemeralServerIDs.Lock()
	defer ephemeralServerIDs.Unlock()
	for _, entry := range ephemeralServerIDs.entries {
		if os.SameFile(entry.info, info) {
			return entry.id
		}
	}
	return ""
}

func forgetEphemeralServerID(info os.FileInfo) {
	ephemeralServerIDs.Lock()
	defer ephemeralServerIDs.Unlock()
	ephemeralServerIDs.entries = slices.DeleteFunc(ephemeralServerIDs.entries, func(entry ephemeralServerID) bool {
		return os.SameFile(entry.info, info)
	})
}

func retirePersistedServerID(info os.FileInfo, data []byte) {
	if lookupEphemeralServerID(info) == "" {
		return
	}
	doc, err := DecodeFirstConfigDocument(data)
	if err != nil {
		return
	}
	if id, errID := configuredServerID(doc.Content[0]); errID == nil && id != "" {
		forgetEphemeralServerID(info)
	}
}

func rememberEphemeralServerID(info os.FileInfo, id string) {
	ephemeralServerIDs.Lock()
	defer ephemeralServerIDs.Unlock()
	ephemeralServerIDs.entries = append(ephemeralServerIDs.entries, ephemeralServerID{info, id})
}
