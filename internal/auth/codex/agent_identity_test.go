package codex

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/ssh"
)

func TestGenerateAgentKeyMaterialProducesCompatibleEncodings(t *testing.T) {
	material, err := GenerateAgentKeyMaterial()
	if err != nil {
		t.Fatalf("GenerateAgentKeyMaterial() error = %v", err)
	}
	privateKey, err := ParseAgentIdentityPrivateKey(material.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("ParseAgentIdentityPrivateKey() error = %v", err)
	}
	derivedPublicKey, err := PublicKeySSHFromPrivateKey(material.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("PublicKeySSHFromPrivateKey() error = %v", err)
	}
	if derivedPublicKey != material.PublicKeySSH {
		t.Fatalf("derived public key does not match generated public key")
	}
	parsedSSHKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(material.PublicKeySSH))
	if err != nil {
		t.Fatalf("parse OpenSSH public key: %v", err)
	}
	cryptoPublicKey, ok := parsedSSHKey.(ssh.CryptoPublicKey)
	if !ok {
		t.Fatalf("OpenSSH key has type %T, want ssh.CryptoPublicKey", parsedSSHKey)
	}
	sshEd25519Key, ok := cryptoPublicKey.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("crypto public key has type %T", cryptoPublicKey.CryptoPublicKey())
	}
	privatePublicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(sshEd25519Key, privatePublicKey) {
		t.Fatal("OpenSSH and PKCS#8 keys are not the same key pair")
	}
}

func TestGenerateAgentKeyMaterialReportsEntropyFailure(t *testing.T) {
	_, err := generateAgentKeyMaterial(errorReader{})
	if err == nil || !strings.Contains(err.Error(), "generate Agent Identity key") {
		t.Fatalf("generateAgentKeyMaterial() error = %v", err)
	}
}

func TestGenerateAgentKeyMaterialUsesCodexSeedDerivation(t *testing.T) {
	seedMaterial := bytes.Repeat([]byte{0x5a}, agentIdentityKeySeedBytes)
	material, err := generateAgentKeyMaterial(bytes.NewReader(seedMaterial))
	if err != nil {
		t.Fatalf("generateAgentKeyMaterial() error = %v", err)
	}
	privateKey, err := ParseAgentIdentityPrivateKey(material.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("ParseAgentIdentityPrivateKey() error = %v", err)
	}
	digest := sha512.New()
	_, _ = digest.Write([]byte(agentIdentityKeyDerivationContext))
	_, _ = digest.Write(seedMaterial)
	if !bytes.Equal(privateKey.Seed(), digest.Sum(nil)[:ed25519.SeedSize]) {
		t.Fatal("private key seed does not match Codex Agent Identity derivation")
	}
}

func TestParseAgentIdentityPrivateKeyRejectsInvalidFormats(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "base64", value: "%%%"},
		{name: "PKCS8", value: base64.StdEncoding.EncodeToString([]byte("not-der"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseAgentIdentityPrivateKey(test.value); err == nil {
				t.Fatal("ParseAgentIdentityPrivateKey() unexpectedly succeeded")
			}
		})
	}
}

func TestSignAgentTaskRegistrationSignsCanonicalPayload(t *testing.T) {
	material := deterministicAgentKeyMaterial(t, 0x11)
	key := AgentIdentityKey{AgentRuntimeID: "runtime-123", PrivateKeyPKCS8Base64: material.PrivateKeyPKCS8Base64}
	timestamp := "2026-07-22T12:34:56Z"
	signatureText, err := signAgentTaskRegistration(key, timestamp)
	if err != nil {
		t.Fatalf("signAgentTaskRegistration() error = %v", err)
	}
	signature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	privateKey, err := ParseAgentIdentityPrivateKey(material.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if !ed25519.Verify(publicKey, []byte("runtime-123:"+timestamp), signature) {
		t.Fatal("task registration signature did not verify")
	}
	if ed25519.Verify(publicKey, []byte("runtime-123:task-123:"+timestamp), signature) {
		t.Fatal("task registration signature used assertion payload format")
	}
}

func TestDecryptAgentTaskIDPreservesUTF8Plaintext(t *testing.T) {
	material := deterministicAgentKeyMaterial(t, 0x22)
	privateKey, err := ParseAgentIdentityPrivateKey(material.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("ParseAgentIdentityPrivateKey() error = %v", err)
	}
	curvePublicKey, _, err := curve25519KeyPair(privateKey)
	if err != nil {
		t.Fatalf("curve25519KeyPair() error = %v", err)
	}
	plaintext := []byte("  task-123\n")
	ciphertext, err := box.SealAnonymous(nil, plaintext, &curvePublicKey, rand.Reader)
	if err != nil {
		t.Fatalf("box.SealAnonymous() error = %v", err)
	}

	got, err := decryptAgentTaskID(
		AgentIdentityKey{PrivateKeyPKCS8Base64: material.PrivateKeyPKCS8Base64},
		base64.StdEncoding.EncodeToString(ciphertext),
	)
	if err != nil {
		t.Fatalf("decryptAgentTaskID() error = %v", err)
	}
	if got != string(plaintext) {
		t.Fatalf("decryptAgentTaskID() = %q, want %q", got, plaintext)
	}
}

func TestDecryptAgentTaskIDRejectsInvalidUTF8(t *testing.T) {
	material := deterministicAgentKeyMaterial(t, 0x33)
	privateKey, err := ParseAgentIdentityPrivateKey(material.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("ParseAgentIdentityPrivateKey() error = %v", err)
	}
	curvePublicKey, _, err := curve25519KeyPair(privateKey)
	if err != nil {
		t.Fatalf("curve25519KeyPair() error = %v", err)
	}
	ciphertext, err := box.SealAnonymous(nil, []byte{0xff}, &curvePublicKey, rand.Reader)
	if err != nil {
		t.Fatalf("box.SealAnonymous() error = %v", err)
	}

	_, err = decryptAgentTaskID(
		AgentIdentityKey{PrivateKeyPKCS8Base64: material.PrivateKeyPKCS8Base64},
		base64.StdEncoding.EncodeToString(ciphertext),
	)
	if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("decryptAgentTaskID() error = %v", err)
	}
}

func TestDecryptAgentTaskIDAcceptsEmptyOpaqueValue(t *testing.T) {
	material := deterministicAgentKeyMaterial(t, 0x44)
	privateKey, err := ParseAgentIdentityPrivateKey(material.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("ParseAgentIdentityPrivateKey() error = %v", err)
	}
	curvePublicKey, _, err := curve25519KeyPair(privateKey)
	if err != nil {
		t.Fatalf("curve25519KeyPair() error = %v", err)
	}
	ciphertext, err := box.SealAnonymous(nil, nil, &curvePublicKey, rand.Reader)
	if err != nil {
		t.Fatalf("box.SealAnonymous() error = %v", err)
	}

	got, err := decryptAgentTaskID(
		AgentIdentityKey{PrivateKeyPKCS8Base64: material.PrivateKeyPKCS8Base64},
		base64.StdEncoding.EncodeToString(ciphertext),
	)
	if err != nil {
		t.Fatalf("decryptAgentTaskID() error = %v", err)
	}
	if got != "" {
		t.Fatalf("decryptAgentTaskID() = %q, want empty value", got)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
