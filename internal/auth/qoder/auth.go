// Package qoder provides OAuth2 authentication functionality for the Qoder provider.
package qoder

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	log "github.com/sirupsen/logrus"
)

// UserStatusResponse represents the response from the user status endpoint.
type UserStatusResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// QoderAuth handles Qoder PKCE + URI-scheme authentication.
type QoderAuth struct {
	httpClient *http.Client
}

// NewQoderAuth creates a new Qoder auth service.
func NewQoderAuth(httpClient *http.Client) *QoderAuth {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &QoderAuth{httpClient: httpClient}
}

// GeneratePKCE generates a PKCE verifier/challenge pair and a nonce.
func GeneratePKCE() (nonce, challenge, verifier string, err error) {
	// Generate 32-byte random verifier
	verifierBytes := make([]byte, 32)
	if _, err = rand.Read(verifierBytes); err != nil {
		return "", "", "", fmt.Errorf("qoder: generate verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)

	// SHA-256 challenge
	challengeHash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(challengeHash[:])

	// Nonce
	nonceBytes := make([]byte, 16)
	if _, err = rand.Read(nonceBytes); err != nil {
		return "", "", "", fmt.Errorf("qoder: generate nonce: %w", err)
	}
	nonce = fmt.Sprintf("%x", nonceBytes)

	return nonce, challenge, verifier, nil
}

// BuildAuthURL constructs the Qoder login URL for browser-based authentication.
func BuildAuthURL(nonce, challenge, machineID string) string {
	params := url.Values{}
	params.Set("nonce", nonce)
	params.Set("challenge", challenge)
	params.Set("challenge_method", "S256")
	params.Set("redirect_uri", RedirectURI)
	params.Set("machine_id", machineID)
	return AuthBase + SelectAccountsPath + "?" + params.Encode()
}

// FetchUserStatus retrieves user info using a device token.
func (o *QoderAuth) FetchUserStatus(deviceToken string) (*UserStatusResponse, error) {
	deviceToken = strings.TrimSpace(deviceToken)
	if deviceToken == "" {
		return nil, fmt.Errorf("qoder user status: missing device token")
	}
	reqURL := OpenAPIBase + UserStatusPath
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder user status: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+deviceToken)
	req.Header.Set("Cosy-Version", IDEVersion)
	req.Header.Set("Cosy-Clienttype", "0")

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("qoder user status: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder user status: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return nil, fmt.Errorf("qoder user status: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		if body == "" {
			return nil, fmt.Errorf("qoder user status: request failed: status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("qoder user status: request failed: status %d: %s", resp.StatusCode, body)
	}

	var user UserStatusResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&user); errDecode != nil {
		return nil, fmt.Errorf("qoder user status: decode response: %w", errDecode)
	}
	return &user, nil
}

// DecodeAuthField decodes the obfuscated auth callback field to extract user info.
func DecodeAuthField(authStr string) (map[string]any, error) {
	if strings.TrimSpace(authStr) == "" {
		return nil, fmt.Errorf("qoder: empty auth field")
	}

	// Reverse custom alphabet to standard base64
	var b64 strings.Builder
	for _, c := range authStr {
		ch := string(c)
		if ch == CustomPad {
			b64.WriteByte('=')
		} else {
			idx := strings.Index(CustomAlphabet, ch)
			if idx >= 0 {
				b64.WriteByte(StdAlphabet[idx])
			} else {
				b64.WriteString(ch)
			}
		}
	}

	decoded := b64.String()

	// Find the base64-encoded JSON payload starting with "eyJ"
	eqPos := strings.Index(decoded, "=")
	var head, tail string
	if eqPos < 0 {
		head = decoded
		tail = ""
	} else {
		tail = decoded[:eqPos]
		head = decoded[eqPos+1:]
	}

	eyjPos := strings.Index(head, "eyJ")
	var reconstructed string
	if eyjPos < 0 {
		eyjFull := strings.Index(decoded, "eyJ")
		if eyjFull < 0 {
			return nil, fmt.Errorf("qoder: no JWT payload found in auth field")
		}
		reconstructed = decoded[eyjFull:]
	} else {
		reconstructed = head[eyjPos:] + head[:eyjPos] + tail + "="
	}

	// Try decoding with different padding
	for _, pad := range []string{"", "=", "==", "==="} {
		raw, errDec := base64.StdEncoding.DecodeString(reconstructed + pad)
		if errDec != nil {
			raw, errDec = base64.RawStdEncoding.DecodeString(reconstructed + pad)
			if errDec != nil {
				continue
			}
		}
		var result map[string]any
		if errJSON := json.Unmarshal(raw, &result); errJSON != nil {
			continue
		}
		return result, nil
	}

	return nil, fmt.Errorf("qoder: failed to decode auth field")
}

// GenerateMachineID creates a deterministic machine identifier.
func GenerateMachineID(hostname, macAddr, system, machine string) string {
	raw := fmt.Sprintf("%s-%s-%s-%s", hostname, macAddr, system, machine)
	digest := sha256.Sum256([]byte(raw))
	encoded := base64.RawURLEncoding.EncodeToString(digest[:])
	var parts []string
	for i := 0; i < len(encoded); i += 22 {
		end := i + 22
		if end > len(encoded) {
			end = len(encoded)
		}
		parts = append(parts, encoded[i:end])
	}
	return strings.Join(parts, "-")
}
