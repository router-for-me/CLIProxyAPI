"""Voice tools: text-to-speech generation.

Inspired by nari-labs/dia. Uses available TTS engines to generate audio.
Uses argument-list subprocess calls to prevent command injection.
"""

import asyncio
import os
import re
import tempfile

from agent import tool

# Only allow safe language codes: letters, digits, hyphens, underscores
_LANG_RE = re.compile(r"^[a-zA-Z0-9_-]+$")


@tool(
    "voice_speak",
    "Convert text to speech and generate an audio file. "
    "The audio file path is returned for sending as a Telegram voice message.",
    {
        "type": "object",
        "properties": {
            "text": {
                "type": "string",
                "description": "Text to convert to speech",
            },
            "language": {
                "type": "string",
                "description": "Language code (e.g., 'en', 'pt', 'es'). Defaults to 'en'.",
            },
        },
        "required": ["text"],
    },
)
async def voice_speak(text: str, language: str = "en") -> str:
    if not text.strip():
        return "No text provided for speech generation."

    # Validate language code — reject anything suspicious
    lang = language[:10]
    if not _LANG_RE.match(lang):
        lang = "en"

    # Truncate text for safety
    safe_text = text[:500]

    audio_path = os.path.join(tempfile.gettempdir(), "shadow_claw_tts.mp3")

    # Argument-list subprocess — no shell interpretation, safe from injection
    for cmd in [
        ["espeak-ng", "-v", lang, "-w", audio_path, safe_text],
        ["espeak", "-v", lang, "-w", audio_path, safe_text],
        ["say", "-o", audio_path, "--data-format=LEF32@22050", safe_text],
    ]:
        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            await asyncio.wait_for(proc.wait(), timeout=30)
            if proc.returncode == 0 and os.path.exists(audio_path):
                size = os.path.getsize(audio_path)
                return f"Audio generated: {audio_path} ({size} bytes, language={lang})"
        except (asyncio.TimeoutError, FileNotFoundError, OSError):
            continue

    # Fallback: try pyttsx3 (Python TTS library)
    try:
        import pyttsx3
        engine = pyttsx3.init()
        engine.save_to_file(safe_text, audio_path)
        engine.runAndWait()
        if os.path.exists(audio_path):
            return f"Audio generated: {audio_path} ({os.path.getsize(audio_path)} bytes)"
    except (ImportError, Exception):
        pass

    return "TTS failed: no supported speech engine found (espeak-ng, espeak, say, pyttsx3)."
