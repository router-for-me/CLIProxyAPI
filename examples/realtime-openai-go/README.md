# OpenAI Go SDK Realtime Voice Example

This example sends spoken audio to CLIProxyAPI and saves the model's spoken reply as a WAV file.

It uses the official [`github.com/openai/openai-go/v3`](https://github.com/openai/openai-go) SDK to create a short-lived Realtime client secret. The official Go SDK currently exposes the Realtime REST resources but does not provide a WebSocket connection helper, so `github.com/gorilla/websocket` is used for the standard Realtime audio events.

## Prerequisites

1. Start CLIProxyAPI with at least one working ChatGPT/Codex OAuth credential.
2. Configure a proxy API key in `config.yaml`.
3. Use Go 1.26 or newer.
4. Prepare a PCM WAV file with these exact properties:
   - 24,000 Hz sample rate
   - 16-bit signed PCM
   - mono
   - little-endian

Convert an existing recording with FFmpeg:

```bash
ffmpeg -i recording.m4a -ar 24000 -ac 1 -c:a pcm_s16le question.wav
```

## Run

```bash
cd examples/realtime-openai-go

OPENAI_BASE_URL="http://127.0.0.1:8317/v1" \
OPENAI_API_KEY="your-proxy-api-key" \
OPENAI_REALTIME_MODEL="gpt-realtime-2.1" \
OPENAI_REALTIME_INPUT_WAV="question.wav" \
OPENAI_REALTIME_OUTPUT_WAV="response.wav" \
go run .
```

Expected output:

```text
Loaded question.wav (2.4s, 115200 PCM bytes)
Connected to ws://127.0.0.1:8317/v1/realtime?model=gpt-realtime-2.1 using model gpt-realtime-2.1 and voice marin
Sent 2.4s of speech audio
Assistant transcript: The connection is working correctly.
Saved spoken response to response.wav (1.8s, 86400 PCM bytes)
```

Play the response:

```bash
# macOS
afplay response.wav

# Linux
aplay response.wav

# Cross-platform with FFmpeg
ffplay -autoexit response.wav
```

## Environment variables

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `OPENAI_API_KEY` | Yes | — | API key configured for CLIProxyAPI. |
| `OPENAI_REALTIME_INPUT_WAV` | Yes | — | Input speech WAV file. It must be 24kHz, 16-bit, mono PCM. |
| `OPENAI_REALTIME_OUTPUT_WAV` | No | `response.wav` | Destination for the spoken response. |
| `OPENAI_BASE_URL` | No | `http://127.0.0.1:8317/v1` | CLIProxyAPI OpenAI-compatible base URL. `/v1` is added when the URL has no path. |
| `OPENAI_REALTIME_MODEL` | No | `gpt-realtime-2.1` | Standard Realtime model name. CLIProxyAPI uses it for the upstream standard WebSocket while selecting a compatible Codex OAuth credential internally. |
| `OPENAI_REALTIME_VOICE` | No | `marin` | Realtime output voice. Other common values include `cedar`, `alloy`, `ash`, `coral`, and `echo`. |
| `OPENAI_REALTIME_INSTRUCTIONS` | No | Short spoken response instruction | Session instructions attached to the client secret. |
| `OPENAI_REALTIME_DEBUG` | No | `false` | Print every received Realtime server event. |

## Audio flow

1. The official OpenAI Go SDK calls `POST /v1/realtime/client_secrets` with an audio session configured for 24kHz PCM input and output.
2. The returned local `ek_...` credential authenticates the `/v1/realtime` WebSocket.
3. Input WAV samples are sent in 200ms `input_audio_buffer.append` chunks.
4. The client sends `input_audio_buffer.commit` and `response.create`.
5. Base64 `response.output_audio.delta` events are decoded and written to the output WAV.

The client secret returned by CLIProxyAPI is local to that proxy instance and is not valid against `api.openai.com`.

## Test

```bash
go test -race ./...
```

The test starts an in-process HTTP/WebSocket server and verifies client-secret configuration, input audio streaming, output audio decoding, and WAV generation.
