package wstunnel

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestValidateHandshakeParams(t *testing.T) {
	t.Run("role normalization and validation", func(t *testing.T) {
		roleClient, errClient := NormalizeAndValidateRole("  CLIENT ")
		if errClient != nil || roleClient != RoleClient {
			t.Fatalf("NormalizeAndValidateRole(CLIENT) = (%q, %v), want (%q, nil)", roleClient, errClient, RoleClient)
		}
		roleServer, errServer := NormalizeAndValidateRole("\tSeRvEr\n")
		if errServer != nil || roleServer != RoleServer {
			t.Fatalf("NormalizeAndValidateRole(server) = (%q, %v), want (%q, nil)", roleServer, errServer, RoleServer)
		}
		for _, bad := range []string{"", "   ", "admin", "peer", "client2"} {
			if _, errBad := NormalizeAndValidateRole(bad); errBad == nil {
				t.Fatalf("NormalizeAndValidateRole(%q) succeeded, want error", bad)
			}
		}
	})

	t.Run("uuid validation", func(t *testing.T) {
		for _, okVal := range []string{
			"a",
			"  550e8400-e29b-41d4-a716-446655440000  ",
			strings.Repeat("x", MaxUUIDLen),
		} {
			got, errUUID := NormalizeAndValidateUUID(okVal)
			if errUUID != nil {
				t.Fatalf("NormalizeAndValidateUUID(%q) error = %v", okVal, errUUID)
			}
			if got != strings.TrimSpace(okVal) {
				t.Fatalf("NormalizeAndValidateUUID(%q) = %q, want %q", okVal, got, strings.TrimSpace(okVal))
			}
		}

		for _, badVal := range []string{
			"",
			"   ",
			"has space",
			"has\ttab",
			"has\nnewline",
			"非ASCII",
			"bad\x7fdel",
			strings.Repeat("x", MaxUUIDLen+1),
		} {
			if _, errBad := NormalizeAndValidateUUID(badVal); errBad == nil {
				t.Fatalf("NormalizeAndValidateUUID(%q) succeeded, want error", badVal)
			}
		}
	})

	t.Run("name validation", func(t *testing.T) {
		validNames := []string{
			"  Office Mac Studio  ",
			"研发构建机-01 🚀",
			`Quote "A" \ Slash`,
			strings.Repeat("a", MaxDeviceNameLen),
			strings.Repeat("测", MaxDeviceNameLen/3),
		}
		for _, raw := range validNames {
			got, errName := NormalizeAndValidateName(raw)
			if errName != nil {
				t.Fatalf("NormalizeAndValidateName(%q) error = %v", raw, errName)
			}
			if got != strings.TrimSpace(raw) {
				t.Fatalf("NormalizeAndValidateName(%q) = %q, want %q", raw, got, strings.TrimSpace(raw))
			}
		}

		invalidNames := []string{
			"",
			"    ",
			"\nLeadingNewline",
			"TrailingCR\r",
			"\tTabWrapped\t",
			"line1\nline2",
			"line1\rline2",
			"tab\there",
			"null\x00byte",
			"del\x7fbyte",
			string([]byte{0xff, 0xfe, 0xfd}),
			strings.Repeat("a", MaxDeviceNameLen+1),
		}
		for _, raw := range invalidNames {
			if _, errBad := NormalizeAndValidateName(raw); errBad == nil {
				t.Fatalf("NormalizeAndValidateName(%q) succeeded, want error", raw)
			}
		}
	})

	t.Run("type validation", func(t *testing.T) {
		validTypes := []string{
			"a",
			"测",
			"  cpa-desktop  ",
			"研发服务端类型-01 🚀",
			`Custom "Type" \ v2`,
			strings.Repeat("t", MaxServerTypeLen),
			strings.Repeat("类", MaxServerTypeLen/3),
		}
		for _, raw := range validTypes {
			got, errType := NormalizeAndValidateType(raw)
			if errType != nil {
				t.Fatalf("NormalizeAndValidateType(%q) error = %v", raw, errType)
			}
			if got != strings.TrimSpace(raw) {
				t.Fatalf("NormalizeAndValidateType(%q) = %q, want %q", raw, got, strings.TrimSpace(raw))
			}
		}

		invalidTypes := []string{
			"",
			" ",
			"    ",
			"\u00a0",
			"\u3000",
			"\nLeadingNewline",
			"TrailingCR\r",
			"\tTabWrapped\t",
			"line1\nline2",
			"line1\rline2",
			"tab\there",
			"null\x00byte",
			"del\x7fbyte",
			string([]byte{0xff, 0xfe, 0xfd}),
			strings.Repeat("t", MaxServerTypeLen+1),
		}
		for _, raw := range invalidTypes {
			if _, errBad := NormalizeAndValidateType(raw); errBad == nil || errBad.Error() != "invalid or missing type" {
				t.Fatalf("NormalizeAndValidateType(%q) = %v, want error 'invalid or missing type'", raw, errBad)
			}
		}
	})
}

func TestBinaryDataFrameCodec(t *testing.T) {
	cases := []struct {
		name        string
		payloadType byte
		srcUUID     string
		dstUUID     string
		payload     []byte
	}{
		{
			name:        "empty payload",
			payloadType: PayloadTypeBinary,
			srcUUID:     "cli-1",
			dstUUID:     "srv-1",
			payload:     nil,
		},
		{
			name:        "utf8 text payload",
			payloadType: PayloadTypeText,
			srcUUID:     "550e8400-e29b-41d4-a716-446655440000",
			dstUUID:     "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			payload:     []byte(`{"hello":"世界"}`),
		},
		{
			name:        "arbitrary binary bytes and custom payload type",
			payloadType: 0x42,
			srcUUID:     strings.Repeat("c", MaxUUIDLen),
			dstUUID:     strings.Repeat("s", MaxUUIDLen),
			payload:     []byte{0x00, 0x01, 0x7f, 0x80, 0xfe, 0xff, 0x00},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, errEncode := EncodeDataFrame(tc.payloadType, tc.srcUUID, tc.dstUUID, tc.payload)
			if errEncode != nil {
				t.Fatalf("EncodeDataFrame() error = %v", errEncode)
			}

			view, errCode, errParse := ParseDataFrameHeader(encoded)
			if errParse != nil || errCode != "" {
				t.Fatalf("ParseDataFrameHeader() = (%+v, %q, %v), want nil error", view, errCode, errParse)
			}
			if view.Version != ProtocolVersion1 {
				t.Fatalf("Version = 0x%02x, want 0x%02x", view.Version, ProtocolVersion1)
			}
			if view.PayloadType != tc.payloadType {
				t.Fatalf("PayloadType = 0x%02x, want 0x%02x", view.PayloadType, tc.payloadType)
			}
			if view.SrcUUID != tc.srcUUID || view.DstUUID != tc.dstUUID {
				t.Fatalf("UUIDs = (%q -> %q), want (%q -> %q)", view.SrcUUID, view.DstUUID, tc.srcUUID, tc.dstUUID)
			}
			if !bytes.Equal(view.Payload, tc.payload) {
				t.Fatalf("Payload = %x, want %x", view.Payload, tc.payload)
			}
		})
	}

	t.Run("encode rejects invalid uuids", func(t *testing.T) {
		if _, errEncode := EncodeDataFrame(PayloadTypeBinary, " cli-1 ", "srv-1", []byte("x")); errEncode == nil {
			t.Fatal("EncodeDataFrame with untrimmed srcUUID succeeded, want error")
		}
		if _, errEncode := EncodeDataFrame(PayloadTypeBinary, "cli-1", "", []byte("x")); errEncode == nil {
			t.Fatal("EncodeDataFrame with empty dstUUID succeeded, want error")
		}
	})

	t.Run("malformed binary frames", func(t *testing.T) {
		malformed := []struct {
			name     string
			raw      []byte
			wantCode string
		}{
			{
				name:     "shorter than 4 byte header",
				raw:      []byte{0x01, 0x00, 0x01},
				wantCode: ErrCodeInvalidDataFrame,
			},
			{
				name:     "unsupported protocol version",
				raw:      []byte{0x02, 0x00, 0x01, 0x01, 'a', 'b'},
				wantCode: ErrCodeUnsupportedVersion,
			},
			{
				name:     "zero src uuid length",
				raw:      []byte{0x01, 0x00, 0x00, 0x01, 'b'},
				wantCode: ErrCodeInvalidDataFrame,
			},
			{
				name:     "zero dst uuid length",
				raw:      []byte{0x01, 0x00, 0x01, 0x00, 'a'},
				wantCode: ErrCodeInvalidDataFrame,
			},
			{
				name:     "src uuid length exceeds 128",
				raw:      append([]byte{0x01, 0x00, 129, 0x01}, bytes.Repeat([]byte{'a'}, 130)...),
				wantCode: ErrCodeInvalidDataFrame,
			},
			{
				name:     "truncated uuid bytes",
				raw:      []byte{0x01, 0x00, 0x05, 0x05, 'a', 'b', 'c'},
				wantCode: ErrCodeInvalidDataFrame,
			},
			{
				name:     "non-printable ascii inside uuid",
				raw:      []byte{0x01, 0x00, 0x02, 0x01, 'a', ' ', 'b'},
				wantCode: ErrCodeInvalidDataFrame,
			},
			{
				name:     "payload exceeds 64 MiB limit",
				raw:      append([]byte{0x01, 0x00, 0x01, 0x01, 'a', 'b'}, make([]byte, MaxRawPayloadBytes+1)...),
				wantCode: ErrCodeInvalidDataFrame,
			},
		}

		for _, tc := range malformed {
			t.Run(tc.name, func(t *testing.T) {
				_, code, errParse := ParseDataFrameHeader(tc.raw)
				if errParse == nil {
					t.Fatalf("ParseDataFrameHeader() succeeded, want error code %q", tc.wantCode)
				}
				if code != tc.wantCode {
					t.Fatalf("error code = %q, want %q (err=%v)", code, tc.wantCode, errParse)
				}
			})
		}
	})
}

func TestControlFrameJSONCodec(t *testing.T) {
	t.Run("empty server_list omits request_id and emits empty array", func(t *testing.T) {
		raw, errEncode := EncodeServerListFrame("", nil)
		if errEncode != nil {
			t.Fatalf("EncodeServerListFrame() error = %v", errEncode)
		}
		if string(raw) != `{"type":"server_list","servers":[]}` {
			t.Fatalf("EncodeServerListFrame() = %s, want %s", string(raw), `{"type":"server_list","servers":[]}`)
		}
	})

	t.Run("server_list escapes special characters and preserves request_id", func(t *testing.T) {
		servers := []ServerInfo{
			{UUID: "srv-1", Name: `Node "Alpha" \ 节点`, Type: `Type "Alpha" \ 类型`},
			{UUID: "srv-2", Name: "Node Beta", Type: "x"},
		}
		raw, errEncode := EncodeServerListFrame("req-42", servers)
		if errEncode != nil {
			t.Fatalf("EncodeServerListFrame() error = %v", errEncode)
		}
		if !ojson.ValidBytes(raw) {
			t.Fatalf("invalid json emitted: %s", string(raw))
		}
		parsed := ojson.ParseBytes(raw)
		if got := parsed.Get("type").String(); got != ControlTypeServerList {
			t.Fatalf("type = %q, want %q", got, ControlTypeServerList)
		}
		if got := parsed.Get("request_id").String(); got != "req-42" {
			t.Fatalf("request_id = %q, want req-42", got)
		}
		if got := parsed.Get("servers.#").Int(); got != 2 {
			t.Fatalf("servers count = %d, want 2", got)
		}
		if got := parsed.Get("servers.0.name").String(); got != `Node "Alpha" \ 节点` {
			t.Fatalf("servers.0.name = %q, want %q", got, `Node "Alpha" \ 节点`)
		}
		if got := parsed.Get("servers.0.type").String(); got != `Type "Alpha" \ 类型` {
			t.Fatalf("servers.0.type = %q, want %q", got, `Type "Alpha" \ 类型`)
		}
		if got := parsed.Get("servers.1.type").String(); got != "x" {
			t.Fatalf("servers.1.type = %q, want x", got)
		}
	})

	t.Run("server_online and server_offline events", func(t *testing.T) {
		online, errOnline := EncodeServerEventFrame(ControlTypeServerOnline, ServerInfo{UUID: "s1", Name: "Server One", Type: "cpa-desktop"})
		if errOnline != nil {
			t.Fatalf("EncodeServerEventFrame(online) error = %v", errOnline)
		}
		pOnline := ojson.ParseBytes(online)
		if pOnline.Get("type").String() != ControlTypeServerOnline ||
			pOnline.Get("server.uuid").String() != "s1" ||
			pOnline.Get("server.name").String() != "Server One" ||
			pOnline.Get("server.type").String() != "cpa-desktop" {
			t.Fatalf("unexpected online frame: %s", string(online))
		}

		offline, errOffline := EncodeServerEventFrame(ControlTypeServerOffline, ServerInfo{UUID: "s1", Name: "Server One", Type: "cpa-desktop"})
		if errOffline != nil {
			t.Fatalf("EncodeServerEventFrame(offline) error = %v", errOffline)
		}
		pOffline := ojson.ParseBytes(offline)
		if pOffline.Get("type").String() != ControlTypeServerOffline ||
			pOffline.Get("server.uuid").String() != "s1" ||
			pOffline.Get("server.name").String() != "Server One" ||
			pOffline.Get("server.type").String() != "cpa-desktop" {
			t.Fatalf("unexpected offline frame: %s", string(offline))
		}
	})

	t.Run("error frame optional fields", func(t *testing.T) {
		minimal, errMin := EncodeErrorFrame(ErrCodeInvalidControlFrame, "bad json", "", "")
		if errMin != nil {
			t.Fatalf("EncodeErrorFrame(minimal) error = %v", errMin)
		}
		pMin := ojson.ParseBytes(minimal)
		if pMin.Get("target_uuid").Exists() || pMin.Get("request_id").Exists() {
			t.Fatalf("expected optional fields omitted, got %s", string(minimal))
		}

		full, errFull := EncodeErrorFrame(ErrCodeTargetNotFound, "peer offline", "srv-9", "req-9")
		if errFull != nil {
			t.Fatalf("EncodeErrorFrame(full) error = %v", errFull)
		}
		pFull := ojson.ParseBytes(full)
		if pFull.Get("code").String() != ErrCodeTargetNotFound || pFull.Get("target_uuid").String() != "srv-9" || pFull.Get("request_id").String() != "req-9" {
			t.Fatalf("unexpected full error frame: %s", string(full))
		}
	})

	t.Run("parse incoming control frames", func(t *testing.T) {
		req, code, errParse := ParseControlFrame([]byte(`{"type":"list_servers","request_id":"r-1"}`))
		if errParse != nil || code != "" {
			t.Fatalf("ParseControlFrame(valid) = (%+v, %q, %v)", req, code, errParse)
		}
		if req.Type != ControlTypeListServers || req.RequestID != "r-1" {
			t.Fatalf("unexpected ControlRequest: %+v", req)
		}

		badInputs := [][]byte{
			nil,
			[]byte("not-json"),
			[]byte("[]"),
			[]byte(`{}`),
			[]byte(`{"type":"unknown_cmd"}`),
			[]byte("{\"type\":\"list_servers\",\"extra\":\"\xff\"}"),
			[]byte("{\"type\":\"list_servers\",\"\xff\":\"ignored\"}"),
			[]byte("{\"type\":\"list_servers\",\"extra\":{\"name\":\"\xc0\xaf\"}}"),
			[]byte(`{"type":"list_servers","request_id":"` + strings.Repeat("a", MaxRequestIDLen+1) + `"}`),
			[]byte("{\"type\":\"list_servers\",\"request_id\":\"bad\\u0000id\"}"),
		}
		for _, bad := range badInputs {
			if _, badCode, errBad := ParseControlFrame(bad); errBad == nil || badCode != ErrCodeInvalidControlFrame {
				t.Fatalf("ParseControlFrame(%q) = (%q, %v), want (%q, non-nil)", string(bad), badCode, errBad, ErrCodeInvalidControlFrame)
			}
		}
	})
}
