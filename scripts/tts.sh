#!/bin/bash
# TTS Helper Script for CLIProxyAPI
# Uses the OpenAI-compatible /v1/audio/speech endpoint
#
# Usage: ./tts.sh "Text to speak" [voice] [model] [format]
#
# Examples:
#   ./tts.sh "Hello world"
#   ./tts.sh "Hello world" Kore
#   ./tts.sh "Hello world" alloy tts-1 mp3
#
# Voices (OpenAI): alloy, echo, fable, nova, onyx, shimmer
# Voices (Gemini): Kore, Puck, Charon, Zephyr, Leda, Aoede, etc.
# Models: tts-1, tts-1-hd, gemini-2.5-flash-preview-tts, gemini-2.5-pro-preview-tts
# Formats: mp3, wav, opus, aac, flac, pcm

set -e

TEXT="${1:-Hello world}"
VOICE="${2:-Kore}"
MODEL="${3:-tts-1}"
FORMAT="${4:-mp3}"
API_URL="http://localhost:8317"
API_KEY="sk-proxy"

# Output file
OUTPUT="/tmp/tts_output.${FORMAT}"

echo "Speaking: \"$TEXT\""
echo "Voice: $VOICE | Model: $MODEL | Format: $FORMAT"

# Call OpenAI-compatible TTS endpoint
HTTP_CODE=$(curl -s -w "%{http_code}" -X POST "$API_URL/v1/audio/speech" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"input\":\"$TEXT\",\"voice\":\"$VOICE\",\"response_format\":\"$FORMAT\"}" \
  -o "$OUTPUT")

# Check response
if [ "$HTTP_CODE" != "200" ]; then
  echo "Error: HTTP $HTTP_CODE"
  cat "$OUTPUT"
  exit 1
fi

# Check if output file has content
if [ ! -s "$OUTPUT" ]; then
  echo "Error: Empty response"
  exit 1
fi

echo "Audio saved to: $OUTPUT"

# Play audio (macOS: afplay, Linux: aplay or paplay)
if command -v afplay &> /dev/null; then
  afplay "$OUTPUT"
elif command -v paplay &> /dev/null; then
  paplay "$OUTPUT"
elif command -v aplay &> /dev/null; then
  aplay "$OUTPUT"
else
  echo "No audio player found. File saved to $OUTPUT"
fi

echo "Done!"
