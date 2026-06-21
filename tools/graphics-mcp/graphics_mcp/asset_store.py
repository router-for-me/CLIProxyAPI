from __future__ import annotations

import json
import re
from datetime import UTC, datetime
from pathlib import Path
from uuid import uuid4

from graphics_mcp.models import GraphicAsset


def _slugify(value: str) -> str:
    cleaned = re.sub(r"[^a-zA-Z0-9._-]+", "-", value.strip()).strip("-._")
    return cleaned[:64] or "asset"


def _now() -> str:
    return datetime.now(UTC).isoformat()


class AssetStore:
    def __init__(self, assets_root: Path) -> None:
        self.assets_root = assets_root

    def create_asset(self, name: str, kind: str, source_format: str) -> GraphicAsset:
        asset_id = f"{_slugify(name)}-{uuid4().hex[:8]}"
        asset_dir = self.assets_root / asset_id
        source_dir = asset_dir / "source"
        preview_dir = asset_dir / "preview"
        export_dir = asset_dir / "export"
        history_dir = asset_dir / "history"

        for directory in (source_dir, preview_dir, export_dir, history_dir):
            directory.mkdir(parents=True, exist_ok=True)

        asset = GraphicAsset(
            asset_id=asset_id,
            kind=kind,
            source_format=source_format,
            source_path=(source_dir / f"main.{source_format}").as_posix(),
            preview_path=preview_dir.joinpath("main.svg").as_posix(),
            exports=[],
            metadata={
                "name": name,
                "created_at": _now(),
                "updated_at": _now(),
                "editable_formats": [source_format],
                "preferred_export_formats": ["svg", source_format],
            },
            lineage=[],
            status="draft",
        )
        self.save_asset(asset)
        return asset

    def save_asset(self, asset: GraphicAsset) -> GraphicAsset:
        asset.metadata["updated_at"] = _now()
        meta_path = self.assets_root / asset.asset_id / "meta.json"
        meta_path.write_text(
            json.dumps(asset.to_dict(), ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        return asset

    def load_asset(self, asset_id: str) -> GraphicAsset:
        meta_path = self.assets_root / asset_id / "meta.json"
        data = json.loads(meta_path.read_text(encoding="utf-8"))
        return GraphicAsset.from_dict(data)

    def save_source_text(self, asset_id: str, source_text: str) -> GraphicAsset:
        asset = self.load_asset(asset_id)
        source_path = Path(asset.source_path)
        source_path.write_text(source_text, encoding="utf-8")
        self._snapshot_history(asset, source_text)
        return self.save_asset(asset)

    def list_assets(self) -> list[GraphicAsset]:
        if not self.assets_root.exists():
            return []
        assets: list[GraphicAsset] = []
        for meta_path in sorted(self.assets_root.glob("*/meta.json")):
            data = json.loads(meta_path.read_text(encoding="utf-8"))
            assets.append(GraphicAsset.from_dict(data))
        return assets

    def _snapshot_history(self, asset: GraphicAsset, source_text: str) -> None:
        history_dir = self.assets_root / asset.asset_id / "history"
        version_name = datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
        history_path = history_dir / f"{version_name}.{asset.source_format}"
        history_path.write_text(source_text, encoding="utf-8")
