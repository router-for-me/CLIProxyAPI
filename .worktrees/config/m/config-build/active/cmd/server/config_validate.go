package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	"gopkg.in/yaml.v3"
)

func validateConfigFileStrict(configFilePath string) error {
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg config.Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return fmt.Errorf("strict schema validation failed: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("config must contain a single YAML document")
	}

	if _, err := config.LoadConfig(configFilePath); err != nil {
		return fmt.Errorf("runtime validation failed: %w", err)
	}
	return nil
}
