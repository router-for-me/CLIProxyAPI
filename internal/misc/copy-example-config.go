package misc

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const generatedClientAPIKeyBytes = 32

func CopyConfigTemplate(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	rendered, generatedKey, generated, err := renderConfigTemplateWithGeneratedAPIKey(data)
	if err != nil {
		return err
	}

	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := out.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close destination config file")
		}
	}()

	if _, err = out.Write(rendered); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if generated {
		fmt.Printf("Generated client API key for %s: %s\n", filepath.Clean(dst), generatedKey)
	}
	return nil
}

func generateClientAPIKey() (string, error) {
	buf := make([]byte, generatedClientAPIKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate client API key: %w", err)
	}
	return "cpak-" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func renderConfigTemplateWithGeneratedAPIKey(data []byte) ([]byte, string, bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return data, "", false, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, "", false, fmt.Errorf("parse config template: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0] == nil {
		return data, "", false, nil
	}

	apiKeysKeyNode, apiKeysValueNode := topLevelMappingEntry(doc.Content[0], "api-keys")
	if apiKeysValueNode == nil {
		return data, "", false, nil
	}
	if hasOperatorAPIKeys(apiKeysValueNode) {
		return data, "", false, nil
	}

	key, err := generateClientAPIKey()
	if err != nil {
		return nil, "", false, err
	}

	if apiKeysKeyNode != nil {
		apiKeysKeyNode.FootComment = ""
	}
	*apiKeysValueNode = yaml.Node{
		Kind: yaml.SequenceNode,
		Tag:  "!!seq",
		Content: []*yaml.Node{
			{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: key,
				Style: yaml.DoubleQuotedStyle,
			},
		},
	}

	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err = encoder.Encode(&doc); err != nil {
		_ = encoder.Close()
		return nil, "", false, fmt.Errorf("render config template: %w", err)
	}
	if err = encoder.Close(); err != nil {
		return nil, "", false, fmt.Errorf("close yaml encoder: %w", err)
	}
	return removeTopLevelSampleAPIKeyComments(out.Bytes()), key, true, nil
}

func topLevelMappingEntry(root *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode := root.Content[i]
		if keyNode != nil && keyNode.Value == key {
			return keyNode, root.Content[i+1]
		}
	}
	return nil, nil
}

func hasOperatorAPIKeys(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind != yaml.SequenceNode {
		return false
	}
	for _, child := range node.Content {
		if child == nil || child.Kind != yaml.ScalarNode {
			continue
		}
		key := strings.TrimSpace(child.Value)
		if key == "" || isSampleClientAPIKey(key) {
			continue
		}
		return true
	}
	return false
}

func isSampleClientAPIKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "your-api-key-1", "your-api-key-2", "your-api-key-3":
		return true
	}
	return false
}

func removeTopLevelSampleAPIKeyComments(data []byte) []byte {
	var out strings.Builder
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, `# - "your-api-key-`) {
			continue
		}
		out.WriteString(line)
	}
	return []byte(out.String())
}
