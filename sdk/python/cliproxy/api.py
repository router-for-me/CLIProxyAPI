"""
Comprehensive Python SDK for cliproxyapi-plusplus.

NOT just HTTP wrappers - provides native Python classes and functions.
Translates Go types to Python dataclasses with full functionality.
"""

import httpx
from dataclasses import dataclass, field
from typing import Any, Optional
from enum import Enum
import os


# =============================================================================
# Enums - Native Python
# =============================================================================

class ModelProvider(str, Enum):
    """Supported model providers."""
    OPENAI = "openai"
    ANTHROPIC = "anthropic"
    GOOGLE = "google"
    OPENROUTER = "openrouter"
    MINIMAX = "minimax"
    KIRO = "kiro"
    CODEX = "codex"
    CLAUDE = "claude"
    GEMINI = "gemini"
    VERTEX = "vertex"


# =============================================================================
# Models - Native Python classes
# =============================================================================

@dataclass
class ProviderConfig:
    """Native Python config for providers."""
    provider: ModelProvider
    api_key: Optional[str] = None
    base_url: Optional[str] = None
    models: list[str] = field(default_factory=list)
    timeout: int = 30
    max_retries: int = 3


@dataclass
class AuthEntry:
    """Authentication entry."""
    name: str
    provider: ModelProvider
    credentials: dict[str, Any] = field(default_factory=dict)
    enabled: bool = True


@dataclass
class ChatMessage:
    """Chat message with role support."""
    role: str  # "system", "user", "assistant"
    content: str
    name: Optional[str] = None


@dataclass
class ChatChoice:
    """Single chat choice."""
    index: int
    message: dict
    finish_reason: Optional[str] = None


@dataclass
class Usage:
    """Token usage."""
    prompt_tokens: int = 0
    completion_tokens: int = 0
    total_tokens: int = 0


@dataclass
class ChatCompletion:
    """Native Python completion response."""
    id: str
    object_type: str = "chat.completion"
    created: int = 0
    model: str = ""
    choices: list[ChatChoice] = field(default_factory=list)
    usage: Usage = field(default_factory=Usage)
    
    @property
    def first_choice(self) -> str:
        if self.choices and self.choices[0].message:
            return self.choices[0].message.get("content", "")
        return ""
    
    @property
    def text(self) -> str:
        return self.first_choice
    
    @property
    def content(self) -> str:
        return self.first_choice


@dataclass
class Model:
    """Model info."""
    id: str
    object_type: str = "model"
    created: Optional[int] = None
    owned_by: Optional[str] = None


@dataclass
class ModelList:
    """List of models."""
    object_type: str = "list"
    data: list[Model] = field(default_factory=list)


# =============================================================================
# Client - Full-featured Python SDK
# =============================================================================

class CliproxyClient:
    """Comprehensive Python SDK - NOT just HTTP wrapper.
    
    Provides native Python classes and functions for cliproxyapi-plusplus.
    """
    
    def __init__(
        self,
        base_url: str = "http://127.0.0.1:8317",
        api_key: Optional[str] = None,
        timeout: int = 30,
    ):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key or os.getenv("CLIPROXY_API_KEY", "8317")
        self.timeout = timeout
        self._client = httpx.Client(timeout=timeout)
    
    # -------------------------------------------------------------------------
    # High-level Python methods (not HTTP mapping)
    # -------------------------------------------------------------------------
    
    def chat(
        self,
        messages: list[ChatMessage],
        model: str = "claude-3-5-sonnet-20241022",
        **kwargs
    ) -> ChatCompletion:
        """Native Python chat - returns ChatCompletion object."""
        resp = self.completions_create(
            model=model,
            messages=[{"role": m.role, "content": m.content} for m in messages],
            **kwargs
        )
        return self._parse_completion(resp)
    
    def complete(
        self,
        prompt: str,
        model: str = "claude-3-5-sonnet-20241022",
        system: Optional[str] = None,
    ) -> str:
        """Simple completion - returns string."""
        msgs = []
        if system:
            msgs.append(ChatMessage(role="system", content=system))
        msgs.append(ChatMessage(role="user", content=prompt))
        
        resp = self.chat(msgs, model)
        return resp.first_choice
    
    # -------------------------------------------------------------------------
    # Mid-level operations
    # -------------------------------------------------------------------------
    
    def providers_list(self) -> list[str]:
        """List available providers."""
        return [p.value for p in ModelProvider]
    
    def auth_add(self, auth: AuthEntry) -> dict:
        """Add auth entry - native Python."""
        return self.management_request("POST", "/v0/management/auth", json=auth.__dict__)
    
    def config_update(self, **kwargs) -> dict:
        """Update config with kwargs."""
        return self.management_request("PUT", "/v0/management/config", json=kwargs)
    
    def models(self) -> ModelList:
        """List models as ModelList."""
        resp = self._request("GET", "/v1/models")
        return ModelList(
            object_type=resp.get("object", "list"),
            data=[Model(**m) for m in resp.get("data", [])]
        )
    
    # -------------------------------------------------------------------------
    # Low-level HTTP
    # -------------------------------------------------------------------------
    
    def completions_create(self, **kwargs) -> dict:
        """Raw OpenAI-compatible /v1/chat/completions."""
        return self._request("POST", "/v1/chat/completions", json=kwargs)
    
    def models_list_raw(self) -> dict:
        """List models raw."""
        return self._request("GET", "/v1/models")
    
    def management_request(
        self,
        method: str,
        path: str,
        **kwargs
    ) -> dict:
        """Management API."""
        return self._request(method, f"/v0/management{path}", **kwargs)
    
    def _request(
        self,
        method: str,
        path: str,
        **kwargs
    ) -> dict:
        """Base HTTP request."""
        url = f"{self.base_url}{path}"
        headers = {"Authorization": f"Bearer {self.api_key}"}
        headers.update(kwargs.pop("headers", {}))
        
        resp = self._client.request(method, url, headers=headers, **kwargs)
        resp.raise_for_status()
        return resp.json()
    
    def _parse_completion(self, resp: dict) -> ChatCompletion:
        """Parse completion response to Python object."""
        choices = [ChatChoice(**c) for c in resp.get("choices", [])]
        usage_data = resp.get("usage", {})
        usage = Usage(
            prompt_tokens=usage_data.get("prompt_tokens", 0),
            completion_tokens=usage_data.get("completion_tokens", 0),
            total_tokens=usage_data.get("total_tokens", 0)
        )
        return ChatCompletion(
            id=resp.get("id", ""),
            object_type=resp.get("object", "chat.completion"),
            created=resp.get("created", 0),
            model=resp.get("model", ""),
            choices=choices,
            usage=usage
        )
    
    def close(self):
        self._client.close()
    
    def __enter__(self):
        return self
    
    def __exit__(self, *args):
        self.close()


# =============================================================================
# Convenience functions
# =============================================================================

def client(**kwargs) -> CliproxyClient:
    """Create client - shortcut."""
    return CliproxyClient(**kwargs)


def chat(prompt: str, model: str = "claude-3-5-sonnet-20241022", **kwargs) -> str:
    """One-shot chat - returns string."""
    with CliproxyClient() as c:
        return c.complete(prompt, model, **kwargs)
