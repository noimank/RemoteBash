"""Server configuration."""

from dataclasses import dataclass, field
from pathlib import Path

DEFAULT_DB = Path.home() / ".remotebash" / "remotebash.db"


@dataclass
class ServerConfig:
    transport: str = "http"
    host: str = "0.0.0.0"
    port: int = 24587
    debug: bool = False
    db_path: Path = field(default_factory=lambda: DEFAULT_DB)
