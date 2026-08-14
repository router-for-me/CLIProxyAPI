package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/realtime"
)

const (
	defaultBaseURL      = "http://127.0.0.1:8317/v1"
	defaultModel        = "gpt-realtime-2.1"
	defaultInstructions = "Listen to the user's speech and reply with a short spoken response."
	defaultOutputWAV    = "response.wav"
	defaultVoice        = "marin"
	audioSampleRate     = 24000
	audioBytesPerSample = 2
	audioChunkDuration  = 200 * time.Millisecond
)

type appConfig struct {
	baseURL      string
	apiKey       string
	model        string
	inputWAV     string
	outputWAV    string
	instructions string
	voice        string
	debug        bool
}

type realtimeServerEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
	Response *struct {
		Status string `json:"status"`
	} `json:"response,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, errConfig := loadConfig()
	if errConfig != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", errConfig)
		os.Exit(1)
	}
	if errRun := run(ctx, cfg, os.Stdout); errRun != nil {
		fmt.Fprintf(os.Stderr, "realtime example failed: %v\n", errRun)
		os.Exit(1)
	}
}

func loadConfig() (appConfig, error) {
	baseURL, errBaseURL := normalizeBaseURL(envOrDefault("OPENAI_BASE_URL", defaultBaseURL))
	if errBaseURL != nil {
		return appConfig{}, errBaseURL
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return appConfig{}, errors.New("OPENAI_API_KEY is required")
	}
	inputWAV := strings.TrimSpace(os.Getenv("OPENAI_REALTIME_INPUT_WAV"))
	if inputWAV == "" {
		return appConfig{}, errors.New("OPENAI_REALTIME_INPUT_WAV is required")
	}
	return appConfig{
		baseURL:      baseURL,
		apiKey:       apiKey,
		model:        envOrDefault("OPENAI_REALTIME_MODEL", defaultModel),
		inputWAV:     inputWAV,
		outputWAV:    envOrDefault("OPENAI_REALTIME_OUTPUT_WAV", defaultOutputWAV),
		instructions: envOrDefault("OPENAI_REALTIME_INSTRUCTIONS", defaultInstructions),
		voice:        envOrDefault("OPENAI_REALTIME_VOICE", defaultVoice),
		debug:        strings.EqualFold(strings.TrimSpace(os.Getenv("OPENAI_REALTIME_DEBUG")), "true"),
	}, nil
}

func run(ctx context.Context, cfg appConfig, output io.Writer) error {
	inputPCM, errInput := readPCM16WAV(cfg.inputWAV)
	if errInput != nil {
		return fmt.Errorf("read input WAV: %w", errInput)
	}
	inputDuration := time.Duration(len(inputPCM)) * time.Second / (audioSampleRate * audioBytesPerSample)
	fmt.Fprintf(output, "Loaded %s (%s, %d PCM bytes)\n", cfg.inputWAV, inputDuration.Round(time.Millisecond), len(inputPCM))

	client := openai.NewClient(
		option.WithAPIKey(cfg.apiKey),
		option.WithBaseURL(cfg.baseURL),
	)
	pcmFormat := realtime.RealtimeAudioFormatsUnionParam{
		OfAudioPCM: &realtime.RealtimeAudioFormatsAudioPCMParam{
			Rate: audioSampleRate,
			Type: "audio/pcm",
		},
	}
	credentialCtx, cancelCredential := context.WithTimeout(ctx, 30*time.Second)
	secret, errSecret := client.Realtime.ClientSecrets.New(credentialCtx, realtime.ClientSecretNewParams{
		ExpiresAfter: realtime.ClientSecretNewParamsExpiresAfter{
			Anchor:  "created_at",
			Seconds: openai.Int(600),
		},
		Session: realtime.ClientSecretNewParamsSessionUnion{
			OfRealtime: &realtime.RealtimeSessionCreateRequestParam{
				Model:            realtime.RealtimeSessionCreateRequestModel(cfg.model),
				Instructions:     openai.String(cfg.instructions),
				OutputModalities: []string{"audio"},
				Audio: realtime.RealtimeAudioConfigParam{
					Input: realtime.RealtimeAudioConfigInputParam{
						Format: pcmFormat,
					},
					Output: realtime.RealtimeAudioConfigOutputParam{
						Format: pcmFormat,
						Voice: realtime.RealtimeAudioConfigOutputVoiceUnionParam{
							OfString: openai.String(cfg.voice),
						},
					},
				},
			},
		},
	}, option.WithJSONSet("session.audio.input.turn_detection", nil))
	cancelCredential()
	if errSecret != nil {
		return fmt.Errorf("create Realtime client secret with official SDK: %w", errSecret)
	}
	if secret == nil || strings.TrimSpace(secret.Value) == "" {
		return errors.New("official SDK returned an empty Realtime client secret")
	}

	websocketURL, errWebsocketURL := realtimeWebsocketURL(cfg.baseURL, cfg.model)
	if errWebsocketURL != nil {
		return errWebsocketURL
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+secret.Value)
	connection, response, errDial := websocket.DefaultDialer.DialContext(ctx, websocketURL, headers)
	if errDial != nil {
		return websocketHandshakeError(response, errDial)
	}
	var closeOnce sync.Once
	closeConnection := func() {
		closeOnce.Do(func() {
			if errClose := connection.Close(); errClose != nil && !websocket.IsCloseError(errClose, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				fmt.Fprintf(output, "warning: close websocket: %v\n", errClose)
			}
		})
	}
	defer closeConnection()

	connectionDone := make(chan struct{})
	defer close(connectionDone)
	go func() {
		select {
		case <-ctx.Done():
			closeConnection()
		case <-connectionDone:
		}
	}()

	fmt.Fprintf(output, "Connected to %s using model %s and voice %s\n", websocketURL, cfg.model, cfg.voice)
	if errSend := sendInputAudio(connection, inputPCM); errSend != nil {
		return errSend
	}
	fmt.Fprintf(output, "Sent %s of speech audio\n", inputDuration.Round(time.Millisecond))

	var responsePCM bytes.Buffer
	fmt.Fprint(output, "Assistant transcript: ")
	if errRead := readRealtimeResponse(ctx, connection, output, &responsePCM, cfg.debug); errRead != nil {
		return errRead
	}
	if responsePCM.Len() == 0 {
		return errors.New("Realtime response completed without audio")
	}
	if errWrite := writePCM16WAV(cfg.outputWAV, responsePCM.Bytes()); errWrite != nil {
		return fmt.Errorf("write output WAV: %w", errWrite)
	}
	responseDuration := time.Duration(responsePCM.Len()) * time.Second / (audioSampleRate * audioBytesPerSample)
	fmt.Fprintf(output, "Saved spoken response to %s (%s, %d PCM bytes)\n", cfg.outputWAV, responseDuration.Round(time.Millisecond), responsePCM.Len())
	return nil
}

func sendInputAudio(connection *websocket.Conn, pcm []byte) error {
	chunkSize := int(int64(audioSampleRate*audioBytesPerSample) * int64(audioChunkDuration) / int64(time.Second))
	for offset := 0; offset < len(pcm); offset += chunkSize {
		end := min(offset+chunkSize, len(pcm))
		if errWrite := connection.WriteJSON(map[string]any{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(pcm[offset:end]),
		}); errWrite != nil {
			return fmt.Errorf("append input audio: %w", errWrite)
		}
	}
	if errWrite := connection.WriteJSON(map[string]any{"type": "input_audio_buffer.commit"}); errWrite != nil {
		return fmt.Errorf("commit input audio: %w", errWrite)
	}
	if errWrite := connection.WriteJSON(map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"output_modalities": []string{"audio"},
		},
	}); errWrite != nil {
		return fmt.Errorf("request spoken Realtime response: %w", errWrite)
	}
	return nil
}

func readRealtimeResponse(ctx context.Context, connection *websocket.Conn, output io.Writer, audioOutput *bytes.Buffer, debug bool) error {
	for {
		_, payload, errRead := connection.ReadMessage()
		if errRead != nil {
			if errContext := ctx.Err(); errContext != nil {
				return errContext
			}
			if websocket.IsCloseError(errRead, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return errors.New("Realtime WebSocket closed before response.done")
			}
			return fmt.Errorf("read Realtime event: %w", errRead)
		}
		var event realtimeServerEvent
		if errUnmarshal := json.Unmarshal(payload, &event); errUnmarshal != nil {
			return fmt.Errorf("decode Realtime event: %w", errUnmarshal)
		}
		if debug {
			fmt.Fprintf(output, "\n[event] %s\n", payload)
		}
		switch event.Type {
		case "response.output_audio.delta", "response.audio.delta":
			audio, errDecode := base64.StdEncoding.DecodeString(event.Delta)
			if errDecode != nil {
				return fmt.Errorf("decode response audio delta: %w", errDecode)
			}
			if audioOutput.Len()+len(audio) > maxOutputPCMBytes {
				return fmt.Errorf("response PCM data exceeds %d bytes", maxOutputPCMBytes)
			}
			if _, errWrite := audioOutput.Write(audio); errWrite != nil {
				return fmt.Errorf("buffer response audio: %w", errWrite)
			}
		case "response.output_audio_transcript.delta", "response.audio_transcript.delta":
			fmt.Fprint(output, event.Delta)
		case "response.done":
			fmt.Fprintln(output)
			if event.Response != nil && event.Response.Status != "" && event.Response.Status != "completed" {
				return fmt.Errorf("Realtime response finished with status %s", event.Response.Status)
			}
			return nil
		case "error":
			if event.Error == nil {
				return errors.New("Realtime API returned an unspecified error")
			}
			return fmt.Errorf("Realtime API error %s/%s: %s", event.Error.Type, event.Error.Code, event.Error.Message)
		}
	}
}

func normalizeBaseURL(rawURL string) (string, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(rawURL))
	if errParse != nil {
		return "", fmt.Errorf("parse OPENAI_BASE_URL: %w", errParse)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("OPENAI_BASE_URL must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("OPENAI_BASE_URL must include a host")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if parsed.Path == "" {
		parsed.Path = "/v1"
	}
	return parsed.String(), nil
}

func realtimeWebsocketURL(baseURL, model string) (string, error) {
	parsed, errParse := url.Parse(baseURL)
	if errParse != nil {
		return "", fmt.Errorf("parse Realtime base URL: %w", errParse)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", errors.New("Realtime base URL must use http or https")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/realtime"
	query := parsed.Query()
	query.Set("model", model)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func websocketHandshakeError(response *http.Response, errDial error) error {
	if response == nil {
		return fmt.Errorf("connect Realtime WebSocket: %w", errDial)
	}
	body, errRead := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	errClose := response.Body.Close()
	if errRead != nil {
		return fmt.Errorf("connect Realtime WebSocket: HTTP %d; read response: %v; dial: %w", response.StatusCode, errRead, errDial)
	}
	if errClose != nil {
		return fmt.Errorf("connect Realtime WebSocket: HTTP %d; close response: %v; dial: %w", response.StatusCode, errClose, errDial)
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return fmt.Errorf("connect Realtime WebSocket: HTTP %d: %s: %w", response.StatusCode, message, errDial)
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
