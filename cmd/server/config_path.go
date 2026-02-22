package main

import (
	"os"
	"path/filepath"
	"strings"
)

func resolveDefaultConfigPath(wd string, isCloudDeploy bool) string {
	selected, _ := resolveDefaultConfigPathWithCandidates(wd, isCloudDeploy)
	return selected
}

func resolveDefaultConfigPathWithCandidates(wd string, isCloudDeploy bool) (string, []string) {
	fallback := filepath.Join(wd, "config.yaml")
	candidates := make([]string, 0, 12)

	addEnvCandidate := func(key string) {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			candidates = append(candidates, value)
		}
	}
	addEnvCandidate("CONFIG")
	addEnvCandidate("CONFIG_PATH")
	addEnvCandidate("CLIPROXY_CONFIG")
	addEnvCandidate("CLIPROXY_CONFIG_PATH")

	candidates = append(candidates, fallback)
	// If config.yaml is mounted as a directory (common Docker mis-mount),
	// prefer the nested config/config.yaml path before failing on the directory.
	candidates = append(candidates, filepath.Join(wd, "config", "config.yaml"))
	if isCloudDeploy {
		candidates = append(candidates,
			"/CLIProxyAPI/config.yaml",
			"/CLIProxyAPI/config/config.yaml",
			"/config/config.yaml",
			"/app/config.yaml",
			"/app/config/config.yaml",
		)
	}

	for _, candidate := range candidates {
		if isReadableConfigFile(candidate) {
			return candidate, candidates
		}
	}
	return fallback, candidates
}

func isReadableConfigFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
