"""WebSocket endpoint for the in-browser terminal.

Exposes one interactive PTY-backed shell per client at::

    ws://host/api/clients/{name}/terminal

Protocol
--------
* Client → Server, **binary** frames: raw bytes typed/pasted in xterm.js.
  They are forwarded verbatim to the PTY via ``shell.feed_raw``.
* Client → Server, **text** frames: JSON control messages, currently
  ``{"type": "resize", "cols": int, "rows": int}``.
* Server → Client, **binary** frames: raw PTY output bytes (colours
  preserved — xterm.js renders them).
* Server → Client, **text** frames: JSON status messages:
  ``{"type":"status","state":"connecting"}`` — shell is being set up
  ``{"type":"status","state":"ready"}`` — shell is live, PTY output follows

The underlying PersistentShell is owned by ConnectionManager and survives a
WebSocket disconnect, so closing the terminal tab does not lose shell state.
"""

import asyncio
import json
import logging

from fastapi import APIRouter, WebSocket, WebSocketDisconnect

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/api", tags=["terminal"])

# Max queued WebSocket frames before backpressure drops incoming PTY chunks.
_SEND_QUEUE_MAX = 256


def _mgr(websocket: WebSocket):
    return websocket.app.state.manager


async def _send_status(websocket: WebSocket, state: str):
    """Send a JSON status frame to the browser (best-effort, never raises)."""
    try:
        await websocket.send_text(json.dumps({"type": "status", "state": state}))
    except Exception:
        pass


@router.websocket("/clients/{client_name}/terminal")
async def terminal(websocket: WebSocket, client_name: str):
    """Bidirectional bridge between xterm.js and a remote PTY shell."""
    await websocket.accept()

    mgr = _mgr(websocket)
    try:
        session = mgr.get(client_name)
    except KeyError:
        await websocket.close(code=4404, reason="客户端不存在")
        return
    if not session.enabled:
        await websocket.close(code=4403, reason="客户端已禁用")
        return

    # Tell the browser the shell is being acquired / created.
    await _send_status(websocket, "connecting")

    # Acquire (or create) the persistent terminal shell.
    try:
        shell = await asyncio.wait_for(
            mgr.get_or_create_terminal(client_name), timeout=20)
    except asyncio.TimeoutError:
        logger.warning("terminal shell setup timed out for '%s'", client_name)
        await _send_status(websocket, "timeout")
        await websocket.close(code=4500, reason="终端启动超时，请重试")
        return
    except Exception as exc:
        logger.warning("terminal shell setup failed for '%s': %s", client_name, exc)
        await websocket.close(code=4500, reason=f"终端启动失败: {exc}")
        return

    # Shell is live — notify the browser.
    await _send_status(websocket, "ready")

    # Forward raw PTY output to the browser as it arrives.  The tap callback
    # is invoked from the shell's reader loop; we bounce through an asyncio
    # queue so the WebSocket send happens on our own task.
    #
    # The queue has a generous upper bound to prevent unbounded memory growth
    # when the PTY produces output faster than the WebSocket can drain it
    # (e.g. a runaway ``cat /dev/zero``).  When full, incoming chunks are
    # dropped — xterm.js will show a gap, but the shell session stays alive.
    send_queue: asyncio.Queue = asyncio.Queue(maxsize=_SEND_QUEUE_MAX)

    def _tap(chunk: bytes):
        try:
            send_queue.put_nowait(chunk)
        except asyncio.QueueFull:
            pass  # drop under backpressure; keep the shell alive

    detach = shell.attach_tap(_tap)

    # Discard any bytes that accumulated while no WebSocket was attached
    # (stale output from before the reconnect would only confuse the user).
    shell.clear_buffer()

    # Tap is now live — inject a newline so the shell prints a fresh prompt
    # immediately (same as `tmux attach`).  This is safe: an empty line at
    # the shell prompt is a no-op — the shell simply re-displays PS1.
    shell.feed_raw(b"\n")

    async def _pump_output():
        """Drain send_queue → websocket.send_bytes."""
        try:
            while True:
                chunk = await send_queue.get()
                await websocket.send_bytes(chunk)
        except (WebSocketDisconnect, asyncio.CancelledError):
            pass
        except Exception:
            logger.exception("output pump for '%s' failed", client_name)

    pump = asyncio.create_task(_pump_output())

    try:
        while True:
            msg = await websocket.receive()
            mtype = msg.get("type")

            if mtype == "websocket.disconnect":
                break
            if mtype == "websocket.receive":
                data = msg.get("bytes")
                if data is not None:
                    # Raw keystrokes/paste → straight to the PTY.
                    shell.feed_raw(data)
                    continue
                text = msg.get("text")
                if text:
                    try:
                        cmd = json.loads(text)
                    except json.JSONDecodeError:
                        continue
                    if cmd.get("type") == "resize":
                        try:
                            await shell.resize(int(cmd["cols"]), int(cmd["rows"]))
                        except (KeyError, ValueError, TypeError):
                            pass
    except WebSocketDisconnect:
        pass
    except Exception:
        logger.exception("terminal websocket for '%s' errored", client_name)
    finally:
        # Stop forwarding but keep the shell alive for the next reconnect.
        detach()
        pump.cancel()
        try:
            await pump
        except (asyncio.CancelledError, Exception):
            pass
        try:
            await websocket.close()
        except Exception:
            pass
