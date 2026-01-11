#!/bin/bash
# TTS Helper Script for CLIProxyAPI
# Usage: ./tts.sh "Text to speak" [voice]
# Voices: Kore, Zephyr, Charon, Puck, Leda, etc.

set -e

TEXT="${1:-Hello world}"
VOICE="${2:-Kore}"
MODEL="${3:-gemini-2.5-flash-preview-tts}"
API_URL="http://localhost:8317"
API_KEY="sk-proxy"

# Create temp directory for this session
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

# Build request JSON
REQUEST_JSON=$(cat <<EOF
{
  "contents": [{"parts": [{"text": "$TEXT"}]}],
  "generationConfig": {
    "responseModalities": ["AUDIO"],
    "speechConfig": {
      "voiceConfig": {
        "prebuiltVoiceConfig": {"voiceName": "$VOICE"}
      }
    }
  }
}
EOF
)

echo "Speaking: \"$TEXT\" (voice: $VOICE)"

# Call TTS API
RESPONSE=$(curl -s -X POST "$API_URL/v1beta/models/$MODEL:generateContent" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "$REQUEST_JSON")

# Check for error
if echo "$RESPONSE" | jq -e '.error' > /dev/null 2>&1; then
  echo "Error: $(echo "$RESPONSE" | jq -r '.error.message // .error')"
  exit 1
fi

# Extract and decode audio
AUDIO_DATA=$(echo "$RESPONSE" | jq -r '.candidates[0].content.parts[0].inlineData.data')

if [ -z "$AUDIO_DATA" ] || [ "$AUDIO_DATA" = "null" ]; then
  echo "Error: No audio data in response"
  echo "$RESPONSE" | jq '.'
  exit 1
fi

# Decode base64 to PCM
echo "$AUDIO_DATA" | base64 -d > "$TMP_DIR/audio.pcm"

# Convert PCM to WAV (24kHz, 16-bit, mono)
ffmpeg -y -f s16le -ar 24000 -ac 1 -i "$TMP_DIR/audio.pcm" "$TMP_DIR/audio.wav" 2>/dev/null

# Play audio
afplay "$TMP_DIR/audio.wav"

echo "Done!"
# Temp files auto-cleaned by trap on EXIT
