 package util
 
 import (
 	"os"
 	"path/filepath"
 	"testing"
 )
 
 func TestResolveAuthDir(t *testing.T) {
 	tests := []struct {
 		name    string
 		input   string
 		wantErr bool
 	}{
 		{
 			name:    "empty string",
 			input:   "",
 			wantErr: false,
 		},
 		{
 			name:    "absolute path",
 			input:   "/tmp/auth",
 			wantErr: false,
 		},
 		{
 			name:    "relative path",
 			input:   "auth",
 			wantErr: false,
 		},
 		{
 			name:    "tilde path",
 			input:   "~/auth",
 			wantErr: false,
 		},
 		{
 			name:    "tilde only",
 			input:   "~",
 			wantErr: false,
 		},
 	}
 
 	for _, tt := range tests {
 		t.Run(tt.name, func(t *testing.T) {
 			result, err := ResolveAuthDir(tt.input)
 			if (err != nil) != tt.wantErr {
 				t.Errorf("ResolveAuthDir(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
 				return
 			}
 			if tt.input == "" && result != "" {
 				t.Errorf("ResolveAuthDir(%q) = %q, want empty", tt.input, result)
 			}
 			if tt.input != "" && result == "" && !tt.wantErr {
 				t.Errorf("ResolveAuthDir(%q) returned empty string unexpectedly", tt.input)
 			}
 		})
 	}
 }
 
 func TestResolveAuthDir_TildeExpansion(t *testing.T) {
 	home, err := os.UserHomeDir()
 	if err != nil {
 		t.Skip("cannot get user home directory")
 	}
 
 	result, err := ResolveAuthDir("~/test/auth")
 	if err != nil {
 		t.Fatalf("ResolveAuthDir(~/test/auth) error = %v", err)
 	}
 
 	expected := filepath.Join(home, "test", "auth")
 	if result != expected {
 		t.Errorf("ResolveAuthDir(~/test/auth) = %q, want %q", result, expected)
 	}
 }
 
 func TestEnsureAuthDir(t *testing.T) {
 	// Test with temp directory
 	tmpDir := t.TempDir()
 	testDir := filepath.Join(tmpDir, "test-auth")
 
 	result, err := EnsureAuthDir(testDir)
 	if err != nil {
 		t.Fatalf("EnsureAuthDir(%q) error = %v", testDir, err)
 	}
 	if result != testDir {
 		t.Errorf("EnsureAuthDir(%q) = %q, want %q", testDir, result, testDir)
 	}
 
 	// Verify directory was created
 	info, err := os.Stat(testDir)
 	if err != nil {
 		t.Fatalf("os.Stat(%q) error = %v", testDir, err)
 	}
 	if !info.IsDir() {
 		t.Errorf("EnsureAuthDir(%q) did not create a directory", testDir)
 	}
 }
 
 func TestEnsureAuthDir_ExistingDir(t *testing.T) {
 	tmpDir := t.TempDir()
 
 	result, err := EnsureAuthDir(tmpDir)
 	if err != nil {
 		t.Fatalf("EnsureAuthDir(%q) error = %v", tmpDir, err)
 	}
 	if result != tmpDir {
 		t.Errorf("EnsureAuthDir(%q) = %q, want %q", tmpDir, result, tmpDir)
 	}
 }
 
 func TestEnsureAuthDir_EmptyString(t *testing.T) {
 	_, err := EnsureAuthDir("")
 	if err == nil {
 		t.Error("EnsureAuthDir(\"\") should return an error")
 	}
 }
 
 func TestCountAuthFiles(t *testing.T) {
 	tmpDir := t.TempDir()
 
 	// Create some test files
 	files := []string{"auth1.json", "auth2.json", "config.yaml", "readme.txt"}
 	for _, f := range files {
 		path := filepath.Join(tmpDir, f)
 		if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
 			t.Fatalf("failed to create test file %s: %v", path, err)
 		}
 	}
 
 	count := CountAuthFiles(tmpDir)
 	if count != 2 {
 		t.Errorf("CountAuthFiles(%q) = %d, want 2", tmpDir, count)
 	}
 }
 
 func TestCountAuthFiles_EmptyDir(t *testing.T) {
 	tmpDir := t.TempDir()
 	count := CountAuthFiles(tmpDir)
 	if count != 0 {
 		t.Errorf("CountAuthFiles(%q) = %d, want 0", tmpDir, count)
 	}
 }
 
 func TestCountAuthFiles_NonExistent(t *testing.T) {
 	count := CountAuthFiles("/nonexistent/path/that/does/not/exist")
 	if count != 0 {
 		t.Errorf("CountAuthFiles(nonexistent) = %d, want 0", count)
 	}
 }
 
 func TestWritablePath(t *testing.T) {
 	// Save and restore environment
 	origUpper := os.Getenv("WRITABLE_PATH")
 	origLower := os.Getenv("writable_path")
 	defer func() {
 		os.Setenv("WRITABLE_PATH", origUpper)
 		os.Setenv("writable_path", origLower)
 	}()
 
 	// Clear both
 	os.Unsetenv("WRITABLE_PATH")
 	os.Unsetenv("writable_path")
 
 	// Test empty
 	if result := WritablePath(); result != "" {
 		t.Errorf("WritablePath() = %q, want empty", result)
 	}
 
 	// Test uppercase
 	os.Setenv("WRITABLE_PATH", "/tmp/test")
 	if result := WritablePath(); result != "/tmp/test" {
 		t.Errorf("WritablePath() = %q, want /tmp/test", result)
 	}
 	os.Unsetenv("WRITABLE_PATH")
 
 	// Test lowercase
 	os.Setenv("writable_path", "/tmp/lower")
 	if result := WritablePath(); result != "/tmp/lower" {
 		t.Errorf("WritablePath() = %q, want /tmp/lower", result)
 	}
 }
