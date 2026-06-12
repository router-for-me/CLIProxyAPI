package pluginstore

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestSelectReleaseAssets(t *testing.T) {
	t.Parallel()

	release := Release{Assets: []ReleaseAsset{
		{Name: "sample-provider_0.1.0_darwin_arm64.zip", BrowserDownloadURL: "https://example.com/sample-provider.zip"},
		{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums.txt"},
	}}
	archiveAsset, checksumAsset, errSelect := SelectReleaseAssets(release, "sample-provider", "0.1.0", "darwin", "arm64")
	if errSelect != nil {
		t.Fatalf("SelectReleaseAssets() error = %v", errSelect)
	}
	if archiveAsset.BrowserDownloadURL != "https://example.com/sample-provider.zip" {
		t.Fatalf("archive URL = %q", archiveAsset.BrowserDownloadURL)
	}
	if checksumAsset.BrowserDownloadURL != "https://example.com/checksums.txt" {
		t.Fatalf("checksum URL = %q", checksumAsset.BrowserDownloadURL)
	}
}

func TestSelectReleaseAssetsRejectsMissingAssets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		release Release
		wantErr string
	}{
		{
			name: "missing zip",
			release: Release{Assets: []ReleaseAsset{
				{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums.txt"},
			}},
			wantErr: "sample-provider_0.1.0_darwin_arm64.zip",
		},
		{
			name: "missing checksum",
			release: Release{Assets: []ReleaseAsset{
				{Name: "sample-provider_0.1.0_darwin_arm64.zip", BrowserDownloadURL: "https://example.com/sample-provider.zip"},
			}},
			wantErr: "checksums.txt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, errSelect := SelectReleaseAssets(tt.release, "sample-provider", "0.1.0", "darwin", "arm64")
			if errSelect == nil {
				t.Fatal("SelectReleaseAssets() error = nil")
			}
			if !strings.Contains(errSelect.Error(), tt.wantErr) {
				t.Fatalf("SelectReleaseAssets() error = %v, want substring %q", errSelect, tt.wantErr)
			}
		})
	}
}

func TestReleaseVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tagName string
		want    string
		wantErr bool
	}{
		{name: "v prefix", tagName: "v1.2.3", want: "1.2.3"},
		{name: "no prefix", tagName: "0.1.0", want: "0.1.0"},
		{name: "whitespace", tagName: " v2.0.0 ", want: "2.0.0"},
		{name: "empty", tagName: "", wantErr: true},
		{name: "non numeric", tagName: "latest", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			version, errVersion := ReleaseVersion(Release{TagName: tt.tagName})
			if tt.wantErr {
				if errVersion == nil {
					t.Fatalf("ReleaseVersion(%q) error = nil", tt.tagName)
				}
				return
			}
			if errVersion != nil {
				t.Fatalf("ReleaseVersion(%q) error = %v", tt.tagName, errVersion)
			}
			if version != tt.want {
				t.Fatalf("ReleaseVersion(%q) = %q, want %q", tt.tagName, version, tt.want)
			}
		})
	}
}

func TestParseChecksumsAndVerifyChecksum(t *testing.T) {
	t.Parallel()

	data := []byte("zip-data")
	sum := sha256.Sum256(data)
	checksumText := hex.EncodeToString(sum[:]) + "  sample-provider_0.1.0_darwin_arm64.zip\n"
	checksums, errParse := ParseChecksums([]byte(checksumText))
	if errParse != nil {
		t.Fatalf("ParseChecksums() error = %v", errParse)
	}
	if errVerify := VerifyChecksum("sample-provider_0.1.0_darwin_arm64.zip", data, checksums); errVerify != nil {
		t.Fatalf("VerifyChecksum() error = %v", errVerify)
	}
}

func TestVerifyChecksumRejectsMissingAndMismatch(t *testing.T) {
	t.Parallel()

	sum := sha256.Sum256([]byte("zip-data"))
	checksums := map[string]string{"sample-provider.zip": hex.EncodeToString(sum[:])}
	if errVerify := VerifyChecksum("missing.zip", []byte("zip-data"), checksums); errVerify == nil {
		t.Fatal("VerifyChecksum() missing checksum error = nil")
	}
	if errVerify := VerifyChecksum("sample-provider.zip", []byte("other"), checksums); errVerify == nil {
		t.Fatal("VerifyChecksum() mismatch error = nil")
	}
}
