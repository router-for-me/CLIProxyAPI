package selfupdate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MoeForge/moego/base64"
)

const maxBinarySize = 512 << 20

// decodePublicKey decodes a base64 ed25519 public key.
func decodePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key has %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// verifySignature checks the base64 ed25519 signature sigFile over the exact bytes of checksums.
func verifySignature(pub ed25519.PublicKey, checksums, sigFile []byte) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("signature has %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub, checksums, sig) {
		return errors.New("checksums.txt signature is invalid")
	}
	return nil
}

// checksumFor returns the sha256 hex digest listed for name in a sha256sum-style checksums file.
func checksumFor(checksums []byte, name string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(checksums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			sum := strings.ToLower(fields[0])
			if len(sum) != sha256.Size*2 {
				return "", fmt.Errorf("checksum for %s is malformed", name)
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

// verifyArchive checks data against the sha256 digest listed for name.
func verifyArchive(checksums []byte, name string, data []byte) error {
	want, err := checksumFor(checksums, name)
	if err != nil {
		return err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	return nil
}

// assetName is the release archive for a platform. arch uses the release naming (amd64/aarch64).
func assetName(ver, goos, goarch string, noPlugin bool) (string, error) {
	var arch string
	switch goarch {
	case "amd64":
		arch = "amd64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("no release build for %s/%s", goos, goarch)
	}
	suffix := ""
	if noPlugin && goos == "linux" {
		suffix = "_no-plugin"
	}
	return fmt.Sprintf("proxx_%s_%s_%s%s.tar.gz", strings.TrimPrefix(ver, "v"), goos, arch, suffix), nil
}

// extractBinary returns the top-level "proxx" file from a .tar.gz archive.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, errNext := tr.Next()
		if errors.Is(errNext, io.EOF) {
			return nil, errors.New("archive does not contain a proxx binary")
		}
		if errNext != nil {
			return nil, fmt.Errorf("read archive: %w", errNext)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name != "proxx" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size <= 0 || hdr.Size > maxBinarySize {
			return nil, fmt.Errorf("proxx binary has unexpected size %d", hdr.Size)
		}
		data, errRead := io.ReadAll(io.LimitReader(tr, maxBinarySize+1))
		if errRead != nil {
			return nil, fmt.Errorf("read binary: %w", errRead)
		}
		if int64(len(data)) != hdr.Size {
			return nil, errors.New("proxx binary is truncated")
		}
		return data, nil
	}
}
