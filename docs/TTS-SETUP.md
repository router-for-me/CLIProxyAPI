# CLIProxyAPI TTS & Live Audio Guide

This document covers Text-to-Speech and real-time audio features in CLIProxyAPI.

**Tested on:** CLIProxyAPI v6.6.98

## Overview

CLIProxyAPI provides two audio interfaces:
1. **TTS API** (`/v1/audio/speech`) - OpenAI-compatible text-to-speech
2. **Live API** (`/v1/realtime`) - Real-time bidirectional audio/video via WebSocket

Both use Gemini models through AI Studio (free) - no API costs.

---

## Part 1: Text-to-Speech (TTS)

### Endpoint

```
POST /v1/audio/speech
```

### OpenAI-Compatible Request

```bash
curl -X POST http://localhost:8317/v1/audio/speech \
  -H "Authorization: Bearer sk-proxy" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "tts-1",
    "input": "Hello, world!",
    "voice": "alloy",
    "response_format": "mp3"
  }' \
  --output speech.mp3
```

### Supported Models

| Request Model | Maps To | Description |
|---------------|---------|-------------|
| `tts-1` | `gemini-2.5-flash-preview-tts` | Fast, low latency |
| `tts-1-hd` | `gemini-2.5-pro-preview-tts` | High quality |
| `gemini-2.5-flash-preview-tts` | (native) | Direct Gemini access |
| `gemini-2.5-pro-preview-tts` | (native) | Direct Gemini access |

### Voice Mapping

| OpenAI Voice | Gemini Voice | Characteristics |
|--------------|--------------|-----------------|
| `alloy` | Puck | Upbeat, energetic (default) |
| `echo` | Charon | Informative, clear |
| `fable` | Kore | Firm, confident |
| `nova` | Aoede | Breezy, light |
| `onyx` | Fenrir | Excitable, dynamic |
| `shimmer` | Leda | Youthful, energetic |

You can also use Gemini voices directly: Zephyr, Sulafat, Enceladus, Iapetus, etc.

### Output Formats

| Format | MIME Type | Use Case |
|--------|-----------|----------|
| `mp3` | audio/mpeg | Default, general use |
| `wav` | audio/wav | Low latency |
| `opus` | audio/ogg | Streaming, low bandwidth |
| `aac` | audio/aac | Mobile apps |
| `flac` | audio/flac | Lossless |
| `pcm` | audio/pcm | Raw 24kHz 16-bit |

### Advanced Features

#### Emotion Tags

Gemini supports emotion tags in the input text:

```bash
curl -X POST http://localhost:8317/v1/audio/speech \
  -H "Authorization: Bearer sk-proxy" \
  -d '{
    "model": "gemini-2.5-pro-preview-tts",
    "input": "[excited] Wow, this is amazing! [whispering] But keep it a secret.",
    "voice": "Puck"
  }' --output emotion.mp3
```

Supported tags: `[excited]`, `[sad]`, `[angry]`, `[whispering]`, `[laughing]`, `[sighing]`, etc.

#### Multi-Speaker

```bash
curl -X POST http://localhost:8317/v1/audio/speech \
  -H "Authorization: Bearer sk-proxy" \
  -d '{
    "model": "gemini-2.5-pro-preview-tts",
    "input": "Alice: Hi Bob! Bob: Hello Alice, how are you?",
    "speakers": [
      {"name": "Alice", "voice": "Kore"},
      {"name": "Bob", "voice": "Charon"}
    ]
  }' --output dialog.mp3
```

#### SSML Support

```bash
curl -X POST http://localhost:8317/v1/audio/speech \
  -H "Authorization: Bearer sk-proxy" \
  -d '{
    "model": "gemini-2.5-flash-preview-tts",
    "input": "Hello <break time=\"1s\"/> world. <prosody rate=\"slow\">This is slow.</prosody>",
    "voice": "Kore"
  }' --output ssml.mp3
```

### Python Example

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8317/v1",
    api_key="sk-proxy"
)

response = client.audio.speech.create(
    model="tts-1",
    voice="alloy",
    input="Hello from Python!"
)

response.stream_to_file("output.mp3")
```

---

## Part 2: Live API (Real-Time Audio/Video)

### Endpoint

```
WebSocket ws://localhost:8317/v1/realtime
```

### Overview

The Live API provides bidirectional real-time communication with Gemini's native audio model. Features:
- Real-time audio input/output
- Video frame input (1 FPS)
- Voice Activity Detection (VAD)
- "Deep Think" reasoning capability
- 10-minute session limit

### Connection

```javascript
const ws = new WebSocket('ws://localhost:8317/v1/realtime?key=YOUR_API_KEY');

// Send setup message
ws.send(JSON.stringify({
  setup: {
    model: "models/gemini-2.5-flash-native-audio-preview",
    generationConfig: {
      responseModalities: ["AUDIO"],
      speechConfig: {
        voiceConfig: {
          prebuiltVoiceConfig: { voiceName: "Puck" }
        }
      }
    }
  }
}));

// Wait for setupComplete, then send audio
ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.setupComplete) {
    console.log("Ready for audio!");
  }
};
```

### Audio Format

| Direction | Format | Sample Rate | Channels |
|-----------|--------|-------------|----------|
| Input | PCM 16-bit LE | 16 kHz | Mono |
| Output | PCM 16-bit LE | 24 kHz | Mono |

### Sending Audio

```javascript
// Send audio chunk (base64 encoded PCM)
ws.send(JSON.stringify({
  realtimeInput: {
    mediaChunks: [{
      mimeType: "audio/pcm;rate=16000",
      data: base64AudioData
    }]
  }
}));
```

### Sending Video Frame

```javascript
// Send video frame (base64 encoded JPEG)
ws.send(JSON.stringify({
  realtimeInput: {
    mediaChunks: [{
      mimeType: "image/jpeg",
      data: base64JpegData
    }]
  }
}));
```

### Receiving Audio

```javascript
ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.serverContent?.modelTurn?.parts) {
    for (const part of msg.serverContent.modelTurn.parts) {
      if (part.inlineData?.mimeType?.startsWith("audio/")) {
        // Decode and play audio
        const audioData = atob(part.inlineData.data);
        playAudio(audioData);
      }
    }
  }
};
```

### CLI Testing

```bash
# Install websocat
brew install websocat

# Connect to Live API
websocat ws://localhost:8317/v1/realtime?key=YOUR_API_KEY
```

---

## Prerequisites

1. **CLIProxyAPI** running on port 8317
2. **AI Studio WebSocket** connected (for free access)
3. **ffmpeg** installed (for TTS format conversion)

```bash
# macOS
brew install ffmpeg

# Ubuntu
sudo apt install ffmpeg
```

---

## Helper Scripts

### scripts/tts.sh

```bash
#!/bin/bash
# Usage: ./scripts/tts.sh "Your text" [voice] [model] [format]

TEXT="${1:-Hello world}"
VOICE="${2:-Kore}"
MODEL="${3:-tts-1}"
FORMAT="${4:-mp3}"
OUTPUT="/tmp/tts_output.${FORMAT}"

curl -s -X POST http://localhost:8317/v1/audio/speech \
  -H "Authorization: Bearer sk-proxy" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"${MODEL}\",\"input\":\"${TEXT}\",\"voice\":\"${VOICE}\",\"response_format\":\"${FORMAT}\"}" \
  -o "$OUTPUT"

afplay "$OUTPUT" 2>/dev/null || aplay "$OUTPUT" 2>/dev/null || echo "Audio saved to $OUTPUT"
```

---

## Troubleshooting

### TTS returns empty/error

1. Check AI Studio WebSocket is connected
2. Verify model is available: `curl http://localhost:8317/v1/models -H "Authorization: Bearer sk-proxy" | grep tts`
3. Check server logs for errors

### Live API connection fails

1. Provide API key via `?key=` query parameter
2. Check WebSocket upgrade is not blocked by proxy
3. Verify model name includes `models/` prefix

### Audio sounds wrong

- Ensure ffmpeg uses correct parameters:
  - TTS output: 24 kHz, mono, 16-bit
  - Live input: 16 kHz, mono, 16-bit
  - Live output: 24 kHz, mono, 16-bit

---

## Files Added/Modified

| File | Purpose |
|------|---------|
| `sdk/api/handlers/openai/audio_handlers.go` | OpenAI-compatible TTS endpoint |
| `sdk/api/handlers/live/live_handler.go` | Live API WebSocket relay |
| `internal/api/server.go` | Route registration |
| `docs/TTS-SETUP.md` | This documentation |

---

## References

- [Gemini TTS Documentation](https://ai.google.dev/gemini-api/docs/speech-generation)
- [Gemini Live API](https://ai.google.dev/gemini-api/docs/live)
- [OpenAI Audio API](https://platform.openai.com/docs/guides/text-to-speech)
