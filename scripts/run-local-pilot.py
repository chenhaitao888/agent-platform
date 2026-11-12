#!/usr/bin/env python3
"""Run the authenticated loopback pilot in the foreground; never starts a review."""
import argparse
import datetime
import json
import os
from pathlib import Path
import signal
import socket
import stat
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request


REPO = Path(__file__).resolve().parents[1]
ENV_FIELDS = {
    "gitlabURL": "GITLAB_BASE_URL",
    "gitlabCAFile": "GITLAB_CA_FILE",
    "dockerHost": "DOCKER_HOST",
    "workerImage": "REVIEW_WORKER_IMAGE",
    "model": "REVIEW_MODEL",
    "modelURL": "MODEL_BASE_URL",
    "servicePrincipal": "MODEL_SERVICE_PRINCIPAL",
    "modelCredentialFile": "MODEL_CREDENTIAL_FILE",
}
STOP = threading.Event()


class PilotError(Exception):
    pass


def private_json(path):
    # No credential values or parser exceptions are included in diagnostics.
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd) as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise PilotError("configuration and credential files must be owned by this user with mode 0600")
        if info.st_size > 65536:
            raise PilotError("configuration or credential file exceeds 64 KiB")
        value = json.load(stream)
    if not isinstance(value, dict):
        raise PilotError("configuration and credentials must be JSON objects")
    return value


def configuration(path):
    config = private_json(path)
    fields = set(ENV_FIELDS) | {"stateDir", "nodeBinary", "goBinary", "apiPort", "frontendPort", "gitlabCredentialFile"}
    if set(config) != fields:
        raise PilotError("pilot configuration has missing or unsupported fields")
    for key in fields - {"apiPort", "frontendPort"}:
        if not isinstance(config[key], str) or not config[key].strip():
            raise PilotError("pilot configuration requires nonempty strings")
    for key in ("apiPort", "frontendPort"):
        if type(config[key]) is not int or not 1024 <= config[key] <= 65535:
            raise PilotError("pilot ports must be integers between 1024 and 65535")
    if config["apiPort"] == config["frontendPort"]:
        raise PilotError("API and frontend need different ports")
    for key in ("stateDir", "nodeBinary", "goBinary", "gitlabCredentialFile", "gitlabCAFile", "modelCredentialFile"):
        if not Path(config[key]).is_absolute():
            raise PilotError("pilot file paths must be absolute")
    state = Path(config["stateDir"]).resolve(strict=True)
    if state == Path("/") or state.is_relative_to(REPO) or stat.S_IMODE(state.stat().st_mode) != 0o700 or state.stat().st_uid != os.getuid():
        raise PilotError("stateDir must be a user-owned 0700 directory outside the checkout")
    root = state / "workspaces"
    for key in ("gitlabCredentialFile", "modelCredentialFile"):
        if Path(config[key]).resolve().is_relative_to(root.resolve()):
            raise PilotError("credential files must be outside workspaces")
    private_json(state / "auth/api-auth.json")
    credential = private_json(config["gitlabCredentialFile"])
    if not isinstance(credential.get("token"), str) or not credential["token"].strip():
        raise PilotError("GitLab credential is missing a token")
    if set(credential.get("scopes", [])) != {"read_api", "read_repository"}:
        raise PilotError("GitLab credential must declare only read_api and read_repository")
    expiry = datetime.date.fromisoformat(credential["expiresAt"][:10])
    if expiry <= datetime.datetime.now(datetime.timezone.utc).date():
        raise PilotError("GitLab credential has expired")
    # Only check model file metadata. The backend reads its contents at review time.
    info = Path(config["modelCredentialFile"]).lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_uid != os.getuid():
        raise PilotError("model credential must be a user-owned regular file with mode 0600")
    return config, state, credential["token"]


def environments(config, state, token):
    # Do not inherit arbitrary parent environment (including VITE_*, proxies or credentials).
    common = {
        "HOME": str(Path.home()), "LANG": "en_US.UTF-8",
        "PATH": f"{Path(config['nodeBinary']).parent}:{Path(config['goBinary']).parent}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
        "TMPDIR": os.environ.get("TMPDIR", "/private/tmp"),
    }
    api = dict(common)
    api.update({"AGENT_PLATFORM_" + field: config[key] for key, field in ENV_FIELDS.items()})
    api.update({
        "AGENT_PLATFORM_API_ADDR": f"127.0.0.1:{config['apiPort']}",
        "AGENT_PLATFORM_AUTH_FILE": str(state / "auth/api-auth.json"),
        "AGENT_PLATFORM_WORKSPACE_ROOT": str(state / "workspaces"),
        "AGENT_PLATFORM_GITLAB_TOKEN": token,
        "AGENT_PLATFORM_REVIEW_ENABLED": "true",
        "AGENT_PLATFORM_REVIEW_SECCOMP_POLICY": "codex-bwrap",
    })
    frontend = dict(common, AGENT_PLATFORM_DEV_API_URL=f"http://127.0.0.1:{config['apiPort']}")
    build = dict(common, GOTOOLCHAIN="local", GOCACHE=str(state / "go-cache"), GOPATH=str(state / "go-path"))
    return api, frontend, build


def free_ports(config):
    sockets = []
    try:
        for port in (config["apiPort"], config["frontendPort"]):
            listener = socket.socket()
            sockets.append(listener)
            listener.bind(("127.0.0.1", port))
    except OSError:
        raise PilotError("pilot port is occupied; existing services were left running") from None
    finally:
        for listener in sockets:
            listener.close()


def wait_ready(url, children, timeout=30):
    # Ignore host proxy settings, even for health checks.
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + timeout
    while not STOP.is_set() and time.monotonic() < deadline:
        if any(child.poll() is not None for child in children):
            raise PilotError("pilot child exited during startup; inspect the private service log")
        try:
            with client.open(url, timeout=1) as response:
                if response.status == 200:
                    return
        except (OSError, urllib.error.URLError):
            pass
        STOP.wait(0.1)
    if STOP.is_set():
        raise InterruptedError
    raise PilotError("pilot health check timed out")


def shutdown(children, grace=150):
    for child in children:
        if child.poll() is None:
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
    deadline = time.monotonic() + grace
    for child in children:
        try:
            child.wait(timeout=max(0, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            child.wait()


def run(config, state, token, check=False):
    api_env, frontend_env, build_env = environments(config, state, token)
    free_ports(config)
    version = subprocess.check_output([config["nodeBinary"], "--version"], env=frontend_env, timeout=10, stderr=subprocess.DEVNULL).decode().strip().lstrip("v").split(".")
    if int(version[0]) != 22 or int(version[1]) < 13:
        raise PilotError("frontend requires Node 22.13–22.x")
    vite = REPO / "frontend/node_modules/vite/bin/vite.js"
    if not vite.is_file():
        raise PilotError("install frontend dependencies with Node 22 and npm ci first")
    if check:
        print("Pilot configuration, permissions, ports and Node checked; no services or model calls started.", flush=True)
        return
    binary = state / "bin/api"
    binary.parent.mkdir(mode=0o700, exist_ok=True)
    subprocess.run([config["goBinary"], "build", "-o", str(binary), "./cmd/api"], cwd=REPO / "backend", env=build_env, check=True, timeout=180)
    children = []
    try:
        if STOP.is_set():
            return
        children.append(subprocess.Popen([str(binary)], cwd=REPO, env=api_env, start_new_session=True))
        wait_ready(f"http://127.0.0.1:{config['apiPort']}/healthz", children)
        children.append(subprocess.Popen([config["nodeBinary"], str(vite), "--host", "127.0.0.1", "--port", str(config["frontendPort"]), "--strictPort"], cwd=REPO / "frontend", env=frontend_env, start_new_session=True))
        wait_ready(f"http://127.0.0.1:{config['frontendPort']}/api/healthz", children)
        print(f"Pilot ready: http://127.0.0.1:{config['frontendPort']} (API {config['apiPort']}). Reviews require an explicit UI action. Restart clears in-memory records.", flush=True)
        while not STOP.wait(0.25):
            if any(child.poll() is not None for child in children):
                raise PilotError("pilot child exited; stopping the other child")
    finally:
        shutdown(children)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--check", action="store_true", help="offline preflight only; not a dependency or API readiness check")
    args = parser.parse_args()
    os.umask(0o077)
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: STOP.set())
    try:
        config, state, token = configuration(args.config)
        run(config, state, token, args.check)
    except InterruptedError:
        return 0
    except PilotError as error:
        print(f"Pilot: {error}", file=sys.stderr)
        return 1
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
        print("Pilot: configuration, dependency or process check failed; credential values are omitted.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
