package openai

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	requestlogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type websocketTimelineAppender interface {
	Append(eventType string, payload []byte, timestamp time.Time)
}

type responsesWebsocketPinnedAuthState struct {
	authID   string
	modelKey string
}

type websocketTimelineLog struct {
	enabled bool
	source  *requestlogging.FileBodySource
	builder *strings.Builder

	currentPart       io.WriteCloser
	currentPartHasLog bool
}

func newWebsocketTimelineLog(enabled bool, source *requestlogging.FileBodySource) *websocketTimelineLog {
	if !enabled {
		return &websocketTimelineLog{}
	}
	if source == nil {
		return newInMemoryWebsocketTimelineLog()
	}
	return &websocketTimelineLog{
		enabled: true,
		source:  source,
	}
}

func newInMemoryWebsocketTimelineLog() *websocketTimelineLog {
	return &websocketTimelineLog{
		enabled: true,
		builder: &strings.Builder{},
	}
}

func websocketTimelineSourceFromContext(c *gin.Context) *requestlogging.FileBodySource {
	if c == nil {
		return nil
	}
	value, exists := c.Get(requestlogging.WebsocketTimelineSourceContextKey)
	if !exists {
		return nil
	}
	source, ok := value.(*requestlogging.FileBodySource)
	if !ok {
		return nil
	}
	return source
}

func (l *websocketTimelineLog) BeginRequest() {
	if l == nil || !l.enabled || l.source == nil {
		return
	}
	l.closeCurrentPart()
	part, errCreate := l.source.CreatePart("request")
	if errCreate != nil {
		log.WithError(errCreate).Warn("failed to create websocket request detail log")
		return
	}
	l.currentPart = part
	l.currentPartHasLog = false
}

func (l *websocketTimelineLog) Append(eventType string, payload []byte, timestamp time.Time) {
	if l == nil || !l.enabled {
		return
	}
	data := formatWebsocketTimelineEvent(eventType, payload, timestamp)
	if len(data) == 0 {
		return
	}
	if l.source != nil {
		if l.currentPart == nil {
			l.BeginRequest()
		}
		if l.currentPart == nil {
			return
		}
		if errWrite := writeWebsocketTimelinePart(l.currentPart, data, l.currentPartHasLog); errWrite != nil {
			log.WithError(errWrite).Warn("failed to write websocket request detail log")
			return
		}
		l.currentPartHasLog = true
		return
	}
	if l.builder != nil {
		writeWebsocketTimelineBuilder(l.builder, data)
	}
}

func (l *websocketTimelineLog) SetContext(c *gin.Context) {
	if l == nil || !l.enabled {
		return
	}
	l.closeCurrentPart()
	if l.source != nil {
		if l.source.HasPayload() {
			c.Set(requestlogging.WebsocketTimelineSourceContextKey, l.source)
			return
		}
		if errCleanup := l.source.Cleanup(); errCleanup != nil {
			log.WithError(errCleanup).Warn("failed to clean up empty websocket timeline log parts")
		}
	}
	if l.builder != nil {
		setWebsocketTimelineBody(c, l.builder.String())
	}
}

func (l *websocketTimelineLog) String() string {
	if l == nil || !l.enabled {
		return ""
	}
	l.closeCurrentPart()
	if l.source != nil {
		data, errRead := l.source.Bytes()
		if errRead != nil {
			return ""
		}
		return string(data)
	}
	if l.builder == nil {
		return ""
	}
	return l.builder.String()
}

func (l *websocketTimelineLog) closeCurrentPart() {
	if l == nil || l.currentPart == nil {
		return
	}
	if errClose := l.currentPart.Close(); errClose != nil {
		log.WithError(errClose).Warn("failed to close websocket request detail log")
	}
	l.currentPart = nil
	l.currentPartHasLog = false
}

func writeWebsocketTimelinePart(w io.Writer, data []byte, prependNewline bool) error {
	if w == nil || len(data) == 0 {
		return nil
	}
	if prependNewline {
		if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
			return errWrite
		}
	}
	_, errWrite := w.Write(data)
	return errWrite
}

func writeWebsocketTimelineBuilder(builder *strings.Builder, data []byte) {
	if builder == nil || len(data) == 0 {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	builder.Write(data)
}

func appendWebsocketEvent(builder *strings.Builder, eventType string, payload []byte) {
	if builder == nil {
		return
	}
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	builder.WriteString("websocket.")
	builder.WriteString(eventType)
	builder.WriteString("\n")
	builder.Write(trimmedPayload)
	builder.WriteString("\n")
}

func websocketPayloadEventType(payload []byte) string {
	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if eventType == "" {
		return "-"
	}
	return eventType
}

func websocketPayloadPreview(payload []byte) string {
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return "<empty>"
	}
	previewText := strings.ReplaceAll(string(trimmedPayload), "\n", "\\n")
	previewText = strings.ReplaceAll(previewText, "\r", "\\r")
	return previewText
}

func isResponsesWebsocketCompletionEvent(eventType string) bool {
	return eventType == wsEventTypeCompleted || eventType == wsEventTypeDone
}

func responsesWebsocketErrorMessageFromPayload(payload []byte) *interfaces.ErrorMessage {
	status := int(gjson.GetBytes(payload, "status").Int())
	if status <= 0 {
		status = int(gjson.GetBytes(payload, "status_code").Int())
	}
	if status <= 0 {
		status = http.StatusInternalServerError
	}

	errText := strings.TrimSpace(gjson.GetBytes(payload, "error.message").String())
	if errText == "" {
		errText = strings.TrimSpace(gjson.GetBytes(payload, "message").String())
	}
	if errText == "" {
		errText = strings.TrimSpace(string(payload))
	}
	if errText == "" {
		errText = http.StatusText(status)
	}
	return &interfaces.ErrorMessage{StatusCode: status, Error: fmt.Errorf("%s", errText)}
}

func setWebsocketTimelineBody(c *gin.Context, body string) {
	setWebsocketBody(c, wsTimelineBodyKey, body)
}

func setWebsocketBody(c *gin.Context, key string, body string) {
	if c == nil {
		return
	}
	trimmedBody := strings.TrimSpace(body)
	if trimmedBody == "" {
		return
	}
	c.Set(key, []byte(trimmedBody))
}

func writeResponsesWebsocketPayload(writer *responsesWebsocketWriter, wsTimelineLog websocketTimelineAppender, payload []byte, timestamp time.Time) error {
	if wsTimelineLog != nil {
		wsTimelineLog.Append("response", payload, timestamp)
	}
	if writer == nil || writer.conn == nil {
		return fmt.Errorf("responses websocket: writer is nil")
	}
	writer.writeMu.Lock()
	defer writer.writeMu.Unlock()
	if writer.closing.Load() {
		return websocket.ErrCloseSent
	}
	return writer.conn.WriteMessage(websocket.TextMessage, payload)
}

func appendWebsocketTimelineDisconnect(timeline websocketTimelineAppender, err error, timestamp time.Time) {
	if err == nil {
		return
	}
	if timeline != nil {
		timeline.Append("disconnect", []byte(err.Error()), timestamp)
	}
}

func appendWebsocketTimelineEvent(builder *strings.Builder, eventType string, payload []byte, timestamp time.Time) {
	if builder == nil {
		return
	}
	writeWebsocketTimelineBuilder(builder, formatWebsocketTimelineEvent(eventType, payload, timestamp))
}

func formatWebsocketTimelineEvent(eventType string, payload []byte, timestamp time.Time) []byte {
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return nil
	}
	var builder strings.Builder
	builder.WriteString("Timestamp: ")
	builder.WriteString(timestamp.Format(time.RFC3339Nano))
	builder.WriteString("\n")
	builder.WriteString("Event: websocket.")
	builder.WriteString(eventType)
	builder.WriteString("\n")
	builder.Write(trimmedPayload)
	builder.WriteString("\n")
	return []byte(builder.String())
}

func markAPIResponseTimestamp(c *gin.Context) {
	if c == nil {
		return
	}
	if _, exists := c.Get("API_RESPONSE_TIMESTAMP"); exists {
		return
	}
	c.Set("API_RESPONSE_TIMESTAMP", time.Now())
}
