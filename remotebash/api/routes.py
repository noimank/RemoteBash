"""FastAPI REST routes — clients & audit."""

import asyncio

import asyncssh
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
        return JSONResponse({"error": "名称、主机和用户为必填项"}, status_code=400)
    if not all(c.isalnum() or c in "-_" for c in name):
        return JSONResponse({"error": "名称：仅允许字母、数字、连字符和下划线"}, status_code=400)

    enabled = body.get("enabled", True)
    safe_rm = body.get("safe_rm", False)
    auto_connect = body.get("auto_connect", True)
    port = int(body.get("port", 22))

    mgr = _mgr(request)
    try:
        info = await mgr.add(name=name, host=host, user=user, password=password,
                             port=port, enabled=enabled, safe_rm=safe_rm)
    except ValueError as exc:
        return JSONResponse({"error": str(exc)}, status_code=409)

    if auto_connect and enabled:
        try:
            await mgr.get(name).connect()
            info = mgr.get(name).to_dict()
        except Exception as exc:
            return JSONResponse({"error": f"已添加但连接失败: {exc}", "name": name}, status_code=500)

    return info


@router.delete("/clients/{client_name}")
async def remove_client(client_name: str, request: Request):
    try:
        await _mgr(request).remove(client_name)
        return {"ok": True}
    except KeyError:
        return JSONResponse({"error": f"客户端 '{client_name}' 不存在"}, status_code=404)


@router.post("/clients/{client_name}/connect")
async def connect_client(client_name: str, request: Request):
    try:
        await _mgr(request).get(client_name).connect()
        return {"ok": True, "name": client_name}
    except KeyError:
        return JSONResponse({"error": f"客户端 '{client_name}' 不存在"}, status_code=404)
    except Exception as exc:
        return JSONResponse({"error": str(exc)}, status_code=500)


@router.post("/clients/{client_name}/disconnect")
async def disconnect_client(client_name: str, request: Request):
    try:
        await _mgr(request).get(client_name).disconnect()
        return {"ok": True, "name": client_name}
    except KeyError:
        return JSONResponse({"error": f"客户端 '{client_name}' 不存在"}, status_code=404)


@router.post("/clients/{client_name}/test")
async def test_client(client_name: str, request: Request):
    try:
        s = _mgr(request).get(client_name)
    except KeyError:
        return JSONResponse({"error": f"客户端 '{client_name}' 不存在"}, status_code=404)

    try:
        await s.test_connection()
    except asyncio.TimeoutError:
        return JSONResponse({
            "error": f"连接超时 — 无法在有效时间内连接到 {s.host}:{s.port}，请检查网络或防火墙",
            "host": s.host, "port": s.port,
        }, status_code=504)
    except asyncssh.PermissionDenied as exc:
        return JSONResponse({
            "error": f"认证失败 — 用户名或密码错误 ({s.user}@{s.host}:{s.port})",
            "host": s.host, "port": s.port,
        }, status_code=401)
    except asyncssh.PasswordChangeRequired:
        return JSONResponse({
            "error": f"认证失败 — 密码已过期，需要更改密码 ({s.user}@{s.host}:{s.port})",
            "host": s.host, "port": s.port,
        }, status_code=401)
    except (asyncssh.Error, OSError) as exc:
        msg = str(exc).lower()
        if "refused" in msg or "refused" in str(exc):
            detail = f"连接被拒绝 — {s.host}:{s.port} 端口未开放或 SSH 服务未运行"
        elif "no address" in msg or "resolve" in msg or "name" in msg:
            detail = f"无法解析主机名 — {s.host}，请检查主机地址是否正确"
        elif "no route" in msg or "unreachable" in msg or "network" in msg:
            detail = f"网络不可达 — 无法访问 {s.host}:{s.port}"
        else:
            detail = f"连接失败 — {s.host}:{s.port}，{exc}"
        return JSONResponse({
            "error": detail,
            "host": s.host, "port": s.port,
        }, status_code=500)
    except Exception as exc:
        return JSONResponse({
            "error": f"连接测试失败: {exc}",
            "host": s.host, "port": s.port,
        }, status_code=500)

    return {
        "ok": True, "name": client_name,
        "host": s.host, "port": s.port, "user": s.user,
    }


@router.patch("/clients/{client_name}")
async def update_client(client_name: str, request: Request):
    body = await request.json()
    try:
        return await _mgr(request).update(client_name, **body)
    except KeyError:
        return JSONResponse({"error": f"客户端 '{client_name}' 不存在"}, status_code=404)


# ═══════════════════════════════════════════════════════════════════════
# Audit
# ═══════════════════════════════════════════════════════════════════════

@router.get("/audit")
async def list_audit(request: Request,
                     client_name: str | None = Query(None),
                     after: str | None = Query(None, description="ISO 8601 start (inclusive)"),
                     before: str | None = Query(None, description="ISO 8601 end (exclusive)"),
                     limit: int = Query(200, ge=1, le=1000),
                     offset: int = Query(0, ge=0)):
    mgr = _mgr(request)
    entries = await mgr.audit_list(client_name=client_name, after=after, before=before,
                                   limit=limit, offset=offset)
    total = await mgr.audit_count(client_name=client_name, after=after, before=before)
    return {"entries": entries, "total": total, "limit": limit, "offset": offset}


@router.delete("/audit")
async def delete_audit(request: Request,
                       entry_id: int | None = Query(None),
                       client_name: str | None = Query(None),
                       before_id: int | None = Query(None)):
    deleted = await _mgr(request).audit_delete(
        entry_id=entry_id, client_name=client_name, before_id=before_id)
    return {"ok": True, "deleted": deleted}
