package qoder

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const qoderRSAPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

const (
	qoderCustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	qoderStdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
)

var (
	cosyPublicKeyOnce sync.Once
	cosyPublicKey     *rsa.PublicKey
	cosyPublicKeyErr  error
	qoderEncodeMap    [128]byte
)

func init() {
	for index := range qoderStdAlphabet {
		qoderEncodeMap[qoderStdAlphabet[index]] = qoderCustomAlphabet[index]
	}
	qoderEncodeMap['='] = '$'
}

type authIdentityPayload struct {
	Name               string `json:"name"`
	AID                string `json:"aid"`
	UID                string `json:"uid"`
	YxUID              string `json:"yx_uid"`
	OrganizationID     string `json:"organization_id"`
	OrganizationName   string `json:"organization_name"`
	UserType           string `json:"user_type"`
	SecurityOAuthToken string `json:"security_oauth_token"`
	RefreshToken       string `json:"refresh_token"`
}

type cosyPayload struct {
	CosyVersion string `json:"cosyVersion"`
	IDEVersion  string `json:"ideVersion"`
	Info        string `json:"info"`
	RequestID   string `json:"requestId"`
	Version     string `json:"version"`
}

// SignRequest applies COSY authentication using the exact bytes sent on the wire.
func SignRequest(req *http.Request, body []byte, storage *TokenStorage) error {
	return signRequestAt(req, body, storage, time.Now())
}

func signRequestAt(req *http.Request, body []byte, storage *TokenStorage, now time.Time) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("qoder: signed request is missing")
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if storage == nil || storage.UserID == "" || storage.AccessToken == "" || storage.MachineID == "" {
		return fmt.Errorf("qoder: signing credential is incomplete")
	}
	identity, err := MarshalWire(authIdentityPayload{
		Name:               storage.Name,
		UID:                storage.UserID,
		OrganizationID:     storage.OrganizationID,
		OrganizationName:   storage.OrganizationName,
		SecurityOAuthToken: storage.AccessToken,
		RefreshToken:       storage.RefreshToken,
	})
	if err != nil {
		return fmt.Errorf("qoder: marshal signing identity: %w", err)
	}
	// The gateway decrypts the identity with a printable key; arbitrary bytes are
	// rejected as "Signature invalid". The official client uses uuid[:16].
	aesKey := newCOSYKey()
	info, err := encryptAESBase64(identity, aesKey)
	if err != nil {
		return err
	}
	cosyKey, err := encryptRSABase64(aesKey)
	if err != nil {
		return err
	}
	payload, err := MarshalWire(cosyPayload{
		CosyVersion: ClientVersion,
		Info:        info,
		RequestID:   uuid.NewString(),
		Version:     "v1",
	})
	if err != nil {
		return fmt.Errorf("qoder: marshal COSY payload: %w", err)
	}
	payloadB64 := base64.StdEncoding.EncodeToString(payload)
	date := strconv.FormatInt(now.Unix(), 10)
	sigPath := signingPath(req.URL)
	signatureInput := payloadB64 + "\n" + cosyKey + "\n" + date + "\n" + string(body) + "\n" + sigPath
	signature := md5Hex([]byte(signatureInput))

	req.Header.Set("Authorization", "Bearer COSY."+payloadB64+"."+signature)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "undici")
	req.Header.Set("Login-Version", "v2")
	req.Header.Set("X-Request-Id", uuid.NewString())
	req.Header.Set("Cosy-Key", cosyKey)
	req.Header.Set("Cosy-User", storage.UserID)
	req.Header.Set("Cosy-Date", date)
	req.Header.Set("Cosy-Version", ClientVersion)
	req.Header.Set("Cosy-Machineid", storage.MachineID)
	req.Header.Set("Cosy-Machinetoken", firstNonEmpty(storage.MachineToken, storage.MachineID))
	req.Header.Set("Cosy-Machinetype", firstNonEmpty(storage.MachineType, "5"))
	req.Header.Set("Cosy-Machineos", machineOS())
	req.Header.Set("Cosy-Machinehostname", machineHostname(storage.UserID))
	req.Header.Set("Cosy-Clienttype", "5")
	req.Header.Set("Cosy-Clientip", "169.254.198.161")
	req.Header.Set("Cosy-Bodyhash", md5Hex(body))
	req.Header.Set("Cosy-Bodylength", strconv.Itoa(len(body)))
	req.Header.Set("Cosy-Sigpath", sigPath)
	req.Header.Set("Cosy-Business-Product", "cli")
	req.Header.Set("Cosy-Business-Type", "agent")
	req.Header.Set("Cosy-Scene", "assistant")
	if storage.DataPolicyAgreed {
		req.Header.Set("Cosy-Data-Policy", "agree")
	} else {
		req.Header.Set("Cosy-Data-Policy", "disagree")
	}
	if storage.OrganizationID != "" {
		req.Header.Set("Cosy-Organization-Id", storage.OrganizationID)
	}
	if len(storage.OrganizationTags) > 0 {
		req.Header.Set("Cosy-Organization-Tags", strings.Join(storage.OrganizationTags, ","))
	}
	return nil
}

func newCOSYKey() []byte {
	return []byte(uuid.NewString()[:16])
}

// EncodeBody applies Qoder's base64 rearrangement and alphabet substitution.
func EncodeBody(plaintext []byte) string {
	standard := base64.StdEncoding.EncodeToString(plaintext)
	length := len(standard)
	third := length / 3
	rearranged := standard[length-third:] + standard[third:length-third] + standard[:third]
	encoded := make([]byte, length)
	for index := range encoded {
		char := rearranged[index]
		if char < 128 {
			encoded[index] = qoderEncodeMap[char]
		}
	}
	return string(encoded)
}

// MarshalWire emits compact JSON without HTML escaping, matching the official client.
func MarshalWire(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func signingPath(parsed *url.URL) string {
	path := parsed.Path
	if strings.HasPrefix(path, "/algo") {
		path = strings.TrimPrefix(path, "/algo")
	}
	return path
}

func encryptAESBase64(plaintext, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("qoder: initialize AES: %w", err)
	}
	padding := block.BlockSize() - len(plaintext)%block.BlockSize()
	padded := make([]byte, len(plaintext)+padding)
	copy(padded, plaintext)
	for index := len(plaintext); index < len(padded); index++ {
		padded[index] = byte(padding)
	}
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:block.BlockSize()]).CryptBlocks(encrypted, padded)
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func encryptRSABase64(key []byte) (string, error) {
	cosyPublicKeyOnce.Do(func() {
		block, _ := pem.Decode([]byte(qoderRSAPublicKey))
		if block == nil {
			cosyPublicKeyErr = fmt.Errorf("qoder: invalid COSY public key")
			return
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			cosyPublicKeyErr = fmt.Errorf("qoder: parse COSY public key: %w", err)
			return
		}
		publicKey, ok := parsed.(*rsa.PublicKey)
		if !ok {
			cosyPublicKeyErr = fmt.Errorf("qoder: COSY key is not RSA")
			return
		}
		cosyPublicKey = publicKey
	})
	if cosyPublicKeyErr != nil {
		return "", cosyPublicKeyErr
	}
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, cosyPublicKey, key)
	if err != nil {
		return "", fmt.Errorf("qoder: encrypt COSY key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func md5Hex(data []byte) string {
	// MD5 is mandated by Qoder's COSY wire signature, not used for password storage.
	digest := md5.Sum(data)
	return hex.EncodeToString(digest[:])
}

func machineOS() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	return arch + "_" + runtime.GOOS
}

func machineHostname(uid string) string {
	return "DESKTOP-" + strings.ToUpper(md5Hex([]byte("hostname:" + uid))[:7])
}
