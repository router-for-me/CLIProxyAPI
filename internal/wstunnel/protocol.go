// Package wstunnel implements an access.api-keys authenticated, multi-tenant,
// many-to-many WebSocket tunnel between client and server devices.
package wstunnel

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
)

// Role identifies the declared endpoint role of a connected tunnel device.
type Role string

const (
	// RoleClient identifies a client device that discovers and exchanges packets with servers.
	RoleClient Role = "client"
	// RoleServer identifies a server device that accepts and sends packets with clients.
	RoleServer Role = "server"
)

const (
	// ProtocolVersion1 is the binary data-frame wire format version.
	ProtocolVersion1 byte = 0x01

	// PayloadTypeBinary indicates an opaque binary payload.
	PayloadTypeBinary byte = 0x00
	// PayloadTypeText indicates a UTF-8 text payload.
	PayloadTypeText byte = 0x01

	// MinBinaryFrameHeaderLen is the fixed prefix length before SrcUUID and DstUUID bytes.
	MinBinaryFrameHeaderLen = 4
	// MaxUUIDLen is the maximum byte length of a device UUID.
	MaxUUIDLen = 128
	// MaxDeviceNameLen is the maximum UTF-8 byte length of a device name.
	MaxDeviceNameLen = 256
	// MaxServerTypeLen is the maximum UTF-8 byte length of a server type.
	MaxServerTypeLen = 256
	// MaxControlFrameBytes caps inbound JSON text messages before allocation and parsing.
	MaxControlFrameBytes = 4096
	// MaxControlTypeLen caps the byte length of an inbound control type.
	MaxControlTypeLen = 64
	// MaxRequestIDLen is the maximum byte length of an optional control request_id.
	MaxRequestIDLen = 128
	// MaxRawPayloadBytes is the maximum raw business payload size per binary data frame (64 MiB).
	MaxRawPayloadBytes = 64 << 20
	// MaxInboundFrameBytes is the WebSocket read limit covering header overhead plus MaxRawPayloadBytes.
	MaxInboundFrameBytes = MaxRawPayloadBytes + 512
)

const (
	// ControlTypeServerList delivers the online server snapshot to a client.
	ControlTypeServerList = "server_list"
	// ControlTypeListServers requests an up-to-date server_list snapshot from a client.
	ControlTypeListServers = "list_servers"
	// ControlTypeServerOnline notifies online clients that a server joined the tenant realm.
	ControlTypeServerOnline = "server_online"
	// ControlTypeServerOffline notifies online clients that a server left the tenant realm.
	ControlTypeServerOffline = "server_offline"
	// ControlTypeError reports a non-fatal protocol or routing error to the sender.
	ControlTypeError = "error"
)

const (
	// ErrCodeInvalidControlFrame indicates malformed JSON or an unsupported control message.
	ErrCodeInvalidControlFrame = "invalid_control_frame"
	// ErrCodeUnsupportedRoleAction indicates a control action not permitted for the peer role.
	ErrCodeUnsupportedRoleAction = "unsupported_role_action"
	// ErrCodeInvalidDataFrame indicates a malformed or out-of-bounds binary data frame.
	ErrCodeInvalidDataFrame = "invalid_data_frame"
	// ErrCodeUnsupportedVersion indicates an unknown binary frame protocol version.
	ErrCodeUnsupportedVersion = "unsupported_version"
	// ErrCodeSourceUUIDMismatch indicates the frame's SrcUUID does not match the authenticated peer UUID.
	ErrCodeSourceUUIDMismatch = "source_uuid_mismatch"
	// ErrCodeTargetNotFound indicates the target DstUUID is not online in the same api-key realm.
	ErrCodeTargetNotFound = "target_not_found"
	// ErrCodeInvalidTargetRole indicates an attempt to route client->client or server->server.
	ErrCodeInvalidTargetRole = "invalid_target_role"
	// ErrCodeTargetBufferFull indicates the target peer's outbound send buffer is full.
	ErrCodeTargetBufferFull = "target_buffer_full"
)

// ServerInfo describes an online server device within the caller's api-key realm.
type ServerInfo struct {
	UUID string
	Name string
	Type string
}

// ControlRequest represents a parsed inbound text control frame.
type ControlRequest struct {
	Type      string
	RequestID string
}

// DataFrameView provides zero-copy slices into a validated binary data frame.
type DataFrameView struct {
	Version     byte
	PayloadType byte
	SrcUUID     string
	DstUUID     string
	Payload     []byte
}

// NormalizeAndValidateRole trims, lowercases, and validates a declared tunnel role.
func NormalizeAndValidateRole(raw string) (Role, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch Role(normalized) {
	case RoleClient:
		return RoleClient, nil
	case RoleServer:
		return RoleServer, nil
	default:
		return "", errors.New("invalid or missing role: must be client or server")
	}
}

// NormalizeAndValidateUUID trims and validates a device UUID (1..128 printable non-space ASCII bytes).
func NormalizeAndValidateUUID(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if !isValidUUIDBytes([]byte(trimmed)) {
		return "", errors.New("invalid or missing uuid")
	}
	return trimmed, nil
}

func isValidUUIDBytes(b []byte) bool {
	if len(b) < 1 || len(b) > MaxUUIDLen {
		return false
	}
	for i := 0; i < len(b); i++ {
		if b[i] < 0x21 || b[i] > 0x7e {
			return false
		}
	}
	return true
}

// NormalizeAndValidateName validates UTF-8 and control-character constraints on raw input,
// then trims surrounding spaces and checks the 1..256 byte length bound.
func NormalizeAndValidateName(raw string) (string, error) {
	if !utf8.ValidString(raw) || hasASCIIControlChar(raw) {
		return "", errors.New("invalid or missing name")
	}
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 1 || len(trimmed) > MaxDeviceNameLen {
		return "", errors.New("invalid or missing name")
	}
	return trimmed, nil
}

// NormalizeAndValidateType validates UTF-8 and control-character constraints on raw input,
// then trims surrounding spaces and checks the 1..256 byte length bound.
func NormalizeAndValidateType(raw string) (string, error) {
	if !utf8.ValidString(raw) || hasASCIIControlChar(raw) {
		return "", errors.New("invalid or missing type")
	}
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 1 || len(trimmed) > MaxServerTypeLen {
		return "", errors.New("invalid or missing type")
	}
	return trimmed, nil
}

func hasASCIIControlChar(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// EncodeDataFrame encapsulates an opaque payload into a tunnel binary frame.
func EncodeDataFrame(payloadType byte, srcUUID, dstUUID string, payload []byte) ([]byte, error) {
	if !isValidUUIDBytes([]byte(srcUUID)) {
		return nil, errors.New("invalid source uuid")
	}
	if !isValidUUIDBytes([]byte(dstUUID)) {
		return nil, errors.New("invalid destination uuid")
	}
	if len(payload) > MaxRawPayloadBytes {
		return nil, fmt.Errorf("payload exceeds maximum size of %d bytes", MaxRawPayloadBytes)
	}

	totalLen := MinBinaryFrameHeaderLen + len(srcUUID) + len(dstUUID) + len(payload)
	buf := make([]byte, 0, totalLen)
	buf = append(buf, ProtocolVersion1, payloadType, byte(len(srcUUID)), byte(len(dstUUID)))
	buf = append(buf, srcUUID...)
	buf = append(buf, dstUUID...)
	buf = append(buf, payload...)
	return buf, nil
}

// ParseDataFrameHeader validates a tunnel binary frame header and returns a zero-copy view.
func ParseDataFrameHeader(rawFrame []byte) (DataFrameView, string, error) {
	if len(rawFrame) < MinBinaryFrameHeaderLen {
		return DataFrameView{}, ErrCodeInvalidDataFrame, errors.New("binary frame shorter than 4-byte header")
	}
	version := rawFrame[0]
	if version != ProtocolVersion1 {
		return DataFrameView{}, ErrCodeUnsupportedVersion, fmt.Errorf("unsupported binary frame version 0x%02x", version)
	}
	payloadType := rawFrame[1]
	srcLen := int(rawFrame[2])
	dstLen := int(rawFrame[3])
	if srcLen < 1 || srcLen > MaxUUIDLen || dstLen < 1 || dstLen > MaxUUIDLen {
		return DataFrameView{}, ErrCodeInvalidDataFrame, errors.New("binary frame uuid length out of bounds")
	}
	headerLen := MinBinaryFrameHeaderLen + srcLen + dstLen
	if len(rawFrame) < headerLen {
		return DataFrameView{}, ErrCodeInvalidDataFrame, errors.New("binary frame truncated before uuid fields")
	}
	payload := rawFrame[headerLen:]
	if len(payload) > MaxRawPayloadBytes {
		return DataFrameView{}, ErrCodeInvalidDataFrame, errors.New("binary frame payload exceeds maximum allowed size")
	}
	srcBytes := rawFrame[MinBinaryFrameHeaderLen : MinBinaryFrameHeaderLen+srcLen]
	dstBytes := rawFrame[MinBinaryFrameHeaderLen+srcLen : headerLen]
	if !isValidUUIDBytes(srcBytes) || !isValidUUIDBytes(dstBytes) {
		return DataFrameView{}, ErrCodeInvalidDataFrame, errors.New("binary frame contains invalid uuid bytes")
	}

	return DataFrameView{
		Version:     version,
		PayloadType: payloadType,
		SrcUUID:     string(srcBytes),
		DstUUID:     string(dstBytes),
		Payload:     payload,
	}, "", nil
}

// EncodeServerListFrame builds a JSON server_list control message using single-exit ojson.Doc serialization.
func EncodeServerListFrame(requestID string, servers []ServerInfo) ([]byte, error) {
	doc := ojson.NewDocString("{}")
	if errSet := doc.Set("type", ControlTypeServerList); errSet != nil {
		return nil, errSet
	}
	if requestID != "" {
		if errSet := doc.Set("request_id", requestID); errSet != nil {
			return nil, errSet
		}
	}
	if errSetRaw := doc.SetRaw("servers", "[]"); errSetRaw != nil {
		return nil, errSetRaw
	}
	for i, srv := range servers {
		idx := strconv.Itoa(i)
		if errSet := doc.Set("servers."+idx+".uuid", srv.UUID); errSet != nil {
			return nil, errSet
		}
		if errSet := doc.Set("servers."+idx+".name", srv.Name); errSet != nil {
			return nil, errSet
		}
		if errSet := doc.Set("servers."+idx+".type", srv.Type); errSet != nil {
			return nil, errSet
		}
	}
	return doc.Bytes(), nil
}

// EncodeServerEventFrame builds a server_online or server_offline JSON control message.
func EncodeServerEventFrame(eventType string, server ServerInfo) ([]byte, error) {
	if eventType != ControlTypeServerOnline && eventType != ControlTypeServerOffline {
		return nil, fmt.Errorf("unsupported server event type %q", eventType)
	}
	doc := ojson.NewDocString("{}")
	if errSet := doc.Set("type", eventType); errSet != nil {
		return nil, errSet
	}
	if errSet := doc.Set("server.uuid", server.UUID); errSet != nil {
		return nil, errSet
	}
	if errSet := doc.Set("server.name", server.Name); errSet != nil {
		return nil, errSet
	}
	if errSet := doc.Set("server.type", server.Type); errSet != nil {
		return nil, errSet
	}
	return doc.Bytes(), nil
}

// EncodeErrorFrame builds a non-fatal JSON error control message.
func EncodeErrorFrame(code, message, targetUUID, requestID string) ([]byte, error) {
	doc := ojson.NewDocString("{}")
	if errSet := doc.Set("type", ControlTypeError); errSet != nil {
		return nil, errSet
	}
	if errSet := doc.Set("code", code); errSet != nil {
		return nil, errSet
	}
	if targetUUID != "" {
		if errSet := doc.Set("target_uuid", targetUUID); errSet != nil {
			return nil, errSet
		}
	}
	if requestID != "" {
		if errSet := doc.Set("request_id", requestID); errSet != nil {
			return nil, errSet
		}
	}
	if errSet := doc.Set("message", message); errSet != nil {
		return nil, errSet
	}
	return doc.Bytes(), nil
}

// ParseControlFrame validates and parses an inbound JSON text control frame.
func ParseControlFrame(raw []byte) (ControlRequest, string, error) {
	if len(raw) > MaxControlFrameBytes {
		return ControlRequest{}, ErrCodeInvalidControlFrame, errors.New("control frame exceeds maximum allowed size")
	}
	if len(raw) == 0 || !utf8.Valid(raw) || !ojson.ValidBytes(raw) {
		return ControlRequest{}, ErrCodeInvalidControlFrame, errors.New("malformed control json")
	}
	parsed := ojson.ParseBytes(raw)
	if !parsed.IsObject() {
		return ControlRequest{}, ErrCodeInvalidControlFrame, errors.New("control frame must be a json object")
	}
	typeRes := parsed.Get("type")
	if typeRes.Type != ojson.String {
		return ControlRequest{}, ErrCodeInvalidControlFrame, errors.New("control frame missing string type")
	}
	msgType := typeRes.String()
	if len(msgType) > MaxControlTypeLen {
		return ControlRequest{}, ErrCodeInvalidControlFrame, errors.New("control type exceeds maximum allowed size")
	}
	if msgType != ControlTypeListServers {
		return ControlRequest{}, ErrCodeInvalidControlFrame, errors.New("unsupported control message type")
	}

	var reqID string
	reqIDRes := parsed.Get("request_id")
	if reqIDRes.Exists() {
		if reqIDRes.Type != ojson.String {
			return ControlRequest{}, ErrCodeInvalidControlFrame, errors.New("request_id must be a string")
		}
		reqID = reqIDRes.String()
		if len(reqID) > MaxRequestIDLen || !utf8.ValidString(reqID) || hasASCIIControlChar(reqID) {
			return ControlRequest{}, ErrCodeInvalidControlFrame, errors.New("invalid request_id")
		}
	}

	return ControlRequest{
		Type:      msgType,
		RequestID: reqID,
	}, "", nil
}
