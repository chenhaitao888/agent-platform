#!/usr/bin/env python3
"""Build a Worker from a caller-selected Codex release; no personal config is copied."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex-version", required=True)
    parser.add_argument("--arch", choices=["arm64", "amd64"], required=True)
    parser.add_argument("--base-image", required=True)
    parser.add_argument("--docker-host", required=True)
    parser.add_argument("--codex-archive", type=Path, help="reuse a local archive after verifying registry integrity")
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?", args.codex_version):
        parser.error("supply an explicit published Codex version")
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9._/:\-]*@sha256:[0-9a-f]{64}", args.base_image):
        parser.error("base image must use a repository digest")
    if not args.docker_host.startswith("unix:///"):
        parser.error("supply the service's local Docker Unix socket")
    repo = Path(__file__).resolve().parents[1]
    docker, go = shutil.which("docker"), shutil.which("go")
    if not docker or not go:
        parser.error("Go and Docker are required")
    platform = "linux-arm64" if args.arch == "arm64" else "linux-x64"
    target = "aarch64-unknown-linux-musl" if args.arch == "arm64" else "x86_64-unknown-linux-musl"
    metadata_url = f"https://registry.npmjs.org/@openai%2fcodex/{args.codex_version}-{platform}"
    with urllib.request.urlopen(metadata_url, timeout=30) as response:
        metadata = json.load(response)
    distribution = metadata["dist"]
    if not distribution["tarball"].startswith("https://registry.npmjs.org/@openai/codex/"):
        raise ValueError("unexpected artifact registry")
    with tempfile.TemporaryDirectory(prefix="agent-platform-worker-build-") as directory:
        temporary = Path(directory)
        context = temporary / "context"
        context.mkdir()
        client = temporary / "client"
        client.mkdir()
        docker_env = {"PATH": "/usr/bin:/bin", "HOME": str(client), "DOCKER_CONFIG": str(client), "DOCKER_HOST": args.docker_host}
        def run_docker(*arguments):
            return subprocess.check_output([docker, *arguments], env=docker_env, timeout=180)
        # Base must already be present locally; runtime and build use its immutable ID.
        base = json.loads(run_docker("image", "inspect", args.base_image))[0]
        if base["Os"] != "linux" or base["Architecture"] != args.arch:
            raise ValueError("base image architecture does not match")
        artifact = temporary / "codex.tgz"
        digest = hashlib.sha512()
        if args.codex_archive:
            artifact = args.codex_archive.resolve(strict=True)
            if artifact.stat().st_size > 512 << 20:
                raise ValueError("Codex archive exceeds build limit")
            with artifact.open("rb") as cached:
                digest = hashlib.file_digest(cached, "sha512")
        else:
            with urllib.request.urlopen(distribution["tarball"], timeout=30) as response, artifact.open("wb") as output:
                total = 0
                while block := response.read(1 << 20):
                    total += len(block)
                    if total > 512 << 20:
                        raise ValueError("Codex archive exceeds build limit")
                    digest.update(block)
                    output.write(block)
        integrity = "sha512-" + base64.b64encode(digest.digest()).decode()
        if integrity != distribution["integrity"]:
            raise ValueError("Codex registry integrity mismatch")
        prefix = f"package/vendor/{target}/"
        with tarfile.open(artifact) as archive:
            members = [member for member in archive if member.name.startswith(prefix)]
            archive.extractall(temporary / "extracted", members=members, filter="data")
        vendor = temporary / "extracted" / prefix
        binary = vendor / "bin/codex"
        binary_digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        shutil.copytree(vendor, context / "codex")
        shutil.copyfile(repo / "worker/Dockerfile", context / "Dockerfile")
        go_env = {key: value for key, value in os.environ.items() if key in {"PATH", "GOCACHE", "GOPATH", "GOTOOLCHAIN"}}
        go_env.update(CGO_ENABLED="0", GOOS="linux", GOARCH=args.arch)
        subprocess.run([go, "build", "-o", str(context / "reviewworker"), "./cmd/reviewworker"], cwd=repo / "backend", env=go_env, timeout=180, check=True)
        image_file = temporary / "image-id"
        run_docker("build", "--pull=false", "--network=none", "--build-arg", "BASE_IMAGE=" + base["Id"], "--iidfile", str(image_file), str(context))
        print(json.dumps(dict(imageId=image_file.read_text().strip(), baseImageId=base["Id"], architecture=args.arch,
                             codexVersion=args.codex_version, codexBinarySha256=binary_digest, artifact=distribution["tarball"], integrity=integrity), indent=2))


if __name__ == "__main__":
    main()
