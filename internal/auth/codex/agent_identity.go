package codex

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

const sshEd25519KeyType = "ssh-ed25519"

const (
	agentIdentityKeySeedBytes         = 64
	agentIdentityKeyDerivationContext = "codex-agent-identity-ed25519-v1"
)

// AgentKeyMaterial contains a registered agent's durable private key and its public form.
type AgentKeyMaterial struct {
	PrivateKeyPKCS8Base64 string
	PublicKeySSH          string
}

// AgentIdentityKey identifies the durable signing material used for task registration.
type AgentIdentityKey struct {
	AgentRuntimeID        string
	PrivateKeyPKCS8Base64 string
}

// GenerateAgentKeyMaterial creates an Ed25519 key pair in Codex-compatible encodings.
func GenerateAgentKeyMaterial() (AgentKeyMaterial, error) {
	return generateAgentKeyMaterial(rand.Reader)
}

func generateAgentKeyMaterial(random io.Reader) (AgentKeyMaterial, error) {
	seedMaterial := make([]byte, agentIdentityKeySeedBytes)
	if _, err := io.ReadFull(random, seedMaterial); err != nil {
		return AgentKeyMaterial{}, fmt.Errorf("generate Agent Identity key: %w", err)
	}
	digest := sha512.New()
	_, _ = digest.Write([]byte(agentIdentityKeyDerivationContext))
	_, _ = digest.Write(seedMaterial)
	privateKey := ed25519.NewKeyFromSeed(digest.Sum(nil)[:ed25519.SeedSize])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return AgentKeyMaterial{}, fmt.Errorf("marshal Agent Identity private key: %w", err)
	}
	return AgentKeyMaterial{
		PrivateKeyPKCS8Base64: base64.StdEncoding.EncodeToString(privateKeyDER),
		PublicKeySSH:          encodeSSHEd25519PublicKey(publicKey),
	}, nil
}

// ParseAgentIdentityPrivateKey decodes a standard-base64 PKCS#8 Ed25519 private key.
func ParseAgentIdentityPrivateKey(encoded string) (ed25519.PrivateKey, error) {
	privateKeyDER, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("Agent Identity private key is not valid standard base64")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(privateKeyDER)
	if err != nil {
		return nil, errors.New("Agent Identity private key is not valid PKCS#8")
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("Agent Identity private key is not Ed25519")
	}
	return append(ed25519.PrivateKey(nil), privateKey...), nil
}

// PublicKeySSHFromPrivateKey derives the OpenSSH public key for a stored private key.
func PublicKeySSHFromPrivateKey(privateKeyPKCS8Base64 string) (string, error) {
	privateKey, err := ParseAgentIdentityPrivateKey(privateKeyPKCS8Base64)
	if err != nil {
		return "", err
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return "", errors.New("Agent Identity public key is not Ed25519")
	}
	return encodeSSHEd25519PublicKey(publicKey), nil
}

func validateAgentKeyMaterial(keyMaterial AgentKeyMaterial) error {
	if keyMaterial.PrivateKeyPKCS8Base64 == "" {
		return errors.New("Agent Identity private key is missing")
	}
	if keyMaterial.PublicKeySSH == "" {
		return errors.New("Agent Identity public key is missing")
	}
	derivedPublicKey, err := PublicKeySSHFromPrivateKey(keyMaterial.PrivateKeyPKCS8Base64)
	if err != nil {
		return err
	}
	if derivedPublicKey != keyMaterial.PublicKeySSH {
		return errors.New("Agent Identity public and private keys do not match")
	}
	return nil
}

func encodeSSHEd25519PublicKey(publicKey ed25519.PublicKey) string {
	blob := make([]byte, 0, 4+len(sshEd25519KeyType)+4+ed25519.PublicKeySize)
	blob = appendSSHString(blob, []byte(sshEd25519KeyType))
	blob = appendSSHString(blob, publicKey)
	return sshEd25519KeyType + " " + base64.StdEncoding.EncodeToString(blob)
}

func appendSSHString(destination []byte, value []byte) []byte {
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(value)))
	destination = append(destination, length...)
	return append(destination, value...)
}

func signAgentTaskRegistration(key AgentIdentityKey, timestamp string) (string, error) {
	runtimeID := key.AgentRuntimeID
	if runtimeID == "" {
		return "", errors.New("Agent Identity runtime ID is missing")
	}
	if timestamp == "" {
		return "", errors.New("Agent Identity task registration timestamp is missing")
	}
	privateKey, err := ParseAgentIdentityPrivateKey(key.PrivateKeyPKCS8Base64)
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(privateKey, []byte(runtimeID+":"+timestamp))
	return base64.StdEncoding.EncodeToString(signature), nil
}

func decryptAgentTaskID(key AgentIdentityKey, encoded string) (string, error) {
	privateKey, err := ParseAgentIdentityPrivateKey(key.PrivateKeyPKCS8Base64)
	if err != nil {
		return "", err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("encrypted Agent Identity task ID is not valid standard base64")
	}
	curvePublicKey, curvePrivateKey, err := curve25519KeyPair(privateKey)
	if err != nil {
		return "", err
	}
	plaintext, ok := box.OpenAnonymous(nil, ciphertext, &curvePublicKey, &curvePrivateKey)
	if !ok {
		return "", errors.New("failed to decrypt Agent Identity task ID")
	}
	if !utf8.Valid(plaintext) {
		return "", errors.New("decrypted Agent Identity task ID is not valid UTF-8")
	}
	return string(plaintext), nil
}

func curve25519KeyPair(privateKey ed25519.PrivateKey) ([32]byte, [32]byte, error) {
	digest := sha512.Sum512(privateKey.Seed())
	var curvePrivateKey [32]byte
	copy(curvePrivateKey[:], digest[:32])
	curvePrivateKey[0] &= 248
	curvePrivateKey[31] &= 127
	curvePrivateKey[31] |= 64
	publicKeyBytes, err := curve25519.X25519(curvePrivateKey[:], curve25519.Basepoint)
	if err != nil {
		return [32]byte{}, [32]byte{}, errors.New("failed to derive Agent Identity Curve25519 public key")
	}
	var curvePublicKey [32]byte
	copy(curvePublicKey[:], publicKeyBytes)
	return curvePublicKey, curvePrivateKey, nil
}
