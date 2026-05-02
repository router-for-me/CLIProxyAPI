package helps

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func newLoggingHelperTestContext(t *testing.T) (context.Context, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	return context.WithValue(context.Background(), "gin", ginCtx), ginCtx
}

func assertAPIResponseTimestampSet(t *testing.T, ginCtx *gin.Context) time.Time {
	t.Helper()
	value, exists := ginCtx.Get("API_RESPONSE_TIMESTAMP")
	if !exists {
		t.Fatal("API_RESPONSE_TIMESTAMP was not set")
	}
	timestamp, ok := value.(time.Time)
	if !ok {
		t.Fatalf("API_RESPONSE_TIMESTAMP type = %T, want time.Time", value)
	}
	if timestamp.IsZero() {
		t.Fatal("API_RESPONSE_TIMESTAMP is zero")
	}
	return timestamp
}

func TestMarkAPIResponseTimestampDoesNotOverwriteExisting(t *testing.T) {
	ctx, ginCtx := newLoggingHelperTestContext(t)
	existing := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	ginCtx.Set("API_RESPONSE_TIMESTAMP", existing)

	MarkAPIResponseTimestamp(ctx)

	value, _ := ginCtx.Get("API_RESPONSE_TIMESTAMP")
	timestamp, ok := value.(time.Time)
	if !ok {
		t.Fatalf("API_RESPONSE_TIMESTAMP type = %T, want time.Time", value)
	}
	if !timestamp.Equal(existing) {
		t.Fatalf("API_RESPONSE_TIMESTAMP = %v, want existing %v", timestamp, existing)
	}
}

func TestRecordAPIResponseMetadataMarksTimestampWithoutRequestLogging(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{name: "nil config", cfg: nil},
		{name: "request logging disabled", cfg: &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ginCtx := newLoggingHelperTestContext(t)

			RecordAPIResponseMetadata(ctx, tt.cfg, 200, nil)

			assertAPIResponseTimestampSet(t, ginCtx)
			if _, exists := ginCtx.Get(apiResponseKey); exists {
				t.Fatal("API response log was written while request logging was disabled")
			}
		})
	}
}

func TestAppendAPIResponseChunkMarksTimestampWhenRequestLoggingDisabled(t *testing.T) {
	ctx, ginCtx := newLoggingHelperTestContext(t)

	AppendAPIResponseChunk(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, []byte(" data: hello\n"))

	assertAPIResponseTimestampSet(t, ginCtx)
	if _, exists := ginCtx.Get(apiResponseKey); exists {
		t.Fatal("API response log was written while request logging was disabled")
	}
}

func TestAppendAPIResponseChunkDoesNotMarkTimestampForWhitespaceChunk(t *testing.T) {
	ctx, ginCtx := newLoggingHelperTestContext(t)

	AppendAPIResponseChunk(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, []byte(" \n\t "))

	if _, exists := ginCtx.Get("API_RESPONSE_TIMESTAMP"); exists {
		t.Fatal("API_RESPONSE_TIMESTAMP was set for an all-whitespace chunk")
	}
}

func TestRecordAPIWebsocketHandshakeMarksTimestampWithoutRequestLogging(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{name: "nil config", cfg: nil},
		{name: "request logging disabled", cfg: &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ginCtx := newLoggingHelperTestContext(t)

			RecordAPIWebsocketHandshake(ctx, tt.cfg, 101, nil)

			assertAPIResponseTimestampSet(t, ginCtx)
			if _, exists := ginCtx.Get(apiWebsocketTimelineKey); exists {
				t.Fatal("API websocket timeline was written while request logging was disabled")
			}
		})
	}
}

func TestAppendAPIWebsocketResponseMarksTimestampWithoutRequestLogging(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{name: "nil config", cfg: nil},
		{name: "request logging disabled", cfg: &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ginCtx := newLoggingHelperTestContext(t)

			AppendAPIWebsocketResponse(ctx, tt.cfg, []byte(" response payload "))

			assertAPIResponseTimestampSet(t, ginCtx)
			if _, exists := ginCtx.Get(apiWebsocketTimelineKey); exists {
				t.Fatal("API websocket timeline was written while request logging was disabled")
			}
		})
	}
}

func TestAppendAPIWebsocketResponseDoesNotMarkTimestampForEmptyOrWhitespacePayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "empty", payload: nil},
		{name: "whitespace", payload: []byte(" \n\t ")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ginCtx := newLoggingHelperTestContext(t)

			AppendAPIWebsocketResponse(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, tt.payload)

			if _, exists := ginCtx.Get("API_RESPONSE_TIMESTAMP"); exists {
				t.Fatal("API_RESPONSE_TIMESTAMP was set for an empty or all-whitespace websocket response")
			}
		})
	}
}

func TestAPIWebsocketResponseTimestampDoesNotOverwriteExisting(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context)
	}{
		{
			name: "handshake",
			call: func(ctx context.Context) {
				RecordAPIWebsocketHandshake(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, 101, nil)
			},
		},
		{
			name: "response append",
			call: func(ctx context.Context) {
				AppendAPIWebsocketResponse(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, []byte(" response payload "))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ginCtx := newLoggingHelperTestContext(t)
			existing := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
			ginCtx.Set("API_RESPONSE_TIMESTAMP", existing)

			tt.call(ctx)

			value, _ := ginCtx.Get("API_RESPONSE_TIMESTAMP")
			timestamp, ok := value.(time.Time)
			if !ok {
				t.Fatalf("API_RESPONSE_TIMESTAMP type = %T, want time.Time", value)
			}
			if !timestamp.Equal(existing) {
				t.Fatalf("API_RESPONSE_TIMESTAMP = %v, want existing %v", timestamp, existing)
			}
		})
	}
}
