#!/usr/bin/env python3
"""Generate new private local PostgreSQL settings; never print the password."""
import argparse
import json
import os
from pathlib import Path
import secrets

parser = argparse.ArgumentParser()
parser.add_argument('--directory', required=True)
parser.add_argument('--port', type=int, default=15432)
args = parser.parse_args()
directory = Path(args.directory)
repo = Path(__file__).resolve().parents[2]
if not directory.is_absolute() or directory.resolve().is_relative_to(repo) or not 1024 <= args.port <= 65535:
    parser.error('use a new absolute private directory outside the checkout and a port between 1024 and 65535')
directory.mkdir(mode=0o700, parents=False, exist_ok=False)
password = secrets.token_urlsafe(32)
for name, content in {
    'password': password,
    'database.json': json.dumps({'url': f'postgres://agent_platform:{password}@127.0.0.1:{args.port}/agent_platform?sslmode=disable'}),
}.items():
    fd = os.open(directory / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as stream:
        stream.write(content + '\n')
print(f'Created private D4 settings in {directory}; no database has been started.')
