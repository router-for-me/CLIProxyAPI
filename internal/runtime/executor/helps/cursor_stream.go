package helps

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps/cursorproto"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	cursorConnectEndStreamFlag  = byte(0x02)
	cursorConnectCompressedFlag = byte(0x01)
	cursorMaxFrameSize          = 64 << 20
)

// CursorStatusError is an HTTP-like error returned by Cursor's Connect stream.
type CursorStatusError struct {
	Status  int
	Message string
}

func (e *CursorStatusError) Error() string {
	if e == nil {
		return "cursor upstream error"
	}
	return e.Message
}

func (e *CursorStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.Status
}

// CursorStreamEvent is one normalized event emitted by Cursor AgentService.Run.
type CursorStreamEvent struct {
	Text             string
	Reasoning        string
	ToolCallID       string
	ToolName         string
	ToolArguments    string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Usage            bool
	Done             bool
	Err              error
}

// CursorStream is a live Cursor Run response.
type CursorStream struct {
	Headers http.Header
	Events  <-chan CursorStreamEvent
}

type cursorRequestWriter struct {
	mu     sync.Mutex
	pipe   *io.PipeWriter
	closed bool
}

func (w *cursorRequestWriter) writeMessage(message []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.pipe == nil {
		return io.ErrClosedPipe
	}
	frame := make([]byte, 5+len(message))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(message)))
	copy(frame[5:], message)
	if _, errWrite := w.pipe.Write(frame); errWrite != nil {
		return fmt.Errorf("cursor stream: write request frame: %w", errWrite)
	}
	return nil
}

func (w *cursorRequestWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	if w.pipe != nil {
		if errClose := w.pipe.Close(); errClose != nil {
			log.WithError(errClose).Debug("cursor stream: close request pipe")
		}
	}
}

// OpenCursorStream starts one bidirectional HTTP/2 ConnectRPC stream.
func OpenCursorStream(ctx context.Context, client *http.Client, accessToken string, run *CursorRunPayload) (*CursorStream, error) {
	if client == nil {
		return nil, fmt.Errorf("cursor stream: HTTP client is nil")
	}
	if run == nil || len(run.Message) == 0 {
		return nil, fmt.Errorf("cursor stream: run payload is empty")
	}
	pipeReader, pipeWriter := io.Pipe()
	writer := &cursorRequestWriter{pipe: pipeWriter}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, cursorauth.APIBaseURL+cursorauth.RunPath, pipeReader)
	if errRequest != nil {
		writer.close()
		return nil, fmt.Errorf("cursor stream: create request: %w", errRequest)
	}
	applyCursorRunHeaders(request, accessToken)

	initialResult := make(chan error, 1)
	go func() {
		initialResult <- writer.writeMessage(run.Message)
	}()
	response, errDo := client.Do(request)
	if errDo != nil {
		writer.close()
		return nil, fmt.Errorf("cursor stream: connect request failed: %w", errDo)
	}
	if errInitial := <-initialResult; errInitial != nil {
		writer.close()
		_ = response.Body.Close()
		return nil, errInitial
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writer.close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if errClose := response.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("cursor stream: close error response")
		}
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return nil, &CursorStatusError{Status: response.StatusCode, Message: "cursor stream: " + message}
	}

	events := make(chan CursorStreamEvent)
	go runCursorResponseLoop(ctx, response.Body, writer, run, events)
	return &CursorStream{Headers: response.Header.Clone(), Events: events}, nil
}

func applyCursorRunHeaders(request *http.Request, accessToken string) {
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	request.Header.Set("Content-Type", "application/connect+proto")
	request.Header.Set("Connect-Protocol-Version", "1")
	request.Header.Set("TE", "trailers")
	request.Header.Set("X-Ghost-Mode", "true")
	request.Header.Set("X-Cursor-Client-Version", cursorauth.ClientVersion)
	request.Header.Set("X-Cursor-Client-Type", cursorauth.ClientType)
	// Without this opt-in Cursor may coalesce interaction deltas before returning them.
	request.Header.Set("X-Cursor-Streaming", "true")
	request.Header.Set("X-Request-ID", uuid.NewString())
}

func runCursorResponseLoop(ctx context.Context, body io.ReadCloser, writer *cursorRequestWriter, run *CursorRunPayload, events chan<- CursorStreamEvent) {
	defer close(events)
	defer writer.close()
	defer func() {
		if errClose := body.Close(); errClose != nil {
			log.WithError(errClose).Debug("cursor stream: close response body")
		}
	}()

	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go sendCursorHeartbeats(ctx, writer, heartbeatDone)

	completionTokens := 0
	totalTokens := 0
	for {
		flags, message, errFrame := readCursorConnectFrame(body)
		if errFrame != nil {
			if errFrame == io.EOF || ctx.Err() != nil {
				emitCursorUsage(ctx, events, totalTokens, completionTokens)
				if ctx.Err() == nil {
					emitCursorEvent(ctx, events, CursorStreamEvent{Done: true})
				}
				return
			}
			emitCursorEvent(ctx, events, CursorStreamEvent{Err: errFrame})
			return
		}
		if flags&cursorConnectCompressedFlag != 0 {
			emitCursorEvent(ctx, events, CursorStreamEvent{Err: &CursorStatusError{Status: http.StatusBadGateway, Message: "cursor stream: compressed Connect frames are unsupported"}})
			return
		}
		if flags&cursorConnectEndStreamFlag != 0 {
			if errEnd := parseCursorConnectEnd(message); errEnd != nil {
				emitCursorEvent(ctx, events, CursorStreamEvent{Err: errEnd})
				return
			}
			emitCursorUsage(ctx, events, totalTokens, completionTokens)
			emitCursorEvent(ctx, events, CursorStreamEvent{Done: true})
			return
		}
		var serverMessage cursorproto.AgentServerMessage
		if errDecode := proto.Unmarshal(message, &serverMessage); errDecode != nil {
			emitCursorEvent(ctx, events, CursorStreamEvent{Err: &CursorStatusError{Status: http.StatusBadGateway, Message: "cursor stream: decode server message: " + errDecode.Error()}})
			return
		}
		switch item := serverMessage.Message.(type) {
		case *cursorproto.AgentServerMessage_InteractionUpdate:
			if item.InteractionUpdate == nil {
				continue
			}
			switch update := item.InteractionUpdate.Message.(type) {
			case *cursorproto.InteractionUpdate_TextDelta:
				if update.TextDelta != nil && update.TextDelta.Text != "" {
					emitCursorEvent(ctx, events, CursorStreamEvent{Text: update.TextDelta.Text})
				}
			case *cursorproto.InteractionUpdate_ThinkingDelta:
				if update.ThinkingDelta != nil && update.ThinkingDelta.Text != "" {
					emitCursorEvent(ctx, events, CursorStreamEvent{Reasoning: update.ThinkingDelta.Text})
				}
			case *cursorproto.InteractionUpdate_TokenDelta:
				if update.TokenDelta != nil && update.TokenDelta.Tokens > 0 {
					completionTokens += int(update.TokenDelta.Tokens)
				}
			}
		case *cursorproto.AgentServerMessage_ConversationCheckpointUpdate:
			if item.ConversationCheckpointUpdate != nil && item.ConversationCheckpointUpdate.TokenDetails != nil {
				totalTokens = int(item.ConversationCheckpointUpdate.TokenDetails.UsedTokens)
			}
		case *cursorproto.AgentServerMessage_KvServerMessage:
			if errKV := respondCursorKV(writer, run.Blobs, item.KvServerMessage); errKV != nil {
				emitCursorEvent(ctx, events, CursorStreamEvent{Err: errKV})
				return
			}
		case *cursorproto.AgentServerMessage_ExecServerMessage:
			toolCall, errExec := respondCursorExec(writer, run.Tools, run.SystemPrompt, item.ExecServerMessage)
			if errExec != nil {
				emitCursorEvent(ctx, events, CursorStreamEvent{Err: errExec})
				return
			}
			if toolCall != nil {
				emitCursorEvent(ctx, events, *toolCall)
				emitCursorUsage(ctx, events, totalTokens, completionTokens)
				emitCursorEvent(ctx, events, CursorStreamEvent{Done: true})
				return
			}
		}
	}
}

func sendCursorHeartbeats(ctx context.Context, writer *cursorRequestWriter, done <-chan struct{}) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			message, errMarshal := proto.Marshal(&cursorproto.AgentClientMessage{Message: &cursorproto.AgentClientMessage_ClientHeartbeat{ClientHeartbeat: &cursorproto.ClientHeartbeat{}}})
			if errMarshal == nil {
				_ = writer.writeMessage(message)
			}
		}
	}
}

func readCursorConnectFrame(reader io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, errRead := io.ReadFull(reader, header); errRead != nil {
		return 0, nil, errRead
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > cursorMaxFrameSize {
		return 0, nil, &CursorStatusError{Status: http.StatusBadGateway, Message: fmt.Sprintf("cursor stream: frame too large: %d", length)}
	}
	payload := make([]byte, int(length))
	if _, errRead := io.ReadFull(reader, payload); errRead != nil {
		return 0, nil, errRead
	}
	return header[0], payload, nil
}

func parseCursorConnectEnd(payload []byte) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	var envelope struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if errJSON := json.Unmarshal(payload, &envelope); errJSON != nil || envelope.Error == nil {
		return nil
	}
	message := strings.TrimSpace(envelope.Error.Message)
	if message == "" {
		message = "Cursor upstream error"
	}
	status := http.StatusBadGateway
	switch strings.ToLower(strings.TrimSpace(envelope.Error.Code)) {
	case "unauthenticated":
		status = http.StatusUnauthorized
	case "resource_exhausted":
		if cursorContextError(message) {
			status = http.StatusBadRequest
		} else {
			status = http.StatusTooManyRequests
		}
	case "invalid_argument":
		status = http.StatusBadRequest
	case "unavailable":
		status = http.StatusServiceUnavailable
	case "deadline_exceeded":
		status = http.StatusGatewayTimeout
	case "internal":
		status = http.StatusBadGateway
	}
	return &CursorStatusError{Status: status, Message: "cursor stream: " + message}
}

func cursorContextError(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{"context", "token", "length", "overflow", "too long", "too large"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func respondCursorKV(writer *cursorRequestWriter, blobs map[string][]byte, message *cursorproto.KvServerMessage) error {
	if message == nil {
		return nil
	}
	response := &cursorproto.KvClientMessage{Id: message.Id}
	switch item := message.Message.(type) {
	case *cursorproto.KvServerMessage_GetBlobArgs:
		data := []byte(nil)
		if item.GetBlobArgs != nil {
			data = blobs[hex.EncodeToString(item.GetBlobArgs.BlobId)]
		}
		response.Message = &cursorproto.KvClientMessage_GetBlobResult{GetBlobResult: &cursorproto.GetBlobResult{BlobData: data}}
	case *cursorproto.KvServerMessage_SetBlobArgs:
		if item.SetBlobArgs != nil {
			blobs[hex.EncodeToString(item.SetBlobArgs.BlobId)] = append([]byte(nil), item.SetBlobArgs.BlobData...)
		}
		response.Message = &cursorproto.KvClientMessage_SetBlobResult{SetBlobResult: &cursorproto.SetBlobResult{}}
	default:
		return nil
	}
	return writeCursorClientMessage(writer, &cursorproto.AgentClientMessage{Message: &cursorproto.AgentClientMessage_KvClientMessage{KvClientMessage: response}})
}

func respondCursorExec(writer *cursorRequestWriter, tools []*cursorproto.McpToolDefinition, systemPrompt string, message *cursorproto.ExecServerMessage) (*CursorStreamEvent, error) {
	if message == nil {
		return nil, nil
	}
	const reason = "Tool not available in this environment. Use the supplied MCP tools instead."
	response := &cursorproto.ExecClientMessage{Id: message.Id, ExecId: message.ExecId}
	switch item := message.Message.(type) {
	case *cursorproto.ExecServerMessage_RequestContextArgs:
		response.Message = &cursorproto.ExecClientMessage_RequestContextResult{RequestContextResult: &cursorproto.RequestContextResult{Result: &cursorproto.RequestContextResult_Success{Success: &cursorproto.RequestContextSuccess{RequestContext: &cursorproto.RequestContext{
			Tools:     tools,
			CloudRule: proto.String(systemPrompt),
		}}}}}
	case *cursorproto.ExecServerMessage_McpArgs:
		if item.McpArgs == nil {
			return nil, &CursorStatusError{Status: http.StatusBadGateway, Message: "cursor stream: empty MCP arguments"}
		}
		arguments := make(map[string]any, len(item.McpArgs.Args))
		for key, raw := range item.McpArgs.Args {
			var value structpb.Value
			if errDecode := proto.Unmarshal(raw, &value); errDecode == nil {
				arguments[key] = value.AsInterface()
			} else {
				arguments[key] = string(raw)
			}
		}
		encoded, errJSON := json.Marshal(arguments)
		if errJSON != nil {
			return nil, fmt.Errorf("cursor stream: encode MCP arguments: %w", errJSON)
		}
		callID := strings.TrimSpace(item.McpArgs.ToolCallId)
		if callID == "" {
			callID = fmt.Sprintf("call_%d", message.Id)
		}
		name := strings.TrimSpace(item.McpArgs.ToolName)
		if name == "" {
			name = strings.TrimSpace(item.McpArgs.Name)
		}
		return &CursorStreamEvent{ToolCallID: callID, ToolName: name, ToolArguments: string(encoded)}, nil
	case *cursorproto.ExecServerMessage_ReadArgs:
		path := ""
		if item.ReadArgs != nil {
			path = item.ReadArgs.Path
		}
		response.Message = &cursorproto.ExecClientMessage_ReadResult{ReadResult: &cursorproto.ReadResult{Result: &cursorproto.ReadResult_Rejected{Rejected: &cursorproto.ReadRejected{Path: path, Reason: reason}}}}
	case *cursorproto.ExecServerMessage_LsArgs:
		path := ""
		if item.LsArgs != nil {
			path = item.LsArgs.Path
		}
		response.Message = &cursorproto.ExecClientMessage_LsResult{LsResult: &cursorproto.LsResult{Result: &cursorproto.LsResult_Rejected{Rejected: &cursorproto.LsRejected{Path: path, Reason: reason}}}}
	case *cursorproto.ExecServerMessage_GrepArgs:
		response.Message = &cursorproto.ExecClientMessage_GrepResult{GrepResult: &cursorproto.GrepResult{Result: &cursorproto.GrepResult_Error{Error: &cursorproto.GrepError{Error: reason}}}}
	case *cursorproto.ExecServerMessage_WriteArgs:
		path := ""
		if item.WriteArgs != nil {
			path = item.WriteArgs.Path
		}
		response.Message = &cursorproto.ExecClientMessage_WriteResult{WriteResult: &cursorproto.WriteResult{Result: &cursorproto.WriteResult_Rejected{Rejected: &cursorproto.WriteRejected{Path: path, Reason: reason}}}}
	case *cursorproto.ExecServerMessage_DeleteArgs:
		path := ""
		if item.DeleteArgs != nil {
			path = item.DeleteArgs.Path
		}
		response.Message = &cursorproto.ExecClientMessage_DeleteResult{DeleteResult: &cursorproto.DeleteResult{Result: &cursorproto.DeleteResult_Rejected{Rejected: &cursorproto.DeleteRejected{Path: path, Reason: reason}}}}
	case *cursorproto.ExecServerMessage_ShellArgs:
		command, dir := "", ""
		if item.ShellArgs != nil {
			command, dir = item.ShellArgs.Command, item.ShellArgs.WorkingDirectory
		}
		response.Message = &cursorproto.ExecClientMessage_ShellResult{ShellResult: &cursorproto.ShellResult{Result: &cursorproto.ShellResult_Rejected{Rejected: &cursorproto.ShellRejected{Command: command, WorkingDirectory: dir, Reason: reason}}}}
	case *cursorproto.ExecServerMessage_ShellStreamArgs:
		command, dir := "", ""
		if item.ShellStreamArgs != nil {
			command, dir = item.ShellStreamArgs.Command, item.ShellStreamArgs.WorkingDirectory
		}
		response.Message = &cursorproto.ExecClientMessage_ShellStream{ShellStream: &cursorproto.ShellStream{Event: &cursorproto.ShellStream_Rejected{Rejected: &cursorproto.ShellRejected{Command: command, WorkingDirectory: dir, Reason: reason}}}}
	case *cursorproto.ExecServerMessage_BackgroundShellSpawnArgs:
		command, dir := "", ""
		if item.BackgroundShellSpawnArgs != nil {
			command, dir = item.BackgroundShellSpawnArgs.Command, item.BackgroundShellSpawnArgs.WorkingDirectory
		}
		response.Message = &cursorproto.ExecClientMessage_BackgroundShellSpawnResult{BackgroundShellSpawnResult: &cursorproto.BackgroundShellSpawnResult{Result: &cursorproto.BackgroundShellSpawnResult_Rejected{Rejected: &cursorproto.ShellRejected{Command: command, WorkingDirectory: dir, Reason: reason}}}}
	case *cursorproto.ExecServerMessage_WriteShellStdinArgs:
		response.Message = &cursorproto.ExecClientMessage_WriteShellStdinResult{WriteShellStdinResult: &cursorproto.WriteShellStdinResult{Result: &cursorproto.WriteShellStdinResult_Error{Error: &cursorproto.WriteShellStdinError{Error: reason}}}}
	case *cursorproto.ExecServerMessage_FetchArgs:
		url := ""
		if item.FetchArgs != nil {
			url = item.FetchArgs.Url
		}
		response.Message = &cursorproto.ExecClientMessage_FetchResult{FetchResult: &cursorproto.FetchResult{Result: &cursorproto.FetchResult_Error{Error: &cursorproto.FetchError{Url: url, Error: reason}}}}
	case *cursorproto.ExecServerMessage_DiagnosticsArgs:
		path := ""
		if item.DiagnosticsArgs != nil {
			path = item.DiagnosticsArgs.Path
		}
		response.Message = &cursorproto.ExecClientMessage_DiagnosticsResult{DiagnosticsResult: &cursorproto.DiagnosticsResult{Result: &cursorproto.DiagnosticsResult_Rejected{Rejected: &cursorproto.DiagnosticsRejected{Path: path, Reason: reason}}}}
	case *cursorproto.ExecServerMessage_ListMcpResourcesExecArgs:
		response.Message = &cursorproto.ExecClientMessage_ListMcpResourcesExecResult{ListMcpResourcesExecResult: &cursorproto.EmptyExec{}}
	case *cursorproto.ExecServerMessage_ReadMcpResourceExecArgs:
		response.Message = &cursorproto.ExecClientMessage_ReadMcpResourceExecResult{ReadMcpResourceExecResult: &cursorproto.EmptyExec{}}
	case *cursorproto.ExecServerMessage_RecordScreenArgs:
		response.Message = &cursorproto.ExecClientMessage_RecordScreenResult{RecordScreenResult: &cursorproto.EmptyExec{}}
	case *cursorproto.ExecServerMessage_ComputerUseArgs:
		response.Message = &cursorproto.ExecClientMessage_ComputerUseResult{ComputerUseResult: &cursorproto.EmptyExec{}}
	default:
		return nil, &CursorStatusError{Status: http.StatusBadGateway, Message: "cursor stream: unhandled native tool request"}
	}
	return nil, writeCursorClientMessage(writer, &cursorproto.AgentClientMessage{Message: &cursorproto.AgentClientMessage_ExecClientMessage{ExecClientMessage: response}})
}

func writeCursorClientMessage(writer *cursorRequestWriter, message *cursorproto.AgentClientMessage) error {
	encoded, errMarshal := proto.Marshal(message)
	if errMarshal != nil {
		return fmt.Errorf("cursor stream: encode client message: %w", errMarshal)
	}
	return writer.writeMessage(encoded)
}

func emitCursorUsage(ctx context.Context, events chan<- CursorStreamEvent, totalTokens, completionTokens int) {
	if totalTokens < completionTokens {
		totalTokens = completionTokens
	}
	emitCursorEvent(ctx, events, CursorStreamEvent{Usage: true, PromptTokens: totalTokens - completionTokens, CompletionTokens: completionTokens, TotalTokens: totalTokens})
}

func emitCursorEvent(ctx context.Context, events chan<- CursorStreamEvent, event CursorStreamEvent) {
	select {
	case events <- event:
	case <-ctx.Done():
	}
}
