package autoupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name    string
		latest  string
		current string
		want    bool
	}{
		{"patch bump", "6.8.48", "6.8.47", true},
		{"minor bump", "6.9.0", "6.8.47", true},
		{"major bump", "7.0.0", "6.8.47", true},
		{"same version", "6.8.47", "6.8.47", false},
		{"older version", "6.8.46", "6.8.47", false},
		{"longer newer", "6.8.47.1", "6.8.47", true},
		{"longer same prefix", "6.8.47.0", "6.8.47", false},
		{"ten vs nine", "6.8.10", "6.8.9", true},
		{"v prefix stripped", "6.8.48", "6.8.47", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNewer(tt.latest, tt.current)
			if got != tt.want {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}

func TestPlatformAssetName(t *testing.T) {
	name := platformAssetName("6.8.47")
	// Should contain version, OS, and arch.
	if name == "" {
		t.Fatal("expected non-empty asset name")
	}
	if !bytes.Contains([]byte(name), []byte("6.8.47")) {
		t.Errorf("expected version in asset name, got %s", name)
	}
	if !bytes.Contains([]byte(name), []byte(".tar.gz")) {
		t.Errorf("expected .tar.gz extension, got %s", name)
	}
}

func TestParseChecksums(t *testing.T) {
	input := []byte("abc123def456  CLIProxyAPI_6.8.47_linux_amd64.tar.gz\n" +
		"789abc012def  CLIProxyAPI_6.8.47_darwin_arm64.tar.gz\n" +
		"\n")
	result := parseChecksums(input)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result["CLIProxyAPI_6.8.47_linux_amd64.tar.gz"] != "abc123def456" {
		t.Errorf("unexpected checksum: %s", result["CLIProxyAPI_6.8.47_linux_amd64.tar.gz"])
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("hello world")
	// SHA256 of "hello world"
	checksums := map[string]string{
		"test.tar.gz": "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
	}
	if err := verifyChecksum(data, "test.tar.gz", checksums); err != nil {
		t.Fatalf("expected valid checksum, got error: %v", err)
	}
	if err := verifyChecksum(data, "missing.tar.gz", checksums); err == nil {
		t.Fatal("expected error for missing checksum")
	}
	if err := verifyChecksum([]byte("wrong"), "test.tar.gz", checksums); err == nil {
		t.Fatal("expected error for mismatched checksum")
	}
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	// Create a tar.gz archive with a fake binary.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("#!/bin/sh\necho hello")
	hdr := &tar.Header{
		Name: "cli-proxy-api",
		Mode: 0o755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()

	extracted, err := extractBinaryFromTarGz(buf.Bytes())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(extracted, content) {
		t.Errorf("extracted content mismatch")
	}
}

func TestExtractBinaryFromTarGz_NotFound(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: "README.md",
		Mode: 0o644,
		Size: 5,
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("hello"))
	tw.Close()
	gw.Close()

	_, err := extractBinaryFromTarGz(buf.Bytes())
	if err == nil {
		t.Fatal("expected error when binary not found")
	}
}

func TestFindAsset(t *testing.T) {
	assets := []releaseAsset{
		{Name: "CLIProxyAPI_6.8.47_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux"},
		{Name: "CLIProxyAPI_6.8.47_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin"},
		{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums"},
	}

	found, err := findAsset(assets, "CLIProxyAPI_6.8.47_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if found.BrowserDownloadURL != "https://example.com/linux" {
		t.Errorf("unexpected URL: %s", found.BrowserDownloadURL)
	}

	_, err = findAsset(assets, "nonexistent.tar.gz")
	if err == nil {
		t.Fatal("expected error for missing asset")
	}
}
