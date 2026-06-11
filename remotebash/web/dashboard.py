"""Dashboard page."""

from pathlib import Path

from fastapi import APIRouter, Request
from fastapi.responses import HTMLResponse
from fastapi.templating import Jinja2Templates

router = APIRouter(prefix="", tags=["dashboard"])

_TEMPLATES = Jinja2Templates(directory=str(Path(__file__).parent / "templates"))


@router.get("/", response_class=HTMLResponse)
async def dashboard(request: Request) -> HTMLResponse:
    return _TEMPLATES.TemplateResponse(
        request=request, name="dashboard.html.j2",
        context={"version": "0.2.0", "active_page": "dashboard"},
    )
