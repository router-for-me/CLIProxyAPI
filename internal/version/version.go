// Package version normalizes and exposes CLIProxyAPI version information.
package version

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"golang.org/x/mod/semver"
)

var strictSemverPrefix = regexp.MustCompile("^v[0-9]+\\.[0-9]+\\.[0-9]+")

// Info is a machine-friendly representation of build metadata.
type Info struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	BuildDate   string `json:"buildDate"`
	RawVersion  string `json:"rawVersion"`
	ValidSemver bool   `json:"validSemver"`
}

// Normalize validates a raw version and returns a semver string without the leading "v".
func Normalize(raw string) (string, error) {
	withV, err := normalizeWithV(raw)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(withV, "v"), nil
}

// Short returns the normalized semantic version (without leading "v").
// If invalid, it falls back to "0.0.0".
func Short() string {
	normalized, err := Normalize(buildinfo.Version)
	if err != nil || normalized == "" {
		return "0.0.0"
	}
	return normalized
}

// Long returns a human-readable version string.
func Long() string {
	return fmt.Sprintf("CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s", Short(), buildinfo.Commit, buildinfo.BuildDate)
}

// JSON returns version info in JSON format.
func JSON() ([]byte, error) {
	normalized, err := Normalize(buildinfo.Version)
	info := Info{
		Version:     normalized,
		Commit:      buildinfo.Commit,
		BuildDate:   buildinfo.BuildDate,
		RawVersion:  buildinfo.Version,
		ValidSemver: err == nil && normalized != "",
	}
	if !info.ValidSemver {
		info.Version = "0.0.0"
	}
	return json.Marshal(info)
}

func normalizeWithV(raw string) (string, error) {
	version := strings.TrimSpace(raw)
	if version == "" {
		return "", fmt.Errorf("empty version")
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	if !strictSemverPrefix.MatchString(version) {
		return "", fmt.Errorf("version must start with vMAJOR.MINOR.PATCH: %s", version)
	}
	if !semver.IsValid(version) {
		return "", fmt.Errorf("invalid semver: %s", version)
	}
	return version, nil
}
