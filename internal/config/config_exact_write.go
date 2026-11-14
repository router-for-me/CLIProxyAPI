package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WriteConfigAtomicExact atomically persists the supplied bytes without
// normalization. CAS publishers use it so config_digest is predictable before
// the request and identifies the exact durable file.
func WriteConfigAtomicExact(path string, data []byte) (returnErr error) {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("config path is empty")
	}
	dir := filepath.Dir(path)
	mode := os.FileMode(0o644)
	if info, errStat := os.Stat(path); errStat == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(errStat) {
		return errStat
	}
	file, errCreate := os.CreateTemp(dir, ".config-write-*.yaml")
	if errCreate != nil {
		return errCreate
	}
	tempPath := file.Name()
	committed := false
	defer func() {
		if file != nil {
			returnErr = errors.Join(returnErr, file.Close())
		}
		if !committed {
			if errRemove := os.Remove(tempPath); errRemove != nil && !os.IsNotExist(errRemove) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove uncommitted config staging file: %w", errRemove))
			}
		}
	}()
	if errChmod := file.Chmod(mode); errChmod != nil {
		return fmt.Errorf("set config staging permissions: %w", errChmod)
	}
	if _, errWrite := file.Write(data); errWrite != nil {
		return fmt.Errorf("write config staging file: %w", errWrite)
	}
	if errSync := file.Sync(); errSync != nil {
		return fmt.Errorf("sync config staging file: %w", errSync)
	}
	if errClose := file.Close(); errClose != nil {
		file = nil
		return fmt.Errorf("close config staging file: %w", errClose)
	}
	file = nil

	directory, errOpen := os.Open(dir)
	if errOpen != nil {
		return fmt.Errorf("open config directory: %w", errOpen)
	}
	if errRename := os.Rename(tempPath, path); errRename != nil {
		return errors.Join(fmt.Errorf("activate staged config: %w", errRename), directory.Close())
	}
	committed = true
	errSync := directory.Sync()
	errClose := directory.Close()
	if errSync != nil {
		errSync = fmt.Errorf("sync config directory: %w", errSync)
	}
	if errClose != nil {
		errClose = fmt.Errorf("close config directory: %w", errClose)
	}
	return errors.Join(errSync, errClose)
}

// RemoveTopLevelKeyPreserveComments deletes one top-level key from the config
// file, keeping every other key, comment and position.
func RemoveTopLevelKeyPreserveComments(configFile string, key string) error {
	data, errRead := os.ReadFile(configFile)
	if errRead != nil {
		return errRead
	}
	var root yaml.Node
	if errUnmarshal := yaml.Unmarshal(data, &root); errUnmarshal != nil {
		return errUnmarshal
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return fmt.Errorf("invalid yaml document structure")
	}
	removeMapKey(root.Content[0], key)
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if errEncode := encoder.Encode(&root); errEncode != nil {
		return errors.Join(errEncode, encoder.Close())
	}
	if errClose := encoder.Close(); errClose != nil {
		return errClose
	}
	return WriteConfigAtomicExact(configFile, buf.Bytes())
}

// UpdateNestedScalarBytes replaces one nested scalar in YAML while preserving
// comments, key order and every other byte position the encoder can retain.
func UpdateNestedScalarBytes(data []byte, path []string, value string) ([]byte, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("nested scalar path is empty")
	}
	var root yaml.Node
	if errUnmarshal := yaml.Unmarshal(data, &root); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("invalid yaml document structure")
	}
	node := root.Content[0]
	for index, key := range path {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("nested scalar path[%d] is empty", index)
		}
		if index == len(path)-1 {
			scalar := getOrCreateMapValue(node, key)
			scalar.Kind = yaml.ScalarNode
			scalar.Tag = "!!str"
			scalar.Value = value
			continue
		}
		next := getOrCreateMapValue(node, key)
		if next.Kind != yaml.MappingNode {
			next.Kind = yaml.MappingNode
			next.Tag = "!!map"
			next.Content = nil
		}
		node = next
	}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if errEncode := encoder.Encode(&root); errEncode != nil {
		return nil, errors.Join(errEncode, encoder.Close())
	}
	if errClose := encoder.Close(); errClose != nil {
		return nil, errClose
	}
	return buf.Bytes(), nil
}
