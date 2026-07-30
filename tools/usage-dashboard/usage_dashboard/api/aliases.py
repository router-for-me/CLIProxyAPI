"""Router for alias mutation endpoints — GET / PUT / DELETE /api/v1/aliases."""
from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel

from .. import storage as st

router = APIRouter()


class AliasBody(BaseModel):
    account_hash: str
    alias: str


@router.get("/api/v1/aliases")
def list_aliases(request: Request):
    """Return all key aliases."""
    cfg = request.app.state.cfg
    return st.get_aliases(cfg)


@router.put("/api/v1/aliases")
def upsert_alias(body: AliasBody, request: Request):
    """Create or update a key alias."""
    cfg = request.app.state.cfg
    if not body.account_hash.strip() or not body.alias.strip():
        raise HTTPException(400, "account_hash and alias required")
    if len(body.account_hash) > 128 or len(body.alias) > 256:
        raise HTTPException(400, "account_hash or alias too long")
    st.upsert_alias(cfg, body.account_hash.strip(), body.alias.strip())
    return {"ok": True}


@router.delete("/api/v1/aliases/{account_hash}")
def delete_alias(account_hash: str, request: Request):
    """Delete a key alias by account_hash."""
    cfg = request.app.state.cfg
    if not account_hash:
        raise HTTPException(400, "account_hash required")
    st.delete_alias(cfg, account_hash)
    return {"ok": True}
