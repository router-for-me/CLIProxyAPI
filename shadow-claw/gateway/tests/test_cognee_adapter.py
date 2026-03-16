"""Tests for adapters/cognee_adapter.py — cognee KnowledgeVault adapter."""

import unittest
from unittest.mock import AsyncMock, MagicMock, patch

from adapters.cognee_adapter import is_cognee_available


class TestCogneeAvailability(unittest.TestCase):
    """Test cognee availability detection."""

    def test_is_cognee_available_returns_bool(self):
        result = is_cognee_available()
        self.assertIsInstance(result, bool)


class TestCogneeAdapterInterface(unittest.TestCase):
    """Test that CogneeKnowledgeVault has the same interface as KnowledgeVault."""

    def test_adapter_has_required_methods(self):
        """Verify CogneeKnowledgeVault exposes the same public API."""
        from adapters.cognee_adapter import CogneeKnowledgeVault

        # Check all required methods exist
        required_sync = [
            "ingest_sync", "lookup_sync", "get_by_ref_sync",
            "snapshot_context_sync", "list_by_source_sync", "delete_sync",
        ]
        required_async = [
            "ingest", "lookup", "get_by_ref",
            "snapshot_context", "list_by_source", "delete",
        ]

        for method in required_sync + required_async:
            self.assertTrue(
                hasattr(CogneeKnowledgeVault, method),
                f"CogneeKnowledgeVault missing method: {method}",
            )

    @unittest.skipUnless(is_cognee_available(), "cognee not installed")
    def test_instantiation(self):
        from adapters.cognee_adapter import CogneeKnowledgeVault
        vault = CogneeKnowledgeVault()
        self.assertIsNotNone(vault)

    def test_instantiation_fails_without_cognee(self):
        """When cognee is not installed, constructor should raise ImportError."""
        with patch("adapters.cognee_adapter._cognee_available", False):
            from adapters.cognee_adapter import CogneeKnowledgeVault
            with self.assertRaises(ImportError):
                CogneeKnowledgeVault()


class TestCogneeAdapterFallback(unittest.TestCase):
    """Test that snapshot_context returns empty string on failure."""

    @patch("adapters.cognee_adapter._cognee_available", True)
    def test_snapshot_context_empty_on_no_results(self):
        from adapters.cognee_adapter import CogneeKnowledgeVault

        with patch.object(CogneeKnowledgeVault, "lookup_sync", return_value=[]):
            vault = CogneeKnowledgeVault.__new__(CogneeKnowledgeVault)
            vault._initialized = True
            result = vault.snapshot_context_sync("test query")
            self.assertEqual(result, "")

    @patch("adapters.cognee_adapter._cognee_available", True)
    def test_snapshot_context_formats_results(self):
        from adapters.cognee_adapter import CogneeKnowledgeVault

        mock_results = [
            {"key": "ref1", "content": "Test content 1", "tags": ""},
            {"key": "ref2", "content": "Test content 2", "tags": ""},
        ]
        with patch.object(CogneeKnowledgeVault, "lookup_sync", return_value=mock_results):
            vault = CogneeKnowledgeVault.__new__(CogneeKnowledgeVault)
            vault._initialized = True
            result = vault.snapshot_context_sync("test")
            self.assertIn("Relevant knowledge:", result)
            self.assertIn("[ref1]", result)
            self.assertIn("[ref2]", result)


if __name__ == "__main__":
    unittest.main()
