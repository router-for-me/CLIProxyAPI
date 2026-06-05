package misc

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/samplekeys"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const generatedClientAPIKeyBytes = 32

func CopyConfigTemplate(src, dst string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}

	rendered, generatedKey, generated, err := renderConfigTemplateWithGeneratedAPIKey(data)
	if err != nil {
		return "", err
	}

	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer func() {
		if errClose := out.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close destination config file")
		}
	}()

	if _, err = out.Write(rendered); err != nil {
		return "", err
	}
	if err = out.Sync(); err != nil {
		return "", err
	}
	if generated {
		return generatedKey, nil
	}
	return "", nil
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
	if !shouldGenerateClientAPIKey(apiKeysValueNode) {
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
	// Encoding yaml.Node may normalize whitespace and comments. This only runs when
	// generating a fresh bootstrap config from sample placeholders.
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

func shouldGenerateClientAPIKey(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return true
	}
	if node.Kind != yaml.SequenceNode {
		return false
	}
	sawSample := false
	for _, child := range node.Content {
		if child == nil || child.Kind != yaml.ScalarNode {
			continue
		}
		key := strings.TrimSpace(child.Value)
		if key == "" {
			continue
		}
		if !samplekeys.IsClientAPIKey(key) {
			return false
		}
		sawSample = true
	}
	return sawSample
}

func removeTopLevelSampleAPIKeyComments(data []byte) []byte {
	var out strings.Builder
	// The sample template keeps inactive api-keys as commented list items. After
	// generating a real key, drop those placeholder lines from the copied config.
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, `# - "your-api-key-`) {
			continue
		}
		out.WriteString(line)
	}
	return []byte(out.String())
}
