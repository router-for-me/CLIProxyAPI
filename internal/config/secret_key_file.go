package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// secretKeyPaths are the document paths the management secret is read from: the
// v8 layout and the legacy one.
var secretKeyPaths = []string{"management.secret-key", "remote-management.secret-key"}

// HashSecretKeysInFile hashes every plaintext management secret in configFile in
// place, as a startup load does, and returns the file's resulting contents.
// Callers that copy the file off the machine upload those bytes. It fails when the
// file cannot be read, parsed as YAML or written, when a secret is not a string,
// or when a hashed secret's plaintext is still in the file (an anchor shared with
// another setting keeps it), since the bytes would then carry the plaintext. Only
// the secrets are inspected, so a file the full config parser rejects is still
// returned if it holds none.
func HashSecretKeysInFile(configFile string) ([]byte, error) {
	data, errRead := os.ReadFile(configFile)
	if errRead != nil {
		return nil, fmt.Errorf("read config file: %w", errRead)
	}
	hashed, plaintexts, errHash := hashSecretKeysInYAML(data)
	if errHash != nil {
		return nil, errHash
	}
	if len(plaintexts) == 0 {
		return data, nil
	}
	if errWrite := os.WriteFile(configFile, hashed, 0o600); errWrite != nil {
		return nil, fmt.Errorf("write hashed management secret: %w", errWrite)
	}
	if line, errScan := scalarLine(hashed, plaintexts); errScan != nil || line > 0 {
		return nil, fmt.Errorf("a hashed management secret is still in plaintext at line %d, e.g. in an anchor another setting uses (scan error: %v)", line, errScan)
	}
	return hashed, nil
}

// hashSecretKeysInYAML returns data with the management secret at every secret
// path replaced by its bcrypt hash, keeping comments and key order, and the
// plaintexts it replaced. Like other config writes it expands aliases and merge
// keys, so a setting that shares an anchor with a secret keeps its own value. It
// fails when data is not YAML or a secret is not a string.
func hashSecretKeysInYAML(data []byte) ([]byte, []string, error) {
	var root yaml.Node
	if errParse := yaml.Unmarshal(data, &root); errParse != nil {
		return nil, nil, fmt.Errorf("parse config file: %w", errParse)
	}
	if len(root.Content) == 0 {
		return data, nil, nil
	}
	doc := expandConfigAliases(root.Content[0])
	var plaintexts []string
	for _, path := range secretKeyPaths {
		secret := yamlPath(doc, path)
		if secret == nil {
			continue
		}
		if secret.Kind != yaml.ScalarNode {
			return nil, nil, fmt.Errorf("%s is not a string", path)
		}
		if secret.Value == "" || looksLikeBcrypt(secret.Value) {
			continue
		}
		hashed, errHash := hashSecret(secret.Value)
		if errHash != nil {
			return nil, nil, fmt.Errorf("hash %s: %w", path, errHash)
		}
		plaintexts = append(plaintexts, secret.Value)
		secret.Tag = "!!str"
		secret.Value = hashed
	}
	if len(plaintexts) == 0 {
		return data, nil, nil
	}
	root.Content[0] = doc
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if errEncode := enc.Encode(&root); errEncode != nil {
		_ = enc.Close()
		return nil, nil, fmt.Errorf("encode hashed config: %w", errEncode)
	}
	if errClose := enc.Close(); errClose != nil {
		return nil, nil, fmt.Errorf("encode hashed config: %w", errClose)
	}
	return NormalizeCommentIndentation(buf.Bytes()), plaintexts, nil
}

// scalarLine returns the line of the first scalar in data whose value is one of
// values, or 0 when none is.
func scalarLine(data []byte, values []string) (int, error) {
	var root yaml.Node
	if errParse := yaml.Unmarshal(data, &root); errParse != nil {
		return 0, errParse
	}
	var find func(*yaml.Node) int
	find = func(node *yaml.Node) int {
		if node.Kind == yaml.ScalarNode {
			for _, value := range values {
				if node.Value == value {
					return node.Line
				}
			}
		}
		for _, child := range node.Content {
			if line := find(child); line > 0 {
				return line
			}
		}
		return 0
	}
	return find(&root), nil
}
