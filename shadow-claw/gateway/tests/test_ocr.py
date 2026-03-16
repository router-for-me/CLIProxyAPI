"""Tests for tools/ocr.py — OCR document processing."""

import os
import unittest
from unittest.mock import MagicMock, patch

from tools.ocr import ocr_document


class TestOcrDocument(unittest.IsolatedAsyncioTestCase):
    """Tests for ocr_document tool."""

    async def test_file_not_found(self):
        result = await ocr_document("/tmp/nonexistent.pdf")
        self.assertIn("File not found", result)

    async def test_path_traversal_blocked(self):
        """Disallow files outside /tmp/ and data/."""
        result = await ocr_document("/etc/passwd")
        self.assertIn("Access denied", result)

    async def test_unsupported_extension(self):
        # Create a temp .txt file
        path = "/tmp/test_ocr_unsupported.txt"
        with open(path, "w") as f:
            f.write("hello")
        try:
            result = await ocr_document(path)
            self.assertIn("Unsupported file type", result)
        finally:
            os.unlink(path)

    @patch("tools.ocr._pdf_to_images")
    @patch("tools.ocr._ocr_image")
    async def test_pdf_ocr_success(self, mock_ocr, mock_pdf):
        """Mocks pdf2image + pytesseract to verify the pipeline."""
        mock_img = MagicMock()
        mock_pdf.return_value = [mock_img, mock_img]
        mock_ocr.side_effect = ["Page 1 text content", "Page 2 text content"]

        # Create a dummy PDF file in /tmp
        path = "/tmp/test_ocr_doc.pdf"
        with open(path, "wb") as f:
            f.write(b"%PDF-1.4 dummy")
        try:
            result = await ocr_document(path)
            self.assertIn("Page 1", result)
            self.assertIn("Page 2", result)
            self.assertIn("OCR result", result)
            self.assertEqual(mock_ocr.call_count, 2)
        finally:
            os.unlink(path)

    @patch("tools.ocr._pdf_to_images")
    @patch("tools.ocr._ocr_image")
    async def test_empty_document(self, mock_ocr, mock_pdf):
        mock_pdf.return_value = [MagicMock()]
        mock_ocr.return_value = ""

        path = "/tmp/test_ocr_empty.pdf"
        with open(path, "wb") as f:
            f.write(b"%PDF-1.4 dummy")
        try:
            result = await ocr_document(path)
            self.assertIn("No text could be extracted", result)
        finally:
            os.unlink(path)

    @patch("tools.ocr._pdf_to_images", side_effect=RuntimeError("pdf2image not installed"))
    async def test_missing_dependency(self, _):
        path = "/tmp/test_ocr_nodep.pdf"
        with open(path, "wb") as f:
            f.write(b"%PDF-1.4 dummy")
        try:
            result = await ocr_document(path)
            self.assertIn("pdf2image not installed", result)
        finally:
            os.unlink(path)


if __name__ == "__main__":
    unittest.main()
