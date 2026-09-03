package cursor

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// APIHost is the Cursor AIServer origin.
	APIHost = "https://api2.cursor.sh"
	// apiHostHeader is the Host header Cursor's own client sends.
	apiHostHeader = "api2.cursor.sh"

	// ChatEndpoint is the Connect-RPC path for a unified chat stream.
	ChatEndpoint = APIHost + "/aiserver.v1.ChatService/StreamUnifiedChatWithTools"
	// AvailableModelsEndpoint lists the models the account may use.
	AvailableModelsEndpoint = APIHost + "/aiserver.v1.AiService/AvailableModels"

	// DefaultClientVersion is the Cursor client version reported to the API.
	// Cursor rejects releases it no longer supports with ERROR_OUTDATED_CLIENT,
	// so this has to track the current release from
	// https://api2.cursor.sh/updates/api/update/linux-x64/cursor/0.0.0/stable.
	DefaultClientVersion = "3.17.8"

	// UserAgent is the Cursor editor user agent used by the login endpoints.
	UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Cursor/0.48.6 Chrome/132.0.6834.210 Electron/34.3.4 Safari/537.36"
)

// Hashed64Hex returns the sha256 hex digest of input+salt.
// Used for x-client-key and the machine IDs inside the checksum.
func Hashed64Hex(input, salt string) string {
	h := sha256.Sum256([]byte(input + salt))
	return hex.EncodeToString(h[:])
}

// obfuscateBytes mirrors the byte obfuscation from the Cursor client used inside
// the x-cursor-checksum value.
func obfuscateBytes(b []byte) {
	t := byte(165)
	for i := range b {
		b[i] = (b[i] ^ t) + byte(i%256)
		t = b[i]
	}
}

// Checksum builds the x-cursor-checksum header value for the given token.
//
// The timestamp block must match the Cursor client byte for byte or the API
// answers every chat request with ERROR_UNAUTHORIZED. Two details are easy to
// get wrong: the counter is floor(unixMillis/1e6) — a ~16.7 minute tick, not a
// second counter — and the six bytes are not a plain big-endian encoding, they
// repeat the low half of the counter in the client's own order.
func Checksum(token string) string {
	machineID := Hashed64Hex(token, "machineId")
	macMachineID := Hashed64Hex(token, "macMachineId")

	ts := uint32(time.Now().UnixMilli() / 1e6)
	b := []byte{
		byte(ts >> 8),
		byte(ts),
		byte(ts >> 24),
		byte(ts >> 16),
		byte(ts >> 8),
		byte(ts),
	}
	obfuscateBytes(b)
	return base64.StdEncoding.EncodeToString(b) + machineID + "/" + macMachineID
}

// uuidV5 replicates uuid v5 (SHA-1, name-based, RFC 4122 §4.3), which the Cursor
// client uses to derive x-session-id.
func uuidV5(namespace uuid.UUID, name string) uuid.UUID {
	h := sha1.New()
	h.Write(namespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)
	var u uuid.UUID
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50 // version 5
	u[8] = (u[8] & 0x3f) | 0x80 // variant RFC4122
	return u
}

// SessionID derives x-session-id as uuidv5(authToken, uuidv5.DNS).
func SessionID(token string) string {
	dns := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	return uuidV5(dns, token).String()
}

// NormalizeToken extracts the raw access token from a Cursor cookie. Cookies come
// in the `userId::token` and url-encoded `userId%3A%3Atoken` shapes; a bare token
// is returned unchanged.
func NormalizeToken(token string) string {
	token = strings.TrimSpace(token)
	if idx := strings.Index(token, "%3A%3A"); idx >= 0 {
		token = token[idx+len("%3A%3A"):]
	} else if idx := strings.Index(token, "::"); idx >= 0 {
		token = token[idx+len("::"):]
	}
	return strings.TrimSpace(token)
}

// ApplyHeaders sets the Cursor client headers on r. streaming selects the
// Connect streaming content type used by the chat endpoint; the unary form is
// used by AvailableModels.
func ApplyHeaders(r *http.Request, token string, streaming bool) {
	ApplyHeadersWithHost(r, token, streaming, apiHostHeader)
}

// ApplyHeadersWithHost is the same as ApplyHeaders but allows overriding the Host
// header, which is useful for tests or alternate Cursor API gateways.
func ApplyHeadersWithHost(r *http.Request, token string, streaming bool, host string) {
	token = NormalizeToken(token)
	r.Header.Set("authorization", "Bearer "+token)
	r.Header.Set("connect-protocol-version", "1")
	r.Header.Set("user-agent", "connect-es/1.6.1")
	r.Header.Set("x-cursor-checksum", Checksum(token))
	r.Header.Set("x-cursor-client-version", DefaultClientVersion)
	r.Header.Set("x-cursor-config-version", uuid.NewString())
	r.Header.Set("x-cursor-timezone", "Asia/Shanghai")
	r.Header.Set("x-ghost-mode", "true")
	if host != "" {
		r.Host = host
	}
	if streaming {
		r.Header.Set("connect-accept-encoding", "gzip")
		r.Header.Set("connect-content-encoding", "gzip")
		r.Header.Set("content-type", "application/connect+proto")
		r.Header.Set("x-amzn-trace-id", "Root="+uuid.NewString())
		r.Header.Set("x-client-key", Hashed64Hex(token, ""))
		r.Header.Set("x-request-id", uuid.NewString())
		r.Header.Set("x-session-id", SessionID(token))
		return
	}
	r.Header.Set("accept-encoding", "gzip")
	r.Header.Set("content-type", "application/proto")
}
