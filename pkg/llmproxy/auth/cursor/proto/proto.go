// Package proto provides minimal stubs for Cursor protocol support.
// TODO(cursor-proto): These are placeholder stubs pending full protobuf codegen.
// protoc is unavailable in this build environment; runtime errors expected if actually invoked.
package proto

import (
	"errors"
	"io"
)

// H2Stream represents an HTTP/2 stream connection to Cursor.
type H2Stream struct {
	id     string
	dataCh chan []byte
	doneCh chan struct{}
	err    error
	// TODO(cursor-proto): implement real H2 framing
}

// McpToolDef defines an MCP tool for Cursor.
type McpToolDef struct {
	Name        string
	Description string
	InputSchema any
}

// ImageData represents an image in Cursor protocol.
type ImageData struct {
	URL      string
	MimeType string
	Data     []byte
	// TODO(cursor-proto): add other fields
}

// TurnData represents a conversation turn.
type TurnData struct {
	UserText      string
	AssistantText string
	// TODO(cursor-proto): add other fields
}

// RunRequestParams parameters for a run request.
type RunRequestParams struct {
	ModelId        string
	SystemPrompt   string
	UserText       string
	MessageId      string
	ConversationId string
	Images         []ImageData
	Turns          []TurnData
	McpTools       []McpToolDef
	BlobStore      map[string][]byte
	RawCheckpoint  []byte
	// TODO(cursor-proto): add other fields
}

// ConnectError represents a connection error.
type ConnectError struct {
	Code    string
	Message string
}

// Server message types
const (
	ServerMsgTextDelta           = iota
	ServerMsgThinkingDelta       = iota
	ServerMsgThinkingCompleted   = iota
	ServerMsgTurnEnded           = iota
	ServerMsgHeartbeat           = iota
	ServerMsgCheckpoint          = iota
	ServerMsgTokenDelta          = iota
	ServerMsgKvGetBlob           = iota
	ServerMsgKvSetBlob           = iota
	ServerMsgExecRequestCtx      = iota
	ServerMsgExecMcpArgs         = iota
	ServerMsgExecReadArgs        = iota
	ServerMsgExecWriteArgs       = iota
	ServerMsgExecDeleteArgs      = iota
	ServerMsgExecLsArgs          = iota
	ServerMsgExecGrepArgs        = iota
	ServerMsgExecShellArgs       = iota
	ServerMsgExecShellStream     = iota
	ServerMsgExecBgShellSpawn    = iota
	ServerMsgExecFetchArgs       = iota
	ServerMsgExecDiagnostics     = iota
	ServerMsgExecWriteShellStdin = iota
)

// Flags for Connect messages
const (
	ConnectEndStreamFlag   = 0x01
	ConnectFrameHeaderSize = 9 // TODO(cursor-proto): confirm
)

// AgentServerMessage represents a message from the server.
type AgentServerMessage struct {
	Type             int
	Text             string
	CheckpointData   []byte
	TokenDelta       int64
	BlobData         []byte
	McpArgs          map[string][]byte
	McpToolCallId    string
	McpToolName      string
	MsgType          int
	ExecMsgId        string
	ExecId           string
	Content          string
	BlobId           string
	KvId             string
	Path             string
	URL              string
	Url              string
	Command          string
	WorkingDirectory string
}

// DialH2Stream connects to a Cursor H2 stream.
func DialH2Stream(host string, headers map[string]string) (*H2Stream, error) {
	return nil, errors.New("cursor-proto: DialH2Stream not implemented (protoc stub)")
}

// EncodeRunRequest encodes a run request.
func EncodeRunRequest(params *RunRequestParams) []byte {
	// TODO(cursor-proto): implement actual encoding
	return []byte{}
}

// FrameConnectMessage frames a Connect message.
func FrameConnectMessage(data []byte, flags int) []byte {
	// TODO(cursor-proto): implement actual framing
	return append([]byte{byte(flags)}, data...)
}

// EncodeHeartbeat encodes a heartbeat message.
func EncodeHeartbeat() []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// ParseConnectFrame parses a Connect frame.
func ParseConnectFrame(data []byte) (flags int, payload []byte, consumed int, ok bool) {
	if len(data) < ConnectFrameHeaderSize {
		return 0, nil, 0, false
	}
	// TODO(cursor-proto): implement actual parsing
	return int(data[0]), data[ConnectFrameHeaderSize:], len(data), true
}

// ParseConnectEndStream parses an end-stream frame.
func ParseConnectEndStream(payload []byte) error {
	// TODO(cursor-proto): implement
	return nil
}

// DecodeAgentServerMessage decodes a server message.
func DecodeAgentServerMessage(payload []byte) (*AgentServerMessage, error) {
	// TODO(cursor-proto): implement actual decoding
	return &AgentServerMessage{}, nil
}

// BlobIdHex returns hex representation of a blob ID.
func BlobIdHex(blobId string) string {
	return "blob_" + blobId
}

// EncodeKvGetBlobResult encodes KV get blob result.
func EncodeKvGetBlobResult(kvId string, data []byte) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeKvSetBlobResult encodes KV set blob result.
func EncodeKvSetBlobResult(kvId string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecRequestContextResult encodes exec request context result.
func EncodeExecRequestContextResult(execMsgId, execId string, tools []McpToolDef) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecMcpResult encodes exec MCP result.
func EncodeExecMcpResult(execMsgId, execId, content string, isError bool) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecReadRejected encodes exec read rejected.
func EncodeExecReadRejected(execMsgId, execId, path, reason string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecWriteRejected encodes exec write rejected.
func EncodeExecWriteRejected(execMsgId, execId, path, reason string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecDeleteRejected encodes exec delete rejected.
func EncodeExecDeleteRejected(execMsgId, execId, path, reason string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecLsRejected encodes exec ls rejected.
func EncodeExecLsRejected(execMsgId, execId, path, reason string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecGrepError encodes exec grep error.
func EncodeExecGrepError(execMsgId, execId, reason string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecShellRejected encodes exec shell rejected.
func EncodeExecShellRejected(execMsgId, execId, command, wd, reason string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecBackgroundShellSpawnRejected encodes background shell spawn rejected.
func EncodeExecBackgroundShellSpawnRejected(execMsgId, execId, command, wd, reason string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecFetchError encodes exec fetch error.
func EncodeExecFetchError(execMsgId, execId, url, reason string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecDiagnosticsResult encodes exec diagnostics result.
func EncodeExecDiagnosticsResult(execMsgId, execId string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// EncodeExecWriteShellStdinError encodes exec write shell stdin error.
func EncodeExecWriteShellStdinError(execMsgId, execId, reason string) []byte {
	// TODO(cursor-proto): implement
	return []byte{}
}

// ProtobufValueBytesToJSON converts protobuf value bytes to JSON.
func ProtobufValueBytesToJSON(data []byte) (interface{}, error) {
	// TODO(cursor-proto): implement
	return nil, errors.New("cursor-proto: ProtobufValueBytesToJSON not implemented")
}

// Write writes data to the stream.
func (s *H2Stream) Write(data []byte) error {
	// TODO(cursor-proto): implement
	return errors.New("cursor-proto: Write not implemented (protoc stub)")
}

func (s *H2Stream) ID() string {
	if s == nil || s.id == "" {
		return "cursor-stub"
	}
	return s.id
}

func (s *H2Stream) Data() <-chan []byte {
	if s == nil {
		ch := make(chan []byte)
		close(ch)
		return ch
	}
	if s.dataCh == nil {
		s.dataCh = make(chan []byte)
		close(s.dataCh)
	}
	return s.dataCh
}

func (s *H2Stream) Done() <-chan struct{} {
	if s == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	if s.doneCh == nil {
		s.doneCh = make(chan struct{})
		close(s.doneCh)
	}
	return s.doneCh
}

func (s *H2Stream) Err() error {
	if s == nil {
		return nil
	}
	return s.err
}

// Read reads data from the stream.
func (s *H2Stream) Read(p []byte) (n int, err error) {
	// TODO(cursor-proto): implement
	return 0, io.EOF
}

// Close closes the stream.
func (s *H2Stream) Close() error {
	// TODO(cursor-proto): implement
	return nil
}
