package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestRunSendsAndReceivesSpeechAudio(t *testing.T) {
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.wav")
	outputPath := filepath.Join(tmpDir, "response.wav")
	inputPCM := make([]byte, 9602)
	for index := range inputPCM {
		inputPCM[index] = byte(index % 251)
	}
	if errWrite := writePCM16WAV(inputPath, inputPCM); errWrite != nil {
		t.Fatalf("write input WAV: %v", errWrite)
	}
	responsePCM := []byte{10, 20, 30, 40, 50, 60, 70, 80}

	websocketEvents := make(chan []string, 1)
	capturedInput := make(chan []byte, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/realtime/client_secrets":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer proxy-key" {
				http.Error(writer, "invalid client secret request", http.StatusUnauthorized)
				return
			}
			var body map[string]any
			if errDecode := json.NewDecoder(request.Body).Decode(&body); errDecode != nil || !validAudioSession(body) {
				http.Error(writer, "invalid audio session", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{
				"value":"ek_test",
				"expires_at":4102444800,
				"session":{"id":"sess_test","object":"realtime.session","type":"realtime","model":"gpt-realtime"}
			}`))
		case "/v1/realtime":
			if request.Header.Get("Authorization") != "Bearer ek_test" || request.URL.Query().Get("model") != defaultModel {
				http.Error(writer, "invalid websocket request", http.StatusUnauthorized)
				return
			}
			connection, errUpgrade := upgrader.Upgrade(writer, request, nil)
			if errUpgrade != nil {
				return
			}
			defer func() {
				if errClose := connection.Close(); errClose != nil {
					t.Logf("close test websocket: %v", errClose)
				}
			}()

			types := make([]string, 0, 4)
			var receivedPCM bytes.Buffer
			for {
				_, payload, errRead := connection.ReadMessage()
				if errRead != nil {
					return
				}
				var event struct {
					Type  string `json:"type"`
					Audio string `json:"audio"`
				}
				if errUnmarshal := json.Unmarshal(payload, &event); errUnmarshal != nil {
					return
				}
				types = append(types, event.Type)
				if event.Type == "input_audio_buffer.append" {
					audio, errDecode := base64.StdEncoding.DecodeString(event.Audio)
					if errDecode != nil {
						return
					}
					_, _ = receivedPCM.Write(audio)
				}
				if event.Type == "response.create" {
					break
				}
			}
			websocketEvents <- types
			capturedInput <- append([]byte(nil), receivedPCM.Bytes()...)
			midpoint := len(responsePCM) / 2
			for _, audio := range [][]byte{responsePCM[:midpoint], responsePCM[midpoint:]} {
				if errWrite := connection.WriteJSON(map[string]any{
					"type":  "response.output_audio.delta",
					"delta": base64.StdEncoding.EncodeToString(audio),
				}); errWrite != nil {
					return
				}
			}
			if errWrite := connection.WriteJSON(map[string]any{"type": "response.output_audio_transcript.delta", "delta": "Voice response"}); errWrite != nil {
				return
			}
			_ = connection.WriteJSON(map[string]any{"type": "response.done", "response": map[string]any{"status": "completed"}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	baseURL, errBaseURL := normalizeBaseURL(server.URL + "/v1/")
	if errBaseURL != nil {
		t.Fatalf("normalizeBaseURL() error = %v", errBaseURL)
	}
	var output bytes.Buffer
	errRun := run(context.Background(), appConfig{
		baseURL:      baseURL,
		apiKey:       "proxy-key",
		model:        defaultModel,
		inputWAV:     inputPath,
		outputWAV:    outputPath,
		instructions: defaultInstructions,
		voice:        defaultVoice,
	}, &output)
	if errRun != nil {
		t.Fatalf("run() error = %v", errRun)
	}
	if !strings.Contains(output.String(), "Sent") || !strings.Contains(output.String(), "Assistant transcript: Voice response") || !strings.Contains(output.String(), "Saved spoken response") {
		t.Fatalf("output = %q", output.String())
	}
	select {
	case events := <-websocketEvents:
		want := []string{"input_audio_buffer.append", "input_audio_buffer.append", "input_audio_buffer.commit", "response.create"}
		if strings.Join(events, ",") != strings.Join(want, ",") {
			t.Fatalf("client events = %v, want %v", events, want)
		}
	default:
		t.Fatal("websocket events were not captured")
	}
	select {
	case audio := <-capturedInput:
		if !bytes.Equal(audio, inputPCM) {
			t.Fatalf("input PCM mismatch: got %d bytes, want %d", len(audio), len(inputPCM))
		}
	default:
		t.Fatal("input audio was not captured")
	}
	actualResponsePCM, errRead := readPCM16WAV(outputPath)
	if errRead != nil {
		t.Fatalf("read output WAV: %v", errRead)
	}
	if !bytes.Equal(actualResponsePCM, responsePCM) {
		t.Fatalf("response PCM = %v, want %v", actualResponsePCM, responsePCM)
	}
}

func validAudioSession(body map[string]any) bool {
	session, ok := body["session"].(map[string]any)
	if !ok || session["type"] != "realtime" || session["model"] != defaultModel {
		return false
	}
	modalities, ok := session["output_modalities"].([]any)
	if !ok || len(modalities) != 1 || modalities[0] != "audio" {
		return false
	}
	audio, ok := session["audio"].(map[string]any)
	if !ok {
		return false
	}
	input, inputOK := audio["input"].(map[string]any)
	output, outputOK := audio["output"].(map[string]any)
	if !inputOK || !outputOK {
		return false
	}
	inputFormat, inputFormatOK := input["format"].(map[string]any)
	outputFormat, outputFormatOK := output["format"].(map[string]any)
	if !inputFormatOK || !outputFormatOK {
		return false
	}
	_, turnDetectionPresent := input["turn_detection"]
	return inputFormat["type"] == "audio/pcm" && inputFormat["rate"] == float64(audioSampleRate) &&
		outputFormat["type"] == "audio/pcm" && outputFormat["rate"] == float64(audioSampleRate) &&
		output["voice"] == defaultVoice && turnDetectionPresent && input["turn_detection"] == nil
}

func TestNormalizeBaseURLAddsV1(t *testing.T) {
	baseURL, errNormalize := normalizeBaseURL("http://127.0.0.1:8317/")
	if errNormalize != nil {
		t.Fatalf("normalizeBaseURL() error = %v", errNormalize)
	}
	if baseURL != "http://127.0.0.1:8317/v1" {
		t.Fatalf("baseURL = %q", baseURL)
	}
	websocketURL, errWebsocketURL := realtimeWebsocketURL(baseURL, defaultModel)
	if errWebsocketURL != nil {
		t.Fatalf("realtimeWebsocketURL() error = %v", errWebsocketURL)
	}
	wantWebsocketURL := "ws://127.0.0.1:8317/v1/realtime?model=" + defaultModel
	if websocketURL != wantWebsocketURL {
		t.Fatalf("websocketURL = %q", websocketURL)
	}
}

func TestReadPCM16WAVRejectsWrongSampleRate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong-rate.wav")
	if errWrite := writePCM16WAV(path, []byte{1, 2, 3, 4}); errWrite != nil {
		t.Fatalf("writePCM16WAV() error = %v", errWrite)
	}
	payload, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read WAV: %v", errRead)
	}
	payload[24] = 0x80
	payload[25] = 0xbb
	payload[26] = 0x00
	payload[27] = 0x00
	if errWrite := os.WriteFile(path, payload, 0o644); errWrite != nil {
		t.Fatalf("rewrite WAV: %v", errWrite)
	}
	if _, errRead = readPCM16WAV(path); errRead == nil || !strings.Contains(errRead.Error(), "24000") {
		t.Fatalf("readPCM16WAV() error = %v", errRead)
	}
}
