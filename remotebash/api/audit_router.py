"""Audit log page."""

from pathlib import Path

from fastapi import APIRouter, Request
from fastapi.responses import HTMLResponse
from fastapi.templating import Jinja2Templates

router = APIRouter(prefix="/audit", tags=["audit"])

_TEMPLATES = Jinja2Templates(directory=str(Path(__file__).parent.parent / "web" / "templates"))


@router.get("", response_class=HTMLResponse)
async def audit_page(request: Request) -> HTMLResponse:
    return _TEMPLATES.TemplateResponse(
        request=request, name="audit.html.j2",
        context={"version": "0.2.0", "active_page": "audit"},
    )
