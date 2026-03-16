"""Transcription tools: audio/voice to text via Whisper.

Processes audio files sent via Telegram (voice messages, audio files)
and returns PT-BR transcriptions with optional summarization.
"""

import asyncio
import logging
import os
import shutil

from agent import tool

LOGGER = logging.getLogger("shadow_claw_gateway.tools.transcription")

_SUPPORTED_FORMATS = frozenset({".ogg", ".mp3", ".wav", ".m4a", ".flac", ".opus", ".wma"})
_MAX_DURATION_SECONDS = 7200  # 2 hours
_MAX_FILE_SIZE_MB = 100


async def _convert_to_wav(input_path: str) -> str:
    """Convert audio to WAV using ffmpeg if needed."""
    ext = os.path.splitext(input_path)[1].lower()
    if ext == ".wav":
        return input_path

    output_path = input_path.rsplit(".", 1)[0] + ".wav"
    proc = await asyncio.create_subprocess_exec(
        "ffmpeg", "-i", input_path, "-ar", "16000", "-ac", "1", "-y", output_path,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    await asyncio.wait_for(proc.communicate(), timeout=120)

    if proc.returncode != 0 or not os.path.exists(output_path):
        raise RuntimeError(f"ffmpeg conversion failed for {input_path}")

    return output_path


@tool(
    "transcribe_audio",
    "Transcribe audio or voice message to text using Whisper. "
    "Optimized for Brazilian Portuguese. Accepts .ogg, .mp3, .wav, .m4a.",
    {
        "type": "object",
        "properties": {
            "file_path": {
                "type": "string",
                "description": "Path to the audio file",
            },
            "language": {
                "type": "string",
                "description": "Language code (default: 'pt' for Portuguese)",
            },
        },
        "required": ["file_path"],
    },
)
async def transcribe_audio(file_path: str, language: str = "pt") -> str:
    if not os.path.isfile(file_path):
        return f"Audio file not found: {file_path}"

    # Security: only allow files under /tmp/
    abs_path = os.path.abspath(file_path)
    if not abs_path.startswith("/tmp/"):
        return "Access denied: audio files must be in /tmp/"

    ext = os.path.splitext(file_path)[1].lower()
    if ext not in _SUPPORTED_FORMATS:
        return f"Unsupported audio format: {ext}. Supported: {', '.join(sorted(_SUPPORTED_FORMATS))}"

    # Check file size
    size_mb = os.path.getsize(file_path) / (1024 * 1024)
    if size_mb > _MAX_FILE_SIZE_MB:
        return f"File too large ({size_mb:.1f}MB). Maximum: {_MAX_FILE_SIZE_MB}MB."

    # Check for whisper
    if not shutil.which("whisper"):
        return "whisper not installed. Run: pip install openai-whisper"

    # Check for ffmpeg
    if not shutil.which("ffmpeg"):
        return "ffmpeg not installed. Run: apt install ffmpeg"

    try:
        # Convert to WAV if needed
        wav_path = await _convert_to_wav(file_path)

        # Run whisper
        proc = await asyncio.create_subprocess_exec(
            "whisper", wav_path,
            "--language", language,
            "--model", "base",
            "--output_format", "txt",
            "--output_dir", os.path.dirname(wav_path),
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=600)

        if proc.returncode != 0:
            error = stderr.decode("utf-8", errors="replace")[:500]
            return f"Whisper transcription failed: {error}"

        # Read the output .txt file
        txt_path = wav_path.rsplit(".", 1)[0] + ".txt"
        if os.path.exists(txt_path):
            with open(txt_path, "r", encoding="utf-8") as f:
                transcript = f.read().strip()
        else:
            # Fallback: read from stdout
            transcript = stdout.decode("utf-8", errors="replace").strip()

        if not transcript:
            return "No speech detected in the audio."

        # Truncate if too long
        if len(transcript) > 8000:
            transcript = transcript[:8000] + "\n\n... (truncated)"

        duration_info = f" ({size_mb:.1f}MB)" if size_mb > 1 else ""
        return f"Transcription{duration_info}:\n\n{transcript}"

    except asyncio.TimeoutError:
        return "Transcription timed out (max 10 minutes). Try a shorter audio file."
    except Exception as e:
        LOGGER.exception("Transcription failed for %s", file_path)
        return f"Transcription failed: {e}"
