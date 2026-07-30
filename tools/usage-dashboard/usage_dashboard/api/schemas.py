"""Pydantic response models for FastAPI OpenAPI schema generation."""

from pydantic import BaseModel


class SummaryStats(BaseModel):
    requests: int
    total_tokens: int
    input_tokens: int
    output_tokens: int
    reasoning_tokens: int
    cached_tokens: int
    cache_read_tokens: int
    cache_creation_tokens: int
    failed: int
    success_latency_ms: int
    success_requests: int
    success_ttft_ms: int
    estimated_cost: float
    estimated_cost_currency: str


class AccountSummary(BaseModel):
    account: str
    requests: int
    total_tokens: int
    input_tokens: int
    output_tokens: int
    reasoning_tokens: int
    failed: int


class ModelSummary(BaseModel):
    model: str
    requests: int
    total_tokens: int
    input_tokens: int
    output_tokens: int
    reasoning_tokens: int
    cached_tokens: int
    estimated_cost: float
    priced: bool
    unpriced_requests: int


class HourSummary(BaseModel):
    hour: str
    requests: int
    total_tokens: int
    failed: int


class SummaryResponse(BaseModel):
    range: str
    models_filter: list[str]
    accounts_filter: list[str]
    summary: SummaryStats
    accounts: list[AccountSummary]
    models: list[ModelSummary]
    hours: list[HourSummary]
    price_coverage: str