#!/usr/bin/env python3
"""Run the Go adapter against an unchanged, separately installed SCIM server."""
import importlib.metadata
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


def main():
    if importlib.metadata.version("scim2-server") != "0.8.0":
        raise SystemExit("Install scripts/scim-interop-requirements.txt first")
    server_bin = shutil.which("scim2-server")
    go_bin = shutil.which("go")
    if not server_bin or not go_bin:
        raise SystemExit("scim2-server and go must be on PATH")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    token = secrets.token_hex(24)
    url = f"http://127.0.0.1:{port}/v2"
    env = dict(os.environ, IDENTITY_SCIM_TEST_URL=url, IDENTITY_SCIM_TEST_TOKEN=token)
    with tempfile.TemporaryFile(mode="w+") as log:
        server = subprocess.Popen(
            [server_bin, "--hostname", "127.0.0.1", "--port", str(port), "--bearer-token", token],
            stdout=log, stderr=subprocess.STDOUT,
        )
        try:
            deadline = time.monotonic() + 15
            ready = False
            while time.monotonic() < deadline and server.poll() is None:
                request = urllib.request.Request(url + "/ServiceProviderConfig", headers={"Authorization": "Bearer " + token})
                try:
                    with urllib.request.urlopen(request, timeout=1) as response:
                        ready = response.status == 200
                    if ready:
                        break
                except OSError:
                    time.sleep(0.1)
            if not ready:
                raise RuntimeError("independent SCIM server did not become ready")
            print("Target: python-scim/scim2-server 0.8.0; unmodified in-memory backend", flush=True)
            result = subprocess.run(
                [go_bin, "test", "-race", "./pkg/adapter/scim", "-run", "TestSCIMInterop", "-count=1", "-v"],
                cwd=Path(__file__).resolve().parents[1], env=env, timeout=120,
            )
            return result.returncode
        finally:
            server.terminate()
            try:
                server.wait(timeout=5)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()


if __name__ == "__main__":
    sys.exit(main())
