"""Tests for tools/transcription.py — Whisper audio transcription."""

import os
import unittest
from unittest.mock import AsyncMock, MagicMock, patch

from tools.transcription import transcribe_audio


class TestTranscribeAudio(unittest.IsolatedAsyncioTestCase):
    """Tests for transcribe_audio tool."""

    async def test_file_not_found(self):
        result = await transcribe_audio("/tmp/nonexistent.ogg")
        self.assertIn("Audio file not found", result)

    async def test_path_traversal_blocked(self):
        result = await transcribe_audio("/etc/passwd")
        self.assertIn("Access denied", result)

    async def test_unsupported_format(self):
        path = "/tmp/test_audio.xyz"
        with open(path, "w") as f:
            f.write("fake")
        try:
            result = await transcribe_audio(path)
            self.assertIn("Unsupported audio format", result)
        finally:
            os.unlink(path)

    @patch("tools.transcription.shutil.which", return_value=None)
    async def test_missing_whisper(self, _):
        path = "/tmp/test_audio.ogg"
        with open(path, "wb") as f:
            f.write(b"fake audio data")
        try:
            result = await transcribe_audio(path)
            self.assertIn("whisper not installed", result)
        finally:
            os.unlink(path)

    async def test_file_too_large(self):
        path = "/tmp/test_large_audio.ogg"
        # Create a file that appears to be >100MB via mock
        with open(path, "wb") as f:
            f.write(b"x")
        try:
            with patch("os.path.getsize", return_value=200 * 1024 * 1024):
                result = await transcribe_audio(path)
                self.assertIn("File too large", result)
        finally:
            os.unlink(path)


if __name__ == "__main__":
    unittest.main()
