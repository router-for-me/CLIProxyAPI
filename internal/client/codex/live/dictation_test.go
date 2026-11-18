package live

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDictationStreamOAuthFramesAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "finalize", true: "cancel"}[cancel], func(t *testing.T) {
			upstreamDone := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upstreamDone)
				if r.URL.Path != "/dictation/stream" || r.URL.Query().Get("dictation_surface") != "composer" {
					t.Error("wrong dictation URL")
					return
				}
				protocols := websocket.Subprotocols(r)
				if strings.Join(protocols, ",") != "chatgpt-dictation,openai-bearer.synthetic-oauth,codex-desktop" {
					t.Error("runtime OAuth protocols missing")
					return
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Chatgpt-Account-Id") != "synthetic-account" {
					t.Error("incorrect credential boundary")
					return
				}
				upgrader := websocket.Upgrader{Subprotocols: []string{"chatgpt-dictation"}, CheckOrigin: func(*http.Request) bool { return true }}
				ws, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer ws.Close()
				var frame map[string]any
				if ws.ReadJSON(&frame) != nil || frame["type"] != "session.start" {
					t.Error("missing start")
					return
				}
				config := frame["config"].(map[string]any)
				if config["provider_mode"] != "streaming_sse" || config["transcript_delivery_mode"] != "segment" || config["language"] != "en" || config["sample_rate_hz"] != float64(16000) {
					t.Error("incorrect start config")
					return
				}
				if _, ok := frame["model"]; ok {
					t.Error("model forwarded")
				}
				if _, ok := config["prompt"]; ok {
					t.Error("dictionary forwarded")
				}
				_ = ws.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"status": "active"}, "private": "not-forwarded"})
				if ws.ReadJSON(&frame) != nil || frame["type"] != "audio.append" || frame["audio"] != "AQACAA==" {
					t.Error("PCM changed")
					return
				}
				_ = ws.WriteJSON(map[string]any{"type": "transcript.segment", "utterance_id": "u1", "revision": 1, "text": "hello", "sequence_no": 2})
				if cancel {
					if ws.ReadJSON(&frame) == nil {
						t.Error("cancel sent a commit")
					}
					return
				}
				if ws.ReadJSON(&frame) != nil || frame["type"] != "session.close" {
					t.Error("missing close")
					return
				}
				_ = ws.WriteJSON(map[string]any{"type": "transcript.final", "utterance_id": "u1", "revision": 2, "text": "hello world", "sequence_no": 3})
				_ = ws.WriteJSON(map[string]any{"type": "session.updated", "session": map[string]any{"status": "closed"}, "sequence_no": 4})
				_, _, _ = ws.ReadMessage()
			}))
			defer upstream.Close()
			manager := auth.NewManager(nil, &apiKeyFirstSelector{}, nil)
			manager.RegisterExecutor(&captureExecutor{})
			registerCredential(t, manager, &auth.Auth{ID: "api-key", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "synthetic-unused-key"}})
			registerCredential(t, manager, &auth.Auth{ID: "oauth", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL + "/codex"}, Metadata: map[string]any{"access_token": "synthetic-oauth", "account_id": "synthetic-account"}})
			handler := NewHandler(manager, nil)
			router := gin.New()
			router.GET("/v1/audio/transcriptions/stream", handler.HandleDictationStream)
			server := httptest.NewServer(router)
			defer server.Close()
			ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/audio/transcriptions/stream", http.Header{"Authorization": []string{"Bearer synthetic-downstream"}, "Cookie": []string{"private-cookie"}})
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			_ = ws.WriteJSON(map[string]any{"type": "session.start", "model": "codex-transcribe", "config": map[string]any{"language": "en", "input_audio_format": "pcm16", "sample_rate_hz": 16000, "num_channels": 1, "prompt": "private-dictionary"}})
			var event map[string]any
			if ws.ReadJSON(&event) != nil || event["type"] != "session.started" || event["private"] != nil {
				t.Fatal("invalid started event")
			}
			// Changing the scheduler's candidates never changes this open session's auth.
			registerCredential(t, manager, &auth.Auth{ID: "another-oauth", Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "synthetic-other"}})
			_ = ws.WriteJSON(map[string]any{"type": "audio.append", "audio": "AQACAA=="})
			if ws.ReadJSON(&event) != nil || event["type"] != "transcript.segment" || event["text"] != "hello" {
				t.Fatal("partial unavailable before stop")
			}
			if cancel {
				_ = ws.Close()
			} else {
				_ = ws.WriteJSON(map[string]any{"type": "session.close"})
				if ws.ReadJSON(&event) != nil || event["type"] != "transcript.final" || event["text"] != "hello world" {
					t.Fatal("final unavailable")
				}
				if ws.ReadJSON(&event) != nil || event["session"].(map[string]any)["status"] != "closed" {
					t.Fatal("missing final acknowledgment")
				}
				_ = ws.Close()
			}
			select {
			case <-upstreamDone:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream leaked after downstream closed")
			}
		})
	}
}

func TestDictationFramingAndRedaction(t *testing.T) {
	started, closed := false, false
	if _, err := dictationInput([]byte(`{"type":"audio.append","audio":"AA=="}`), &started, &closed); err == nil {
		t.Fatal("audio before start accepted")
	}
	start := []byte(`{"type":"session.start","model":"codex-transcribe","prompt":"private","config":{"input_audio_format":"pcm16","sample_rate_hz":16000,"num_channels":1,"language":"en","prompt":"private"}}`)
	frame, err := dictationInput(start, &started, &closed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(frame), "private") || strings.Contains(string(frame), "\"model\"") {
		t.Fatal("unsupported fields leaked")
	}
	if _, err = dictationInput(start, &started, &closed); err == nil {
		t.Fatal("duplicate start accepted")
	}
	if _, err = dictationInput([]byte(`{"type":"audio.append","audio":"AA=="}`), &started, &closed); err == nil {
		t.Fatal("odd PCM accepted")
	}
	event, err := sanitizedDictationEvent([]byte(`{"type":"session.error","error":{"message":"synthetic-private-credential"},"secret":"private"}`))
	if err != nil || strings.Contains(string(event), "private") {
		t.Fatal("upstream diagnostic leaked")
	}
	var decoded map[string]any
	if json.Unmarshal(event, &decoded) != nil || decoded["fatal"] != true {
		t.Fatal("invalid sanitized error")
	}
}

// Pins the audio.append validation contract. Payloads are long enough to
// exercise the SIMD decoder path, not only the short-input fallback.
func TestDictationAudioValidation(t *testing.T) {
	prefix := strings.Repeat("AAAA", 16)
	cases := []struct {
		name   string
		audio  string
		accept bool
	}{
		{"empty", "", true},
		{"exactly 1 MiB", base64.StdEncoding.EncodeToString(make([]byte, 1<<20)), true},
		{"over 1 MiB", base64.StdEncoding.EncodeToString(make([]byte, 1<<20+2)), false},
		{"odd PCM length", base64.StdEncoding.EncodeToString(make([]byte, 41)), false},
		{"line breaks ignored", prefix + "\r\n" + prefix, true},
		{"nonzero trailing bits", prefix + "AQACAB==", true},
		{"invalid character", prefix + "AQACAA=!", false},
		{"missing padding", prefix + "AQACAA=", false},
		{"padding mid-stream", "AQ==" + prefix, false},
		{"URL alphabet", prefix + "-_-_", false},
	}
	start := []byte(`{"type":"session.start","config":{"input_audio_format":"pcm16","sample_rate_hz":16000,"num_channels":1}}`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started, closing := false, false
			if _, err := dictationInput(start, &started, &closing); err != nil {
				t.Fatal(err)
			}
			payload, errMarshal := json.Marshal(map[string]string{"type": "audio.append", "audio": tc.audio})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			frame, err := dictationInput(payload, &started, &closing)
			if !tc.accept {
				if err == nil {
					t.Fatal("invalid audio accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("valid audio rejected: %v", err)
			}
			var forwarded struct {
				Type  string `json:"type"`
				Audio string `json:"audio"`
			}
			if json.Unmarshal(frame, &forwarded) != nil || forwarded.Type != "audio.append" || forwarded.Audio != tc.audio {
				t.Fatal("audio not forwarded unchanged")
			}
		})
	}
}
