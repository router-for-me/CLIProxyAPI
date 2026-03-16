"""OCR tools: extract text from scanned documents.

Uses tesseract for Portuguese OCR and pdf2image for PDF conversion.
Designed for Brazilian legal documents (petições, certidões, despachos).
"""

import logging
import os
import tempfile

from agent import tool

LOGGER = logging.getLogger("shadow_claw_gateway.tools.ocr")

_MAX_PAGES = 20
_MAX_TEXT_CHARS = 8000


def _pdf_to_images(pdf_path: str, max_pages: int = _MAX_PAGES) -> list:
    """Convert PDF pages to PIL images."""
    try:
        from pdf2image import convert_from_path
    except ImportError:
        raise RuntimeError("pdf2image not installed. Run: pip install pdf2image")
    return convert_from_path(pdf_path, first_page=1, last_page=max_pages, dpi=300)


def _ocr_image(image, lang: str = "por") -> str:
    """Run tesseract OCR on a PIL image."""
    try:
        import pytesseract
    except ImportError:
        raise RuntimeError(
            "pytesseract not installed. Run: pip install pytesseract\n"
            "Also install tesseract: apt install tesseract-ocr tesseract-ocr-por"
        )
    return pytesseract.image_to_string(image, lang=lang)


@tool(
    "ocr_document",
    "Extract text from a scanned PDF or image using OCR. "
    "Optimized for Brazilian Portuguese legal documents.",
    {
        "type": "object",
        "properties": {
            "file_path": {
                "type": "string",
                "description": "Path to the PDF or image file",
            },
            "language": {
                "type": "string",
                "description": "OCR language code (default: 'por' for Portuguese)",
            },
        },
        "required": ["file_path"],
    },
)
async def ocr_document(file_path: str, language: str = "por") -> str:
    if not os.path.isfile(file_path):
        return f"File not found: {file_path}"

    # Sanitise: only allow files under /tmp or the gateway data dir
    abs_path = os.path.abspath(file_path)
    allowed_prefixes = ("/tmp/", os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "data")) + "/")
    if not any(abs_path.startswith(p) for p in allowed_prefixes):
        return "Access denied: file must be in /tmp/ or the gateway data directory."

    ext = os.path.splitext(file_path)[1].lower()

    try:
        if ext == ".pdf":
            images = _pdf_to_images(file_path)
            texts = []
            for i, img in enumerate(images, 1):
                page_text = _ocr_image(img, lang=language)
                if page_text.strip():
                    texts.append(f"--- Page {i} ---\n{page_text.strip()}")
            full_text = "\n\n".join(texts)
        elif ext in (".png", ".jpg", ".jpeg", ".tiff", ".bmp"):
            from PIL import Image
            img = Image.open(file_path)
            full_text = _ocr_image(img, lang=language)
        else:
            return f"Unsupported file type: {ext}. Supported: PDF, PNG, JPG, TIFF, BMP."

    except RuntimeError as e:
        return str(e)
    except Exception as e:
        LOGGER.exception("OCR failed for %s", file_path)
        return f"OCR failed: {e}"

    if not full_text.strip():
        return "No text could be extracted from the document."

    if len(full_text) > _MAX_TEXT_CHARS:
        full_text = full_text[:_MAX_TEXT_CHARS] + "\n\n... (truncated — full text too long)"

    return f"OCR result ({len(full_text)} chars):\n\n{full_text}"
