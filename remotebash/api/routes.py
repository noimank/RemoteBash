"""FastAPI REST routes — clients & audit."""

from fastapi import APIRouter, Query, Request
from fastapi.responses import JSONResponse

router = APIRouter(prefix="/api", tags=["api"])


def _mgr(request: Request):
    return request.app.state.manager


# ═══════════════════════════════════════════════════════════════════════
# Clients
# ═══════════════════════════════════════════════════════════════════════

@router.get("/clients")
async def list_clients(request: Request):
    return _mgr(request).list_all()


@router.post("/clients", status_code=201)
async def add_client(request: Request):
    body = await request.json()
    name = body.get("name", "").strip()
    host = body.get("host", "").strip()
    user = body.get("user", "").strip()
    password = body.get("password", "")

    if not name or not host or not user:
        return JSONResponse({"error": "name, host and user are required"}, status_code=400)
    if not all(c.isalnum() or c in "-_" for c in name):
        return JSONResponse({"error": "name: only alphanumeric, hyphens, underscores"}, status_code=400)

    label = body.get("label", "").strip()
    enabled = body.get("enabled", True)
    auto_connect = body.get("auto_connect", True)
    port = int(body.get("port", 22))

    mgr = _mgr(request)
    try:
        info = await mgr.add(name=name, host=host, user=user, password=password,
                             port=port, label=label, enabled=enabled)
    except ValueError as exc:
        return JSONResponse({"error": str(exc)}, status_code=409)

    if auto_connect and enabled:
        try:
            await mgr.get(name).connect()
            info = mgr.get(name).to_dict()
            info["label"] = label
        except Exception as exc:
            return JSONResponse({"error": f"Added but connect failed: {exc}", "name": name}, status_code=500)

    return info


@router.delete("/clients/{client_name}")
async def remove_client(client_name: str, request: Request):
    try:
        await _mgr(request).remove(client_name)
        return {"ok": True}
    except KeyError:
        return JSONResponse({"error": f"Client '{client_name}' not found"}, status_code=404)


@router.post("/clients/{client_name}/connect")
async def connect_client(client_name: str, request: Request):
    try:
        await _mgr(request).get(client_name).connect()
        return {"ok": True, "name": client_name}
    except KeyError:
        return JSONResponse({"error": f"Client '{client_name}' not found"}, status_code=404)
    except Exception as exc:
        return JSONResponse({"error": str(exc)}, status_code=500)


@router.post("/clients/{client_name}/disconnect")
async def disconnect_client(client_name: str, request: Request):
    try:
        await _mgr(request).get(client_name).disconnect()
        return {"ok": True, "name": client_name}
    except KeyError:
        return JSONResponse({"error": f"Client '{client_name}' not found"}, status_code=404)


@router.post("/clients/{client_name}/test")
async def test_client(client_name: str, request: Request):
    try:
        await _mgr(request).get(client_name).test_connection()
        return {"ok": True, "name": client_name}
    except KeyError:
        return JSONResponse({"error": f"Client '{client_name}' not found"}, status_code=404)
    except Exception as exc:
        return JSONResponse({"error": str(exc)}, status_code=500)


@router.patch("/clients/{client_name}")
async def update_client(client_name: str, request: Request):
    body = await request.json()
    try:
        return await _mgr(request).update(client_name, **body)
    except KeyError:
        return JSONResponse({"error": f"Client '{client_name}' not found"}, status_code=404)


# ═══════════════════════════════════════════════════════════════════════
# Audit
# ═══════════════════════════════════════════════════════════════════════

@router.get("/audit")
async def list_audit(request: Request,
                     client_name: str | None = Query(None),
                     limit: int = Query(200, ge=1, le=1000),
                     offset: int = Query(0, ge=0)):
    mgr = _mgr(request)
    entries = await mgr.audit_list(client_name=client_name, limit=limit, offset=offset)
    total = await mgr.audit_count(client_name=client_name)
    return {"entries": entries, "total": total, "limit": limit, "offset": offset}


@router.delete("/audit")
async def delete_audit(request: Request,
                       entry_id: int | None = Query(None),
                       client_name: str | None = Query(None),
                       before_id: int | None = Query(None)):
    deleted = await _mgr(request).audit_delete(
        entry_id=entry_id, client_name=client_name, before_id=before_id)
    return {"ok": True, "deleted": deleted}
