package qoder

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEncodeBodyRoundTripKnownProtocol(t *testing.T) {
	for _, input := range [][]byte{
		{},
		[]byte("hi"),
		[]byte(`{"messages":[{"role":"user","content":"你好 🚀"}]}`),
		bytes.Repeat([]byte{0, 1, 2, 0xfe, 0xff}, 32),
	} {
		encoded := EncodeBody(input)
		decoded, err := decodeQoderForTest(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, input) {
			t.Fatalf("round trip mismatch: %x != %x", decoded, input)
		}
	}
}

func TestSignRequestVerifiesExactWireBody(t *testing.T) {
	body := []byte("encoded-body")
	req, err := http.NewRequest(http.MethodPost, "https://api2.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?Encode=1", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC)
	storage := &TokenStorage{
		Region: RegionGlobal, AccessToken: "dt-test", RefreshToken: "drt-test", UserID: "user-test", MachineID: "machine-test", OrganizationID: "org-test", OrganizationTags: []string{"a", "b"},
	}
	if err := signRequestAt(req, body, storage, now); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(req.Header.Get("Authorization"), ".")
	if len(parts) != 3 || parts[0] != "Bearer COSY" {
		t.Fatalf("Authorization shape = %q", req.Header.Get("Authorization"))
	}
	payload, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["cosyVersion"] != ClientVersion || decoded["version"] != "v1" || decoded["info"] == "" || decoded["requestId"] == "" {
		t.Fatalf("COSY payload = %v", decoded)
	}
	input := strings.Join([]string{
		parts[1], req.Header.Get("Cosy-Key"), req.Header.Get("Cosy-Date"), string(body), "/api/v2/service/pro/sse/agent_chat_generation",
	}, "\n")
	expected := fmt.Sprintf("%x", md5.Sum([]byte(input)))
	if parts[2] != expected {
		t.Fatalf("signature = %q, want %q", parts[2], expected)
	}
	if req.Header.Get("Cosy-Date") != fmt.Sprintf("%d", now.Unix()) {
		t.Fatalf("Cosy-Date = %q", req.Header.Get("Cosy-Date"))
	}
	if req.Header.Get("Cosy-Bodyhash") != fmt.Sprintf("%x", md5.Sum(body)) || req.Header.Get("Cosy-Bodylength") != "12" {
		t.Fatalf("body digest headers = %q/%q", req.Header.Get("Cosy-Bodyhash"), req.Header.Get("Cosy-Bodylength"))
	}
	if req.Header.Get("Cosy-Organization-Id") != "org-test" || req.Header.Get("Cosy-Organization-Tags") != "a,b" {
		t.Fatal("organization identity was not preserved")
	}
}

func TestMarshalWireDoesNotHTMLEscape(t *testing.T) {
	body, err := MarshalWire(map[string]string{"text": "<tag>&中文"})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"text":"<tag>&中文"}` {
		t.Fatalf("body = %s", body)
	}
}

func decodeQoderForTest(encoded string) ([]byte, error) {
	mapped := make([]byte, len(encoded))
	for index := range encoded {
		char := encoded[index]
		switch {
		case char == '$':
			mapped[index] = '='
		default:
			position := strings.IndexByte(qoderCustomAlphabet, char)
			if position < 0 {
				return nil, fmt.Errorf("invalid character %q", char)
			}
			mapped[index] = qoderStdAlphabet[position]
		}
	}
	length := len(mapped)
	third := length / 3
	standard := make([]byte, length)
	copy(standard[:third], mapped[length-third:])
	copy(standard[third:length-third], mapped[third:length-third])
	copy(standard[length-third:], mapped[:third])
	return base64.StdEncoding.DecodeString(string(standard))
}
