#!/usr/bin/env python3
"""Check local container boundaries without running Codex or contacting a model."""

import argparse
import json
from pathlib import Path
import re
import subprocess
import tempfile
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image", help="probe image pinned as repository@sha256:digest")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}", args.image):
        parser.error("an immutable image digest is required")

    with tempfile.TemporaryDirectory(prefix="agent-platform-runtime-probe-") as directory:
        workspace = Path(directory) / "workspace"
        workspace.mkdir(mode=0o755)
        fixture = workspace / "frozen.txt"
        fixture.write_text("frozen input\n", encoding="utf-8")
        fixture.chmod(0o444)
        name = "agent-platform-isolation-probe-" + uuid.uuid4().hex
        checks = r'''
set -eu
printf 'checking non-root identity\n' >&2
test "$(id -u)" != 0
test "$(awk '$1 == "CapEff:" {print $2}' /proc/self/status)" = 0000000000000000
test "$(awk '$1 == "NoNewPrivs:" {print $2}' /proc/self/status)" = 1
printf 'checking Workspace readability\n' >&2
test "$(cat /workspace/frozen.txt)" = 'frozen input'
printf 'checking read-only mount flags\n' >&2
for mount_point in / /workspace; do
    mount_flags=$(awk -v p="$mount_point" '$2 == p {print $4; exit}' /proc/mounts)
    case ",$mount_flags," in *,ro,*) ;; *) printf 'mount is not read-only: %s %s\n' "$mount_point" "$mount_flags" >&2; exit 1 ;; esac
done
printf 'checking denied filesystem writes\n' >&2
if (printf tampered > /workspace/frozen.txt) 2>/dev/null; then exit 1; fi
if (touch /etc/runtime-write-probe) 2>/dev/null; then exit 1; fi
printf 'checking isolated network namespace\n' >&2
for network_interface in /sys/class/net/*; do
    test -d "$network_interface" || continue
    test "$(basename "$network_interface")" != lo || continue
    network_flags=$(cat "$network_interface/flags")
    test "$((network_flags & 1))" -eq 0
done
test -z "$(ip -4 route show default)"
test -z "$(ip -6 route show default)"
printf 'checking excluded credentials\n' >&2
test -z "${AGENT_PLATFORM_GITLAB_TOKEN-}${CODEX_API_KEY-}${OPENAI_API_KEY-}"
printf 'checking temporary Codex home\n' >&2
mkdir "$CODEX_HOME"
printf ephemeral > "$CODEX_HOME/probe"
test "$(cat "$CODEX_HOME/probe")" = ephemeral
printf '%s\n' '{"nonRoot":true,"readonlyRoot":true,"readonlyWorkspace":true,"networkNone":true,"temporaryHome":true,"noCredentials":true}'
'''
        command = [
            "docker", "run", "--rm", "--name", name,
            "--network=none", "--read-only", "--user=65534:65534",
            "--cap-drop=ALL", "--security-opt=no-new-privileges",
            "--pids-limit=32", "--memory=64m", "--cpus=0.5",
            "--tmpfs=/tmp:rw,noexec,nosuid,size=16m,mode=1777",
            "--mount=type=bind,source=" + str(workspace) + ",target=/workspace,readonly",
            "--env=CODEX_HOME=/tmp/codex-home", args.image,
            "/bin/sh", "-ec", checks,
        ]
        try:
            result = subprocess.run(command, check=True, capture_output=True, text=True, timeout=45)
        except subprocess.TimeoutExpired:
            # Only remove this invocation's container; never touch existing containers.
            subprocess.run(["docker", "rm", "-f", name], capture_output=True, timeout=15)
            raise
        except subprocess.CalledProcessError as error:
            raise RuntimeError("Container isolation probe failed: " + error.stderr.strip()) from None
        if fixture.read_text(encoding="utf-8") != "frozen input\n":
            raise RuntimeError("container modified the original Workspace fixture")
        evidence = json.loads(result.stdout)
        evidence["image"] = args.image
        evidence["hostInputUnchanged"] = True
        print(json.dumps(evidence))


if __name__ == "__main__":
    main()
