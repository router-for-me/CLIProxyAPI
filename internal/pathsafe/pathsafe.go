// Package pathsafe provides defenses against path-injection (CWE-22 /
// CodeQL go/path-injection) by enforcing that user-controlled inputs cannot
// escape an intended base directory.
//
// The canonical pattern is filepath.Clean + filepath.Abs prefix-check; see
// repos/docs/governance/cliproxyapi-security-triage-2026-04.md for the
// triage rationale.
package pathsafe

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrEscape is returned when the resolved path escapes the base directory.
var ErrEscape = errors.New("pathsafe: path escapes base directory")

// ErrTraversal is returned when input contains an explicit traversal segment.
var ErrTraversal = errors.New("pathsafe: path contains traversal component")

// ErrEmpty is returned when base or input are empty after trimming.
var ErrEmpty = errors.New("pathsafe: empty path component")

// SafeJoin joins userInput onto base and verifies the absolute result is
// contained within the absolute base directory. It rejects empty input,
// explicit `..` traversal segments, and paths whose absolute form does not
// have base as a prefix.
//
// userInput may be a bare file name or a relative subpath; absolute paths
// supplied as userInput are rejected as a defensive measure (callers that
// truly want to allow absolute paths should validate them through
// SafeContain instead).
func SafeJoin(base, userInput string) (string, error) {
	base = strings.TrimSpace(base)
	userInput = strings.TrimSpace(userInput)
	if base == "" || userInput == "" {
		return "", ErrEmpty
	}
	if filepath.IsAbs(userInput) {
		return "", ErrEscape
	}
	if hasTraversal(userInput) {
		return "", ErrTraversal
	}

	cleaned := filepath.Clean(filepath.Join(base, userInput))
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("pathsafe: resolve base: %w", err)
	}
	absCleaned, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("pathsafe: resolve target: %w", err)
	}
	if absCleaned != absBase &&
		!strings.HasPrefix(absCleaned, absBase+string(filepath.Separator)) {
		return "", ErrEscape
	}
	return absCleaned, nil
}

// SafeContain validates that an already-constructed full path lies inside
// the supplied base directory. Use this when the caller assembled the path
// themselves (for example, from configuration) but the final path still
// crosses a trust boundary into a filesystem syscall.
func SafeContain(base, fullPath string) (string, error) {
	base = strings.TrimSpace(base)
	fullPath = strings.TrimSpace(fullPath)
	if base == "" || fullPath == "" {
		return "", ErrEmpty
	}
	if hasTraversal(fullPath) {
		return "", ErrTraversal
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("pathsafe: resolve base: %w", err)
	}
	absFull, err := filepath.Abs(filepath.Clean(fullPath))
	if err != nil {
		return "", fmt.Errorf("pathsafe: resolve target: %w", err)
	}
	if absFull != absBase &&
		!strings.HasPrefix(absFull, absBase+string(filepath.Separator)) {
		return "", ErrEscape
	}
	return absFull, nil
}

// hasTraversal returns true when path contains an explicit `..` segment
// after normalising backslashes to forward slashes (defensive on Windows).
func hasTraversal(path string) bool {
	normalized := strings.ReplaceAll(path, "\\", "/")
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}
