package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const jsHandlerProvider = "jshandler"
const pluginName = "jshandler"

type jsHandlerConfig struct {
	Enabled     bool          `yaml:"enabled"`
	ScriptPaths []string      `yaml:"script_paths"`
	TimeoutRaw  string        `yaml:"timeout"`
	Timeout     time.Duration `yaml:"-"`
}

func defaultJSHandlerConfig() jsHandlerConfig {
	return jsHandlerConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}
}

func parseJSHandlerConfig(raw []byte) (jsHandlerConfig, error) {
	cfg := defaultJSHandlerConfig()
	if len(strings.TrimSpace(string(raw))) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return cfg, fmt.Errorf("invalid jshandler config: %w", errUnmarshal)
		}
	}
	if strings.TrimSpace(cfg.TimeoutRaw) != "" {
		parsed, errParse := time.ParseDuration(strings.TrimSpace(cfg.TimeoutRaw))
		if errParse != nil || parsed <= 0 {
			return cfg, fmt.Errorf("invalid jshandler timeout %q", cfg.TimeoutRaw)
		}
		cfg.Timeout = parsed
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 1 * time.Second
	}
	return cfg, nil
}

func (cfg *jsHandlerConfig) resolvedScriptPaths(pluginDir string) ([]string, error) {
	var paths []string
	for _, p := range cfg.ScriptPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		originalPath := p
		relativePath := !filepath.IsAbs(p)
		if !filepath.IsAbs(p) {
			if pluginDir == "" {
				return nil, fmt.Errorf("relative script path %q requires plugin_dir", originalPath)
			}
			p = filepath.Join(pluginDir, p)
			if !isPathWithinDir(p, pluginDir) {
				return nil, fmt.Errorf("relative script path %q escapes plugin_dir", originalPath)
			}
		}
		cleanPath, errClean := filepath.Abs(filepath.Clean(p))
		if errClean != nil {
			return nil, errClean
		}
		if relativePath {
			resolvedPath, errEval := filepath.EvalSymlinks(cleanPath)
			if errEval != nil {
				return nil, errEval
			}
			if !isResolvedPathWithinDir(resolvedPath, pluginDir) {
				return nil, fmt.Errorf("relative script path %q escapes plugin_dir through symlink", originalPath)
			}
			cleanPath = resolvedPath
		}
		paths = append(paths, cleanPath)
	}
	return paths, nil
}

func builtinScriptPaths(pluginDir string) []string {
	if pluginDir == "" {
		return nil
	}
	scriptsDir := filepath.Join(pluginDir, "scripts")
	cleanScriptsDir, errClean := filepath.Abs(filepath.Clean(scriptsDir))
	if errClean != nil {
		return nil
	}
	entries, errRead := os.ReadDir(scriptsDir)
	if errRead != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".js") {
			candidate := filepath.Join(cleanScriptsDir, name)
			resolved, errEval := filepath.EvalSymlinks(candidate)
			if errEval != nil || !isResolvedPathWithinDir(resolved, cleanScriptsDir) {
				continue
			}
			paths = append(paths, resolved)
		}
	}
	return paths
}

func isPathWithinDir(path, dir string) bool {
	cleanPath, errPath := filepath.Abs(filepath.Clean(path))
	if errPath != nil {
		return false
	}
	cleanDir, errDir := filepath.Abs(filepath.Clean(dir))
	if errDir != nil {
		return false
	}
	rel, errRel := filepath.Rel(cleanDir, cleanPath)
	if errRel != nil {
		return false
	}
	return rel == "." || (rel != "" && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func isResolvedPathWithinDir(path, dir string) bool {
	resolvedDir, errEval := filepath.EvalSymlinks(dir)
	if errEval != nil {
		return false
	}
	return isPathWithinDir(path, resolvedDir)
}
