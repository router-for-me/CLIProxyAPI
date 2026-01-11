# CLIProxyAPI TTS (Text-to-Speech) Setup Guide

This document explains how to enable and use Gemini TTS models through CLIProxyAPI.

**Tested on:** CLIProxyAPI v6.6.98

## Overview

CLIProxyAPI can proxy Google's Gemini TTS models, allowing you to generate speech from text through the same API interface used for chat completions.

### Supported TTS Models

| Model ID | Description | Best For |
|----------|-------------|----------|
| `gemini-2.5-flash-preview-tts` | Fast TTS model | Real-time applications, low latency |
| `gemini-2.5-pro-preview-tts` | High-quality TTS model | Quality/expressivity, professional audio |
| `gemini-2.5-flash-native-audio-preview` | Multimodal audio model | Audio input/output, conversations |

### Available Voices

28 prebuilt voices including:
- **Female:** Kore, Zephyr, Leda, Aoede, Sulafat, and more
- **Male:** Charon, Puck, Enceladus, Iapetus, and more

## Prerequisites

1. CLIProxyAPI installed and running
2. AI Studio WebSocket connection configured (for `aistudio` provider)
3. `ffmpeg` installed (for audio conversion)

## Setup

### 1. Add TTS Model Definitions

The TTS models must be added to CLIProxyAPI's model definitions. Edit:

```
internal/registry/model_definitions.go
```

Add the following to `GetGeminiModels()`, `GetGeminiCLIModels()`, and `GetAIStudioModels()`:

```go
// TTS (Text-to-Speech) models
{
    ID:                         "gemini-2.5-flash-preview-tts",
    Object:                     "model",
    Created:                    1762300800,
    OwnedBy:                    "google",
    Type:                       "gemini",
    Name:                       "models/gemini-2.5-flash-preview-tts",
    Version:                    "2.5",
    DisplayName:                "Gemini 2.5 Flash TTS",
    Description:                "Text-to-speech model optimized for low latency.",
    InputTokenLimit:            32768,
    OutputTokenLimit:           8192,
    SupportedGenerationMethods: []string{"generateContent"},
},
{
    ID:                         "gemini-2.5-pro-preview-tts",
    Object:                     "model",
    Created:                    1762300800,
    OwnedBy:                    "google",
    Type:                       "gemini",
    Name:                       "models/gemini-2.5-pro-preview-tts",
    Version:                    "2.5",
    DisplayName:                "Gemini 2.5 Pro TTS",
    Description:                "Text-to-speech model optimized for quality.",
    InputTokenLimit:            32768,
    OutputTokenLimit:           8192,
    SupportedGenerationMethods: []string{"generateContent"},
},
{
    ID:                         "gemini-2.5-flash-native-audio-preview",
    Object:                     "model",
    Created:                    1762300800,
    OwnedBy:                    "google",
    Type:                       "gemini",
    Name:                       "models/gemini-2.5-flash-native-audio-preview",
    Version:                    "2.5",
    DisplayName:                "Gemini 2.5 Flash Native Audio",
    Description:                "Multimodal model with native audio capabilities.",
    InputTokenLimit:            1048576,
    OutputTokenLimit:           8192,
    SupportedGenerationMethods: []string{"generateContent"},
},
```

### 2. Rebuild CLIProxyAPI

```bash
cd /Users/kyin/CLIProxyAPI
go build -o cli-proxy-api ./cmd/server
```

### 3. Restart Server & Reconnect AI Studio

```bash
# Kill existing server
pkill -9 -f 'cli-proxy-api'

# Start server
./cli-proxy-api &
```

**Important:** After restarting, you must reconnect your AI Studio WebSocket for the new models to appear. The TTS models are only exposed through the `aistudio` provider.

### 4. Verify Models

```bash
curl -s http://localhost:8317/v1/models -H "Authorization: Bearer sk-proxy" | \
  jq '.data[] | select(.id | test("tts|audio"))'
```

Expected output:
```json
{"id": "gemini-2.5-flash-preview-tts", ...}
{"id": "gemini-2.5-pro-preview-tts", ...}
{"id": "gemini-2.5-flash-native-audio-preview", ...}
```

## Usage

### API Request Format

TTS uses the Gemini native API format (not OpenAI format):

```bash
curl -X POST "http://localhost:8317/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" \
  -H "Authorization: Bearer sk-proxy" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"parts": [{"text": "Hello, this is a test."}]}],
    "generationConfig": {
      "responseModalities": ["AUDIO"],
      "speechConfig": {
        "voiceConfig": {
          "prebuiltVoiceConfig": {"voiceName": "Kore"}
        }
      }
    }
  }'
```

### Response Format

The response contains base64-encoded PCM audio:

```json
{
  "candidates": [{
    "content": {
      "parts": [{
        "inlineData": {
          "mimeType": "audio/pcm",
          "data": "BASE64_ENCODED_PCM_AUDIO..."
        }
      }]
    }
  }]
}
```

### Audio Specifications

- **Format:** PCM (base64 encoded)
- **Sample Rate:** 24,000 Hz
- **Channels:** Mono (1)
- **Bit Depth:** 16-bit signed

### Converting to Playable Audio

```bash
# Extract base64 data and decode to PCM
echo "$RESPONSE" | jq -r '.candidates[0].content.parts[0].inlineData.data' | base64 -d > audio.pcm

# Convert PCM to WAV
ffmpeg -f s16le -ar 24000 -ac 1 -i audio.pcm audio.wav

# Play (macOS)
afplay audio.wav
```

## Helper Script

A convenience script is provided at `scripts/tts.sh`:

```bash
# Basic usage
./scripts/tts.sh "Your text here"

# With specific voice
./scripts/tts.sh "Your text here" Kore

# With specific model
./scripts/tts.sh "Your text here" Kore gemini-2.5-pro-preview-tts
```

The script automatically:
- Calls the TTS API
- Decodes the base64 audio
- Converts to WAV
- Plays the audio
- Cleans up temp files

## Git Setup (Private Fork)

To maintain TTS changes while staying updated with upstream:

### Repository Structure

| Remote | URL | Purpose |
|--------|-----|---------|
| `origin` | github.com/router-for-me/CLIProxyAPI | Upstream (official) |
| `fork` | github.com/yinkev/CLIProxyAPI | Your private fork |

### Branches

- `main` - Tracks upstream
- `tts-models` - Your TTS changes (on fork)

### Update Workflow

When upstream releases a new version (e.g., updating from v6.6.98 to v6.6.100):

```bash
# 1. Fetch latest tags
git fetch origin --tags

# 2. Check current base version
git describe --tags --always
# Output: v6.6.98-5-gb5b24a3 means you're on v6.6.98 + 5 commits

# 3. Rebase TTS commits onto new version
# Syntax: git rebase --onto <new-base> <old-base> <branch>
git rebase --onto v6.6.100 v6.6.98 tts-models

# 4. Push to your fork (force needed after rebase)
git push fork tts-models --force

# 5. Rebuild
go build -o cli-proxy-api ./cmd/server
```

**Note:** Git will automatically skip commits already in the new version. Only your TTS commits will be reapplied.

### Quick Update Script

```bash
#!/bin/bash
# update-cliproxy.sh
# Usage: ./update-cliproxy.sh v6.6.100

set -e
cd /Users/kyin/CLIProxyAPI

NEW_VERSION=${1:?Usage: $0 <new-version>}
CURRENT_BASE=$(git describe --tags --abbrev=0)

echo "Updating from $CURRENT_BASE to $NEW_VERSION..."

git fetch origin --tags
git rebase --onto "$NEW_VERSION" "$CURRENT_BASE" tts-models
git push fork tts-models --force
go build -o cli-proxy-api ./cmd/server

echo "Done! Now at $(git describe --tags --always)"
echo "Restart the server to apply changes."
```

## Troubleshooting

### TTS models not showing in /v1/models

1. Verify models are in `model_definitions.go`
2. Rebuild: `go build -o cli-proxy-api ./cmd/server`
3. Restart server
4. **Reconnect AI Studio WebSocket** (models only load when aistudio connects)

### No audio in response

- Ensure `responseModalities: ["AUDIO"]` is set
- Check voice name is valid (e.g., "Kore", "Zephyr")

### Audio plays but sounds wrong

- Verify ffmpeg conversion uses correct parameters:
  - Sample rate: 24000 Hz
  - Channels: 1 (mono)
  - Format: s16le (signed 16-bit little-endian)

### Rebase conflicts

If `git rebase origin/main` has conflicts in `model_definitions.go`:
1. Open the file and resolve conflicts (keep both upstream changes and your TTS additions)
2. `git add internal/registry/model_definitions.go`
3. `git rebase --continue`

## Files Modified

| File | Changes |
|------|---------|
| `internal/registry/model_definitions.go` | Added TTS model definitions |
| `scripts/tts.sh` | Helper script for TTS |
| `docs/TTS-SETUP.md` | This documentation |

## References

- [Gemini TTS Documentation](https://ai.google.dev/gemini-api/docs/speech-generation)
- [CLIProxyAPI GitHub](https://github.com/router-for-me/CLIProxyAPI)
- [Your Fork](https://github.com/yinkev/CLIProxyAPI)
