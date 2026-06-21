from pathlib import Path

from graphics_mcp.asset_store import AssetStore


def test_create_asset_builds_expected_directories(tmp_path):
    store = AssetStore(tmp_path / "assets")

    asset = store.create_asset(
        name="router-diagram",
        kind="mermaid",
        source_format="mmd",
    )

    asset_dir = tmp_path / "assets" / asset.asset_id
    assert asset_dir.is_dir()
    assert (asset_dir / "source").is_dir()
    assert (asset_dir / "preview").is_dir()
    assert (asset_dir / "export").is_dir()
    assert (asset_dir / "history").is_dir()
    assert (asset_dir / "meta.json").exists()
    assert asset.kind == "mermaid"
    assert asset.source_path.endswith("source/main.mmd")
