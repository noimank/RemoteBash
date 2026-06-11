"""Server configuration."""

import os
import sys
from dataclasses import dataclass, field
from pathlib import Path

Transport = "http"  # or "sse"

# Default database directory — platform-specific user data dir.
if sys.platform == "win32":
    _base = os.environ.get("APPDATA", str(Path.home() / "AppData" / "Roaming"))
elif sys.platform == "darwin":
    _base = str(Path.home() / "Library" / "Application Support")
else:
    _base = os.environ.get("XDG_DATA_HOME", str(Path.home() / ".local" / "share"))
DEFAULT_DB = Path(_base) / "remotebash" / "remotebash.db"


@dataclass
class ServerConfig:
    transport: str = "http"
    host: str = "0.0.0.0"
    port: int = 24587
    debug: bool = False
    db_path: Path = field(default_factory=lambda: DEFAULT_DB)
