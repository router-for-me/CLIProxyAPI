"""Tests for legal tools and webhook handler."""

import json
import unittest
from unittest.mock import MagicMock, patch

from handlers.legal_webhook import (
    Urgency,
    classify_urgency,
    parse_webhook_payload,
    format_telegram_alert,
    validate_webhook_signature,
)
from tools.legal import check_case_status, list_deadlines


class TestUrgencyClassification(unittest.TestCase):
    """Test court movement urgency classification."""

    def test_critical_when_prazo_short(self):
        self.assertEqual(
            classify_urgency("despacho", "manifestação em 3 dias", prazo_dias=3),
            Urgency.CRITICAL,
        )

    def test_critical_keyword_with_prazo(self):
        self.assertEqual(
            classify_urgency("intimação", "prazo para contestação", prazo_dias=10),
            Urgency.CRITICAL,
        )

    def test_important_keyword_without_prazo(self):
        self.assertEqual(
            classify_urgency("intimação", "citação do réu", prazo_dias=None),
            Urgency.IMPORTANT,
        )

    def test_important_sentenca(self):
        self.assertEqual(
            classify_urgency("sentença", "sentença proferida nos autos"),
            Urgency.IMPORTANT,
        )

    def test_info_juntada(self):
        self.assertEqual(
            classify_urgency("juntada", "juntada de petição inicial"),
            Urgency.INFO,
        )

    def test_info_certidao(self):
        self.assertEqual(
            classify_urgency("certidão", "certidão de publicação"),
            Urgency.INFO,
        )


class TestWebhookPayloadParsing(unittest.TestCase):
    """Test INTIMA.AI webhook payload parsing."""

    def test_parse_standard_payload(self):
        data = {
            "processo": "0001234-56.2026.5.01.0001",
            "tribunal": "TRT1",
            "tipo": "intimação",
            "descricao": "Prazo para contestação de 15 dias",
            "data": "2026-03-16",
            "prazo_dias": 15,
        }
        movement = parse_webhook_payload(data)
        self.assertEqual(movement.processo, "0001234-56.2026.5.01.0001")
        self.assertEqual(movement.tribunal, "TRT1")
        self.assertEqual(movement.prazo_dias, 15)
        self.assertEqual(movement.urgency, Urgency.CRITICAL)

    def test_parse_alternative_field_names(self):
        data = {
            "numero_processo": "0001234-56.2026.5.01.0001",
            "orgao": "TJSP",
            "tipo_movimentacao": "juntada",
            "texto": "Juntada de documento",
            "data_movimentacao": "2026-03-16",
        }
        movement = parse_webhook_payload(data)
        self.assertEqual(movement.processo, "0001234-56.2026.5.01.0001")
        self.assertEqual(movement.tribunal, "TJSP")
        self.assertEqual(movement.urgency, Urgency.INFO)

    def test_invalid_prazo_dias_ignored(self):
        data = {"processo": "123", "tipo": "x", "descricao": "y", "data": "z", "prazo_dias": "invalid"}
        movement = parse_webhook_payload(data)
        self.assertIsNone(movement.prazo_dias)


class TestTelegramAlertFormat(unittest.TestCase):
    """Test Telegram alert message formatting."""

    def test_critical_format_has_emoji(self):
        from handlers.legal_webhook import CourtMovement
        m = CourtMovement(
            processo="0001234-56.2026.5.01.0001",
            tribunal="TRT1",
            tipo="intimação",
            descricao="Prazo para contestação",
            data="2026-03-16",
            prazo_dias=3,
            urgency=Urgency.CRITICAL,
        )
        text = format_telegram_alert(m)
        self.assertIn("🔴", text)
        self.assertIn("CRITICAL", text)
        self.assertIn("3 dias", text)


class TestWebhookSignature(unittest.TestCase):
    """Test webhook HMAC validation."""

    def test_valid_signature(self):
        import hashlib, hmac as hmac_mod
        secret = "test_secret"
        payload = b'{"test": true}'
        sig = hmac_mod.new(secret.encode(), payload, hashlib.sha256).hexdigest()
        self.assertTrue(validate_webhook_signature(payload, sig, secret))

    def test_invalid_signature(self):
        self.assertFalse(validate_webhook_signature(b"data", "bad_sig", "secret"))


class TestCheckCaseStatus(unittest.IsolatedAsyncioTestCase):
    """Test check_case_status tool."""

    async def test_empty_processo(self):
        result = await check_case_status("")
        self.assertIn("provide a case number", result)

    @patch("tools.legal._get_intima_token", return_value=None)
    async def test_missing_token(self, _):
        result = await check_case_status("0001234-56.2026.5.01.0001")
        self.assertIn("INTIMA_AI_TOKEN not configured", result)

    @patch("tools.legal._intima_request")
    async def test_case_found(self, mock_req):
        mock_req.return_value = {
            "data": {
                "tribunal": "TRT1",
                "status": "Em andamento",
                "movimentacoes": [
                    {"data": "2026-03-15", "descricao": "Juntada de petição"}
                ],
            }
        }
        result = await check_case_status("0001234-56.2026.5.01.0001")
        self.assertIn("TRT1", result)
        self.assertIn("Em andamento", result)


class TestListDeadlines(unittest.IsolatedAsyncioTestCase):
    """Test list_deadlines tool."""

    @patch("tools.legal._intima_request")
    async def test_no_deadlines(self, mock_req):
        mock_req.return_value = {"data": []}
        result = await list_deadlines()
        self.assertIn("No active deadlines", result)

    @patch("tools.legal._intima_request")
    async def test_deadlines_with_urgency(self, mock_req):
        mock_req.return_value = {
            "data": [
                {"processo": "0001234", "tipo": "Contestação", "dias_restantes": 2, "vencimento": "2026-03-18"},
            ]
        }
        result = await list_deadlines()
        self.assertIn("🔴", result)
        self.assertIn("2 dias", result)


if __name__ == "__main__":
    unittest.main()
