"""RemoteBash — entry point.

Usage:
    uv run python main.py --transport http --port 8000
    uv run python main.py --transport sse  --port 9090
"""

from remotebash.app import main

if __name__ == "__main__":
    main()
