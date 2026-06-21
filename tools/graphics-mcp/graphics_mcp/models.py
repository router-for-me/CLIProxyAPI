from __future__ import annotations

from dataclasses import asdict, dataclass, field
from typing import Any


@dataclass
class DiagramNode:
    id: str
    label: str
    kind: str = "service"
    x: int = 0
    y: int = 0


@dataclass
class DiagramEdge:
    source: str
    target: str
    label: str = ""


@dataclass
class DiagramSpec:
    title: str
    nodes: list[DiagramNode]
    edges: list[DiagramEdge]

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)

    @classmethod
    def from_payload(
        cls,
        title: str,
        nodes: list[dict[str, Any]],
        edges: list[dict[str, Any]],
    ) -> "DiagramSpec":
        return cls(
            title=title,
            nodes=[DiagramNode(**node) for node in nodes],
            edges=[DiagramEdge(**edge) for edge in edges],
        )


@dataclass
class GraphicAsset:
    asset_id: str
    kind: str
    source_format: str
    source_path: str
    preview_path: str
    exports: list[str] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)
    lineage: list[str] = field(default_factory=list)
    status: str = "draft"

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "GraphicAsset":
        return cls(
            asset_id=data["asset_id"],
            kind=data["kind"],
            source_format=data["source_format"],
            source_path=data["source_path"],
            preview_path=data["preview_path"],
            exports=list(data.get("exports", [])),
            metadata=dict(data.get("metadata", {})),
            lineage=list(data.get("lineage", [])),
            status=data.get("status", "draft"),
        )
